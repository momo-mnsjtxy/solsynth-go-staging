#!/usr/bin/env python3
"""Write a deterministic, unactivated grouping draft for the current exact suite.

Never edits the working plan, test bodies or workflows. New/changed dependencies
remain review-required; clearing that list belongs to the independent reviewer.
"""
import argparse
import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import re
import subprocess
import sys

from integration_groups import GROUPS, ROOT, SMOKE_DEPENDENCIES, read_json

spec = importlib.util.spec_from_file_location('current_gate', ROOT / 'scripts/check-integration-results.py')
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)


def inventory():
    functions, files = {}, {}
    for path in sorted((ROOT / 'tests/integration').glob('*.go')):
        source = path.read_text()
        relative = str(path.relative_to(ROOT))
        files[relative] = hashlib.sha256(path.read_bytes()).hexdigest()
        matches = list(re.finditer(r'^func (\w+)\(', source, re.M))
        for index, match in enumerate(matches):
            name = match.group(1)
            body = source[match.start():matches[index+1].start() if index+1 < len(matches) else len(source)]
            if name in functions:
                raise ValueError('duplicate named Go function: ' + name)
            functions[name] = {'source': relative, 'body': body,
                               'function_sha256': hashlib.sha256(body.encode()).hexdigest()}
    return functions, files


def refresh(old_plan, old_audit, expected, functions, files, commit, compiled_names):
    declared = {name for name in functions if name.startswith('TestPG')}
    if declared != expected['before'] or set(compiled_names) != declared or len(compiled_names) != len(declared):
        raise ValueError('registry, source and compiled declarations must match exactly')
    if expected['after'] != {name for name in declared if name.endswith('RestartPersistence')}:
        raise ValueError('every current restart declaration needs both phases')
    if not re.fullmatch(r'[0-9a-f]{40}', commit):
        raise ValueError('exact current commit required')
    if old_plan.get('version') != 1 or set(old_plan.get('groups', {})) != set(GROUPS):
        raise ValueError('unsupported starting assignment')
    old_names = set().union(*(set(old_plan['groups'][group]) for group in GROUPS))
    removed = old_names - declared
    if removed:
        raise ValueError('case removal/rename needs manual review; not silently dropped: ' + ', '.join(sorted(removed)))
    if not SMOKE_DEPENDENCIES <= declared:
        raise ValueError('external process smoke seeds are missing')
    plan, audit = copy.deepcopy(old_plan), copy.deepcopy(old_audit)
    plan['base_commit'] = audit['base_commit'] = commit
    pending = set(old_plan.get('review_required', []))
    added = declared - old_names
    pending.update('case:' + name for name in added)
    weights = {name: old_plan['estimated_seconds'].get(name, old_plan['timing_source']['fallback_seconds'])
               for name in sorted(declared)}
    plan['estimated_seconds'] = weights
    plan['timing_source']['missing_cases'] = sorted(set(old_plan['timing_source']['missing_cases']) | added)
    groups = {'group-a': sorted(SMOKE_DEPENDENCIES), 'group-b': []}
    loads = {group: sum(weights[name] for name in names) for group, names in groups.items()}
    for name in sorted(declared - SMOKE_DEPENDENCIES, key=lambda name: (-weights[name], name)):
        group = min(GROUPS, key=lambda group: (round(loads[group], 8), group))
        groups[group].append(name)
        loads[group] += weights[name]
    plan['groups'] = {group: sorted(names) for group, names in groups.items()}
    assignment = {name: group for group, names in groups.items() for name in names}

    def case_entry(name, old, group_field):
        current = functions[name]
        entry = copy.deepcopy(old) if old else {'test': name, 'source': current['source']}
        if old is None or old['function_sha256'] != current['function_sha256']:
            pending.add('dependency:' + name)
        entry.update(source=current['source'], function_sha256=current['function_sha256'])
        entry[group_field] = assignment[name]
        return entry

    previous = {entry['test']: entry for entry in old_audit['restart_cases']}
    restarts = []
    for name in sorted(expected['after']):
        entry = case_entry(name, previous.get(name), 'group_before_and_after')
        if name not in previous:
            entry.update(marker_files=[], persistent_directories_or_files=[], named_database_markers={},
                         storage_contract='DRAFT: inspect the complete before/after body and its helpers; record actual saved markers and state before approving.')
        restarts.append(entry)
    audit['restart_cases'] = restarts
    for entry in audit['go_process_smokes']:
        entry['group'] = assignment[entry['test']]
    audit['authority_process_helper']['group'] = assignment[audit['authority_process_helper']['parent_test']]
    old_composed = {entry['test']: entry for entry in old_audit.get('composed_host_cases', [])}
    composed = set(old_composed) | {name for name in added if any(word in name for word in ('Backend', 'Composed', 'Composition')) or 'application.NewPostgres(' in functions[name]['body']}
    audit['composed_host_cases'] = [case_entry(name, old_composed.get(name), 'group') for name in sorted(composed)]
    old_files = {entry['source']: entry['sha256'] for entry in old_audit.get('fixture_source_files', [])}
    pending.update('fixture-source:' + name for name in set(files) | set(old_files) if files.get(name) != old_files.get(name))
    audit['fixture_source_files'] = [{'source': name, 'sha256': digest} for name, digest in sorted(files.items())]
    for entry in audit['external_process_smokes']:
        digest = hashlib.sha256((ROOT / entry['script']).read_bytes()).hexdigest()
        if digest != entry['source_sha256']:
            pending.add('process-smoke:' + entry['script'])
            entry['source_sha256'] = digest
    plan['review_required'] = sorted(pending)
    report = {'status': 'draft only; never activated', 'base_commit': commit,
              'added_cases': sorted(added), 'removed_cases': [], 'review_required': sorted(pending),
              'totals': {group: {'before': len(names), 'after': len(set(names) & expected['after']),
                                  'estimated_before_seconds': round(loads[group], 2)} for group, names in groups.items()},
              'next_step': 'Review every listed dependency and new case, complete new marker inventories, then clear review_required in the draft and run the complete offline guards before proposing adoption.'}
    return plan, audit, report


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--compiled-list', type=Path, required=True)
    parser.add_argument('--output-directory', type=Path, required=True)
    args = parser.parse_args()
    try:
        names = [line.strip() for line in args.compiled_list.read_text().splitlines() if re.fullmatch(r'TestPG\w+', line.strip())]
        commit = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
        functions, files = inventory()
        plan, audit, report = refresh(read_json(ROOT / 'scripts/integration-groups.json'),
                                      read_json(ROOT / 'docs/ci-two-groups-dependencies.json'),
                                      gate.EXPECTED, functions, files, commit, names)
        args.output_directory.mkdir(parents=True, exist_ok=False)
        for name, value in [('integration-groups.json', plan), ('ci-two-groups-dependencies.json', audit), ('review.json', report)]:
            with (args.output_directory / name).open('x') as handle:
                json.dump(value, handle, indent=2)
                handle.write('\n')
        print(json.dumps(report, indent=2))
    except (OSError, ValueError, KeyError, TypeError, subprocess.CalledProcessError) as error:
        sys.exit(str(error))


if __name__ == '__main__':
    main()
