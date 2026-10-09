#!/usr/bin/env python3
"""Validate/finalize exact group evidence, then recompute the aggregate result."""
import argparse
from datetime import datetime
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import sys

from integration_groups import GROUPS, ROOT, SMOKE_GROUP, SMOKES, load, plan_sha256, read_json, selected

spec = importlib.util.spec_from_file_location('integration_gate', ROOT / 'scripts/check-integration-results.py')
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)
EVIDENCE = ('before.jsonl', 'after.jsonl', 'cluster-before.txt', 'cluster-after.txt', 'process-smokes.txt')


def cluster(path):
    value = path.read_text().strip().split('|')
    if len(value) != 2 or not re.fullmatch(r'[0-9]+', value[0]):
        raise ValueError('invalid PostgreSQL cluster identity evidence')
    started = datetime.fromisoformat(value[1])
    if started.tzinfo is None:
        raise ValueError('postmaster start time must include timezone')
    return value[0], started


def context(commit, run_id, attempt):
    if not re.fullmatch(r'[a-f0-9]{40}', commit) or not run_id or not re.fullmatch(r'[1-9][0-9]*', attempt):
        raise ValueError('exact commit and workflow execution identity required')
    return {'commit': commit, 'run_id': run_id, 'attempt': attempt}


def inspect(group, directory, expected_context, plan=None):
    plan = load(gate.EXPECTED) if plan is None else plan
    if group not in GROUPS:
        raise ValueError('unknown result group')
    if any(not (directory / name).is_file() or (directory / name).is_symlink() for name in EVIDENCE):
        raise ValueError('missing group evidence file')
    before_id, before_start = cluster(directory / 'cluster-before.txt')
    after_id, after_start = cluster(directory / 'cluster-after.txt')
    if before_id != after_id or after_start <= before_start:
        raise ValueError('same disposable PostgreSQL cluster must actually restart')
    counts, windows = {}, {}
    for phase in ('before', 'after'):
        with (directory / (phase + '.jsonl')).open() as stream:
            events = [json.loads(line) for line in stream if line.strip()]
        counts[phase] = gate.check(phase, events, selected(gate.EXPECTED, phase, group, plan))
        stamps = []
        for event in events:
            value = event.get('Time')
            if not isinstance(value, str):
                raise ValueError('every raw Go event needs its recorded timestamp')
            stamp = datetime.fromisoformat(value.replace('Z', '+00:00'))
            if stamp.tzinfo is None:
                raise ValueError('Go event timestamp must include timezone')
            if phase == 'before' and not before_start <= stamp < after_start:
                raise ValueError('before events must precede the observed database restart')
            if phase == 'after' and stamp < after_start:
                raise ValueError('after events must follow the observed database restart')
            stamps.append(stamp)
        windows[phase] = {'first': min(stamps).isoformat(), 'last': max(stamps).isoformat()}
    smokes = (directory / 'process-smokes.txt').read_text().splitlines()
    if smokes != (list(SMOKES) if group == SMOKE_GROUP else []):
        raise ValueError('missing, duplicated, misplaced or out-of-order process smoke evidence')
    return {
        'version': 2, 'group': group, **expected_context,
        'plan_sha256': plan_sha256(), 'cluster_system_identifier': before_id,
        'postmaster_before': before_start.isoformat(), 'postmaster_after': after_start.isoformat(),
        'counts': counts, 'phase_windows': windows,
        'files': {name: hashlib.sha256((directory / name).read_bytes()).hexdigest() for name in EVIDENCE},
    }


def finalize(group, directory, expected_context):
    receipt = inspect(group, directory, expected_context)
    # Exclusive publication prevents stale evidence from an earlier attempt
    # from being silently replaced inside an allegedly fresh result directory.
    with (directory / 'receipt.json').open('x') as handle:
        json.dump(receipt, handle, indent=2)
        handle.write('\n')
    return receipt


def artifact_name(group, attempt):
    if group not in GROUPS or not re.fullmatch(r'[1-9][0-9]*', str(attempt)):
        raise ValueError('known group and positive attempt required for artifact identity')
    return 'postgres-' + group + '-attempt-' + str(attempt)


def compiled(lines):
    names = [line.strip() for line in lines if re.fullmatch(r'TestPG\w+', line.strip())]
    if len(names) != len(set(names)) or set(names) != gate.EXPECTED['before']:
        raise ValueError('compiled Go declarations differ from the exact assigned case set')
    return len(names)


def aggregate(root, expected_context):
    plan = load(gate.EXPECTED)
    expected_dirs = {artifact_name(group, expected_context['attempt']) for group in GROUPS}
    if {p.name for p in root.iterdir()} != expected_dirs:
        raise ValueError('both and only the two named result artifacts are required')
    receipts = []
    for group in GROUPS:
        directory = root / artifact_name(group, expected_context['attempt'])
        if directory.is_symlink() or {p.name for p in directory.iterdir()} != set(EVIDENCE) | {'receipt.json'}:
            raise ValueError('missing, duplicate or unexpected group result files')
        actual = inspect(group, directory, expected_context, plan)
        if (directory / 'receipt.json').is_symlink() or read_json(directory / 'receipt.json') != actual:
            raise ValueError('stale or inconsistent group receipt')
        receipts.append(actual)
    if len({v['cluster_system_identifier'] for v in receipts}) != len(GROUPS):
        raise ValueError('groups must use distinct disposable PostgreSQL clusters')
    totals = {phase: sum(v['counts'][phase] for v in receipts) for phase in ('before', 'after')}
    if any(totals[phase] != len(gate.EXPECTED[phase]) for phase in totals):
        raise ValueError('aggregate does not cover the complete required registry')
    return totals


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('operation', choices=('validate', 'compiled', 'finalize', 'aggregate'))
    parser.add_argument('--group', choices=GROUPS)
    parser.add_argument('--directory', type=Path)
    parser.add_argument('--commit', default=os.getenv('GITHUB_SHA', ''))
    parser.add_argument('--run-id', default=os.getenv('GITHUB_RUN_ID', ''))
    parser.add_argument('--attempt', default=os.getenv('GITHUB_RUN_ATTEMPT', ''))
    args = parser.parse_args()
    try:
        plan = load(gate.EXPECTED)
        if args.operation == 'compiled':
            print(f'Verified {compiled(sys.stdin)} exact compiled PostgreSQL case declarations; no database tests executed')
            return
        if args.operation == 'validate':
            for group in GROUPS:
                print(group, {phase: len(selected(gate.EXPECTED, phase, group, plan)) for phase in ('before', 'after')})
            return
        if args.directory is None:
            raise ValueError('evidence directory required')
        expected_context = context(args.commit, args.run_id, args.attempt)
        if args.operation == 'finalize':
            receipt = finalize(args.group, args.directory, expected_context)
            print('Verified complete group evidence:', receipt['group'], receipt['counts'])
        else:
            totals = aggregate(args.directory, expected_context)
            print(f"Verified complete PostgreSQL contract: {totals['before']} before / {totals['after']} after; two isolated restarted clusters and required process smokes; no skipped or missing case")
    except (ValueError, KeyError, TypeError, OSError) as error:
        sys.exit(str(error))


if __name__ == '__main__':
    main()
