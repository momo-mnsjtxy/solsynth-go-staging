#!/usr/bin/env python3
"""Require every selected backend process E2E case; never accept skips as passes.

Usage: python3 scripts/check-backend-e2e-results.py before results.jsonl
This bounded suite is separate from the full PostgreSQL two-group catalogue.
"""
from datetime import datetime
import hashlib
import json
import re
from pathlib import Path
import sys

EXPECTED = {
    'before': {'TestPGBackendHTTPProcessRestart', 'TestPGBackendSigninHTTPProcessRestart',
               'TestPGBackendRestartPersistence'},
    'after': {'TestPGBackendRestartPersistence'},
}


def check(phase, path):
    expected = EXPECTED[phase]
    started, passed = set(), set()
    package_passed = False
    for line in Path(path).read_text().splitlines():
        event = json.loads(line)
        action, name = event.get('Action'), event.get('Test')
        if action in ('skip', 'fail'):
            raise ValueError('E2E case/package skipped or failed: ' + (name or 'package'))
        if not name:
            package_passed |= action == 'pass'
            continue
        if '/' in name:
            continue
        if action == 'run':
            if name not in expected or name in started:
                raise ValueError('unexpected or duplicated E2E case: ' + name)
            started.add(name)
        if action == 'pass':
            if name not in started or name in passed:
                raise ValueError('unmatched or duplicated E2E pass: ' + name)
            passed.add(name)
    if started != expected or passed != expected or not package_passed:
        raise ValueError('missing E2E case or package result')
    print(f'Backend E2E {phase}: {len(passed)} required cases passed without skips')


def finalize(directory):
    directory = Path(directory)
    names = ['before.jsonl', 'after.jsonl', 'cluster-before.txt', 'cluster-after.txt', 'fixture-template.json']
    if any(not (directory / name).is_file() or (directory / name).is_symlink() for name in names):
        raise ValueError('missing or redirected backend E2E evidence')
    cluster = []
    for name in ('cluster-before.txt', 'cluster-after.txt'):
        parts = (directory / name).read_text().strip().split('|')
        if len(parts) != 2 or not re.fullmatch(r'[0-9]+', parts[0]):
            raise ValueError('invalid backend E2E cluster evidence')
        stamp = datetime.fromisoformat(parts[1])
        if stamp.tzinfo is None:
            raise ValueError('cluster start time requires a timezone')
        cluster.append((parts[0], stamp))
    if cluster[0][0] != cluster[1][0] or cluster[1][1] <= cluster[0][1]:
        raise ValueError('same backend E2E cluster must actually restart')
    for phase in ('before', 'after'):
        check(phase, directory / (phase + '.jsonl'))
        for line in (directory / (phase + '.jsonl')).read_text().splitlines():
            event = json.loads(line)
            stamp = datetime.fromisoformat(event['Time'].replace('Z', '+00:00'))
            if stamp.tzinfo is None or (phase == 'before' and not cluster[0][1] <= stamp < cluster[1][1]) or (phase == 'after' and stamp < cluster[1][1]):
                raise ValueError('backend E2E event is outside its database restart phase')
    template = json.loads((directory / 'fixture-template.json').read_text())
    if template['cluster'] != cluster[0][0] or not re.fullmatch(r'[a-f0-9]{40}', template['candidate']):
        raise ValueError('backend E2E template and cluster identity differ')
    receipt = {'version': 1, 'suite': 'backend-e2e', 'full_postgresql_catalogue': False,
               'candidate': template['candidate'], 'schema_sha256': template['schema_sha256'],
               'cluster_system_identifier': cluster[0][0], 'postmaster_before': cluster[0][1].isoformat(),
               'postmaster_after': cluster[1][1].isoformat(),
               'counts': {phase: len(cases) for phase, cases in EXPECTED.items()},
               'files': {name: hashlib.sha256((directory / name).read_bytes()).hexdigest() for name in names}}
    with (directory / 'receipt.json').open('x') as handle:
        json.dump(receipt, handle, indent=2)
        handle.write('\n')
    print('Verified backend E2E: 3 before / 1 after; same real PostgreSQL cluster restarted')
    return receipt


if __name__ == '__main__':
    try:
        if len(sys.argv) != 3 or sys.argv[1] not in (*EXPECTED, 'finalize'):
            raise ValueError('usage: check-backend-e2e-results.py before|after results.jsonl OR finalize directory')
        if sys.argv[1] == 'finalize':
            finalize(sys.argv[2])
        else:
            check(sys.argv[1], sys.argv[2])
    except (KeyError, OSError, ValueError) as error:
        sys.exit(str(error))
