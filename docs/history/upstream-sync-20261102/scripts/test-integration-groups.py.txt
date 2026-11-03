#!/usr/bin/env python3
"""Offline mutation tests. Synthetic JSONL is guard input, never PG evidence."""
import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile

from integration_groups import GROUPS, ROOT, SMOKE_DEPENDENCIES, SMOKES, declarations, load, read_json, selected, validate
from reviewed_sources import reviewed_bytes

spec = importlib.util.spec_from_file_location('groups_guard', ROOT / 'scripts/check-integration-groups.py')
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
gate = module.gate
plan = load(gate.EXPECTED)
rejections = 0


def rejects(call):
    global rejections
    try:
        call()
    except (ValueError, KeyError, TypeError, OSError):
        rejections += 1
    else:
        raise AssertionError('invalid group/evidence mutation was accepted')


for phase in ('before', 'after'):
    chosen = [selected(gate.EXPECTED, phase, group, plan) for group in GROUPS]
    assert chosen[0].isdisjoint(chosen[1])
    assert set.union(*chosen) == gate.EXPECTED[phase]
    for group, names in zip(GROUPS, chosen):
        output = subprocess.check_output([sys.executable, str(ROOT / 'scripts/check-integration-results.py'), '--pattern', phase, '--group', group], text=True).strip()
        assert {n for n in declarations() if re.fullmatch(output, n)} == names
for name in gate.EXPECTED['after']:
    assert [g for g in GROUPS if name in selected(gate.EXPECTED, 'before', g, plan)] == [g for g in GROUPS if name in selected(gate.EXPECTED, 'after', g, plan)]

# The dependency review inventories every current marker and pins its owning
# function. An edited restart fixture must be reviewed instead of silently
# carrying forward an obsolete cross-group assumption.
audit = read_json(ROOT / 'docs/ci-two-groups-dependencies.json')
reviewed_file_hashes = {entry['source']: entry['sha256'] for entry in audit['fixture_source_files']}
assert {entry['test'] for entry in audit['restart_cases']} == gate.EXPECTED['after']
assert len(audit['restart_cases']) == len(gate.EXPECTED['after'])
for entry in audit['restart_cases']:
    source = reviewed_bytes(ROOT, entry['source'], reviewed_file_hashes[entry['source']]).decode()
    functions = list(re.finditer(r'^func (\w+)\(', source, re.M))
    index = next(i for i, match in enumerate(functions) if match.group(1) == entry['test'])
    body = source[functions[index].start():functions[index+1].start() if index+1 < len(functions) else len(source)]
    assert hashlib.sha256(body.encode()).hexdigest() == entry['function_sha256'], entry['test'] + ' restart dependency review is stale'
    assert entry['test'] in plan['groups'][entry['group_before_and_after']]
for entry in audit['external_process_smokes']:
    assert hashlib.sha256((ROOT / entry['script']).read_bytes()).hexdigest() == entry['source_sha256'], entry['script'] + ' smoke dependency review is stale'
for entry in audit['composed_host_cases']:
    source = reviewed_bytes(ROOT, entry['source'], reviewed_file_hashes[entry['source']]).decode()
    functions = list(re.finditer(r'^func (\w+)\(', source, re.M))
    index = next(i for i, match in enumerate(functions) if match.group(1) == entry['test'])
    body = source[functions[index].start():functions[index+1].start() if index+1 < len(functions) else len(source)]
    assert hashlib.sha256(body.encode()).hexdigest() == entry['function_sha256'], entry['test'] + ' composed host review is stale'
    assert entry['test'] in plan['groups'][entry['group']]
for entry in audit['go_process_smokes']:
    assert entry['test'] in plan['groups'][entry['group']]
assert audit['authority_process_helper']['parent_test'] in plan['groups'][audit['authority_process_helper']['group']]
assert audit['base_commit'] == plan['base_commit']
assert {entry['source'] for entry in audit['fixture_source_files']} == {str(path.relative_to(ROOT)) for path in (ROOT / 'tests/integration').glob('*.go')}
for entry in audit['fixture_source_files']:
    assert hashlib.sha256(reviewed_bytes(ROOT, entry['source'], entry['sha256'])).hexdigest() == entry['sha256'], entry['source'] + ' fixture dependency review is stale'

assert module.compiled(sorted(gate.EXPECTED['before'])) == len(gate.EXPECTED['before'])
rejects(lambda: module.compiled(sorted(gate.EXPECTED['before'])[1:]))
rejects(lambda: module.compiled(sorted(gate.EXPECTED['before']) + [next(iter(gate.EXPECTED['before']))]))
rejects(lambda: module.compiled(sorted(gate.EXPECTED['before']) + ['TestPGInvented']))
# Confirm the documented longest-first schedule reproduces the frozen names.
rebuilt = {'group-a': sorted(SMOKE_DEPENDENCIES), 'group-b': []}
weights = plan['estimated_seconds']
loads = {g: sum(weights[n] for n in names) for g, names in rebuilt.items()}
for name in sorted(gate.EXPECTED['before'] - SMOKE_DEPENDENCIES, key=lambda n: (-weights[n], n)):
    group = min(GROUPS, key=lambda g: (round(loads[g], 8), g))
    rebuilt[group].append(name)
    loads[group] += weights[name]
assert {g: sorted(names) for g, names in rebuilt.items()} == plan['groups']

mutations = []
p = copy.deepcopy(plan); p['review_required'] = ['new case pending review']; mutations.append(p)
p = copy.deepcopy(plan); p['groups']['group-a'].pop(); mutations.append(p)
p = copy.deepcopy(plan); p['groups']['group-a'].append(p['groups']['group-a'][0]); p['groups']['group-a'].sort(); mutations.append(p)
p = copy.deepcopy(plan); p['groups']['group-b'].append(p['groups']['group-a'][0]); p['groups']['group-b'].sort(); mutations.append(p)
p = copy.deepcopy(plan); p['groups']['group-b'].append('TestPGInvented'); p['groups']['group-b'].sort(); mutations.append(p)
p = copy.deepcopy(plan); p['groups']['group-c'] = []; mutations.append(p)
p = copy.deepcopy(plan); p['process_smoke_group'] = 'group-b'; mutations.append(p)
p = copy.deepcopy(plan); name = sorted(SMOKE_DEPENDENCIES)[0]; p['groups']['group-a'].remove(name); p['groups']['group-b'].append(name); p['groups']['group-b'].sort(); mutations.append(p)
p = copy.deepcopy(plan); p['estimated_seconds'].pop(next(iter(p['estimated_seconds']))); mutations.append(p)
p = copy.deepcopy(plan); p['estimated_seconds'][next(iter(p['estimated_seconds']))] = float('nan'); mutations.append(p)
for p in mutations:
    rejects(lambda p=p: validate(p, gate.EXPECTED, declarations()))
rejects(lambda: selected(gate.EXPECTED, 'before', 'all-but-one'))
rejects(lambda: validate(plan, {**gate.EXPECTED, 'after': set()}, declarations()))
rejects(lambda: validate(plan, gate.EXPECTED, declarations() | {'TestPGNewUnassigned'}))


def events(names, phase):
    out = []
    for name in sorted(names):
        out.extend([{'Package': gate.EXPECTED_PACKAGE, 'Test': name, 'Action': 'run'}, {'Package': gate.EXPECTED_PACKAGE, 'Test': name, 'Action': 'pass'}])
    out.append({'Package': gate.EXPECTED_PACKAGE, 'Action': 'pass'})
    for event in out:
        event['Time'] = '2026-10-08T01:30:00Z' if phase == 'before' else '2026-10-08T02:30:00Z'
    return out


def seed(root):
    expected_context = module.context('a' * 40, 'offline-guard-only', '1')
    for index, group in enumerate(GROUPS):
        folder = root / module.artifact_name(group, expected_context['attempt'])
        folder.mkdir()
        for phase in ('before', 'after'):
            (folder / (phase + '.jsonl')).write_text(''.join(json.dumps(e) + '\n' for e in events(selected(gate.EXPECTED, phase, group, plan), phase)))
        (folder / 'cluster-before.txt').write_text(f'{100 + index}|2026-10-08 01:00:00+00\n')
        (folder / 'cluster-after.txt').write_text(f'{100 + index}|2026-10-08 02:00:00+00\n')
        (folder / 'process-smokes.txt').write_text(''.join(s + '\n' for s in SMOKES) if group == 'group-a' else '')
        module.finalize(group, folder, expected_context)
    return expected_context


with tempfile.TemporaryDirectory(prefix='offline-pg-group-guards-') as temp:
    temp = Path(temp)
    clean = temp / 'clean'; clean.mkdir()
    identity = seed(clean)
    assert module.aggregate(clean, identity) == {phase: len(names) for phase, names in gate.EXPECTED.items()}
    rejects(lambda: module.finalize('group-a', clean / 'postgres-group-a-attempt-1', identity))
    duplicate = temp / 'duplicate.json'; duplicate.write_text('{"groups":{},"groups":{}}')
    rejects(lambda: read_json(duplicate))
    mutations = [
        lambda r: shutil.rmtree(r / 'postgres-group-b-attempt-1'),
        lambda r: shutil.copytree(r / 'postgres-group-a-attempt-1', r / 'postgres-group-extra'),
        lambda r: (r / 'postgres-group-a-attempt-1' / 'receipt.json').unlink(),
        lambda r: (r / 'postgres-group-a-attempt-1' / 'after.jsonl').unlink(),
        lambda r: (r / 'postgres-group-a-attempt-1' / 'extra-file').write_text('unrelated'),
        lambda r: (r / 'postgres-group-a-attempt-1' / 'cluster-after.txt').write_text('999|2026-10-08 02:00:00+00\n'),
        lambda r: shutil.copyfile(r / 'postgres-group-a-attempt-1' / 'cluster-before.txt', r / 'postgres-group-a-attempt-1' / 'cluster-after.txt'),
        lambda r: (r / 'postgres-group-a-attempt-1' / 'cluster-after.txt').write_text('100|2026-10-08 00:00:00+00\n'),
        lambda r: (r / 'postgres-group-a-attempt-1' / 'cluster-after.txt').write_text('100|2026-10-08 02:00:00\n'),
        lambda r: (r / 'postgres-group-a-attempt-1' / 'process-smokes.txt').write_text(''),
        lambda r: (r / 'postgres-group-a-attempt-1' / 'process-smokes.txt').write_text('\n'.join(SMOKES + SMOKES) + '\n'),
        lambda r: (r / 'postgres-group-b-attempt-1' / 'process-smokes.txt').write_text('candidate-after\n'),
    ]
    def change_receipt(root, key, value):
        path = root / 'postgres-group-a-attempt-1' / 'receipt.json'
        data = read_json(path); data[key] = value; path.write_text(json.dumps(data))
    mutations.extend([lambda r: change_receipt(r, 'commit', 'b' * 40), lambda r: change_receipt(r, 'attempt', '2'), lambda r: change_receipt(r, 'plan_sha256', '0' * 64), lambda r: change_receipt(r, 'group', 'group-b')])
    def replace_events(root, mutate):
        path = root / 'postgres-group-a-attempt-1' / 'before.jsonl'
        data = [json.loads(line) for line in path.read_text().splitlines()]
        mutate(data)
        path.write_text(''.join(json.dumps(e) + '\n' for e in data))
    mutations.extend([
        lambda r: replace_events(r, lambda e: e.pop(1)),
        lambda r: replace_events(r, lambda e: e[1].update(Action='skip')),
        lambda r: replace_events(r, lambda e: e[-1].update(Action='fail')),
        lambda r: replace_events(r, lambda e: e.append(e[1])),
        lambda r: replace_events(r, lambda e: e[0].update(Test='TestPGInvented')),
        lambda r: replace_events(r, lambda e: e[0].update(Package='unrelated/package')),
        lambda r: replace_events(r, lambda e: e.pop()),
        lambda r: replace_events(r, lambda e: e.append({'Package': gate.EXPECTED_PACKAGE, 'Test': 'TestAuthorityProcessHelper', 'Action': 'skip', 'Time': '2026-10-08T01:30:00Z'})),
        lambda r: replace_events(r, lambda e: e[0].pop('Time')),
        lambda r: replace_events(r, lambda e: e[0].update(Time='2026-10-08T02:30:00Z')),
        lambda r: replace_events(r, lambda e: e[0].update(Time='2026-10-08T01:30:00')),
        lambda r: shutil.copyfile(r / 'postgres-group-b-attempt-1' / 'after.jsonl', r / 'postgres-group-a-attempt-1' / 'after.jsonl'),
    ])
    # Recompute the receipt too: aggregate must still reject reused clusters,
    # not merely rely on a hash difference in a supplied summary.
    def same_cluster(root):
        folder = root / 'postgres-group-b-attempt-1'
        for phase in ('before', 'after'):
            shutil.copyfile(root / 'postgres-group-a-attempt-1' / ('cluster-' + phase + '.txt'), folder / ('cluster-' + phase + '.txt'))
        (folder / 'receipt.json').unlink()
        module.finalize('group-b', folder, identity)
    mutations.append(same_cluster)
    for i, mutation in enumerate(mutations):
        changed = temp / str(i); shutil.copytree(clean, changed)
        mutation(changed)
        rejects(lambda changed=changed: module.aggregate(changed, identity))
    rejects(lambda: module.aggregate(clean, {**identity, 'run_id': 'wrong-run'}))
    rejects(lambda: module.aggregate(clean, {**identity, 'attempt': '2'}))
    stale_after = temp / 'stale-after'; shutil.copytree(clean, stale_after)
    path = stale_after / module.artifact_name('group-a', '1') / 'after.jsonl'
    path.write_text(path.read_text().replace('02:30:00Z', '01:30:00Z'))
    rejects(lambda: module.aggregate(stale_after, identity))

def check_group_wiring(runner, workflow):
    import yaml
    before = runner.index('PG_TEST_PHASE=before ')
    after = runner.index('PG_TEST_PHASE=after ')
    proof = "SELECT system_identifier,pg_postmaster_start_time() FROM pg_control_system()"
    for phase, position in [('before', before), ('after', after)]:
        command = '\"$PG_BIN/psql\" \"$PG_TEST_DSN\" -A -t -F \'|\' -v ON_ERROR_STOP=1 -c \'' + proof + "' > \"$run/cluster-" + phase + '.txt\"'
        if command not in runner or runner.index(command) >= position:
            raise ValueError('cluster proof must come from actual PostgreSQL before its phase')
        if phase == 'after' and runner.index(command) <= before:
            raise ValueError('after proof was captured before restart')
    blocks = re.findall(r'if \[\[ "\$group" == all \|\| "\$group" == group-a \]\]; then\n(.*?)\nfi', runner, re.S)
    if len(blocks) != 2:
        raise ValueError('only the seed group may run external process smokes')
    expected_scripts = [('verify-develop-http.py', 'develop-before'), ('verify-catalog-http.py', 'catalog-before')]
    for block, pairs in zip(blocks, [expected_scripts, [('verify-develop-http.py', 'develop-after'), ('verify-catalog-http.py', 'catalog-after'), ('verify-candidate-http.py', 'candidate-after')]]):
        previous = -1
        for script, smoke in pairs:
            command = 'python3 scripts/' + script
            marker = "printf '" + smoke + "\\n' >> \"$run/process-smokes.txt\""
            if command not in block or marker not in block or not previous < block.index(command) < block.index(marker):
                raise ValueError('successful real process call must precede its smoke receipt')
            previous = block.index(marker)
    if runner.index('scripts/check-integration-groups.py finalize') <= runner.index("printf 'candidate-after"):
        raise ValueError('group receipt must follow every required phase and smoke')
    jobs = yaml.load(workflow, Loader=yaml.BaseLoader)['jobs']
    if set(jobs) != {'checks', 'postgres', 'contracts'}:
        raise ValueError('exact ordinary, matrix and aggregate job graph required')
    checks, postgres, aggregate = jobs['checks'], jobs['postgres'], jobs['contracts']
    if checks.get('timeout-minutes') != '50' or postgres.get('timeout-minutes') != '50' or aggregate.get('timeout-minutes') != '5':
        raise ValueError('reviewed workflow budgets must remain unchanged')
    ordinary = [step for step in checks['steps'] if step.get('name') == 'Ordinary build, vet and race checks']
    if len(ordinary) != 1 or ordinary[0].get('run') != 'bash scripts/verify.sh':
        raise ValueError('ordinary full verification must execute exactly once')
    if any('if' in step or 'continue-on-error' in step for step in checks['steps']):
        raise ValueError('ordinary verification cannot be conditional or ignored')
    if any('if' in jobs[job] or 'continue-on-error' in jobs[job] for job in ['checks','postgres']):
        raise ValueError('ordinary or matrix jobs cannot be conditional or ignored')
    upload = [step for step in postgres['steps'] if str(step.get('uses','')).startswith('actions/upload-artifact@')]
    download = [step for step in aggregate['steps'] if str(step.get('uses','')).startswith('actions/download-artifact@')]
    if len(upload)!=1 or upload[0].get('if')!='always()' or upload[0]['with'].get('name')!='postgres-${{ matrix.group }}-attempt-${{ github.run_attempt }}' or upload[0]['with'].get('if-no-files-found')!='error':
        raise ValueError('each attempt requires distinct, mandatory group evidence')
    if len(download)!=1 or download[0]['with'].get('pattern')!='postgres-group-*-attempt-${{ github.run_attempt }}' or download[0]['with'].get('merge-multiple')!='false':
        raise ValueError('aggregate must read only this attempt and keep groups separate')
    if postgres['needs'] != 'checks' or postgres['strategy'] != {'fail-fast': 'false', 'matrix': {'group': list(GROUPS)}}:
        raise ValueError('both complete independent groups must run after ordinary checks')
    if aggregate['if'] != 'always()' or set(aggregate['needs']) != {'checks', 'postgres'}:
        raise ValueError('aggregate must run even when dependencies fail or skip')
    steps = aggregate['steps']
    if 'test "$ORDINARY_RESULT" = success' not in steps[0]['run'] or 'test "$POSTGRES_RESULT" = success' not in steps[0]['run']:
        raise ValueError('aggregate must reject any failed or skipped upstream job')
    final = steps[-1]['run']
    if not final.startswith('set -euo pipefail\n') or 'scripts/check-integration-groups.py aggregate' not in final:
        raise ValueError('aggregate evidence guard must fail closed through tee')
    if any('continue-on-error' in step for step in postgres['steps'] + steps):
        raise ValueError('integration failures cannot be ignored')


def check_ordinary_verification(source):
    if not source.startswith('#!/usr/bin/env bash\nset -euo pipefail\n'):
        raise ValueError('ordinary verifier must fail closed')
    required = [
        'python3 scripts/check-workflow-yaml.py',
        'python3 scripts/check-source-evidence.py',
        'python3 scripts/test-source-evidence.py',
        'python3 scripts/test-integration-guard.py',
        'python3 scripts/test-integration-groups.py',
        'python3 scripts/test-integration-wiring.py',
        'python3 scripts/check-postgres-syntax.py',
        'python3 scripts/verify-coverage-plan.py',
        'python3 scripts/test-coverage-format.py',
        '"$GO_BIN" run ./scripts/sqlargs . | python3 scripts/check-sql-argument-count.py',
        '"$GO_BIN" test ./tests/integration -list \'^TestPG\' | python3 scripts/check-integration-groups.py compiled',
        '"$GO_BIN" build ./...', '"$GO_BIN" vet ./...',
        '"$GO_BIN" test -count=1 ./...', '"$GO_BIN" test -race -count=1 ./...',
    ]
    lines = source.splitlines()
    positions = []
    for command in required:
        if lines.count(command) != 1:
            raise ValueError('missing, conditional or ignored mandatory verification: ' + command)
        positions.append(lines.index(command))
    if positions != sorted(positions):
        raise ValueError('mandatory verification stage order changed')

ordinary_source = (ROOT / 'scripts/verify.sh').read_text()
check_ordinary_verification(ordinary_source)
for original in ['"$GO_BIN" run ./scripts/sqlargs . | python3 scripts/check-sql-argument-count.py', '"$GO_BIN" test -race -count=1 ./...', '"$GO_BIN" vet ./...']:
    rejects(lambda original=original: check_ordinary_verification(ordinary_source.replace(original, 'echo omitted')))
rejects(lambda: check_ordinary_verification(ordinary_source.replace('set -euo pipefail', 'set -eu')))

runner = (ROOT / 'scripts/integration-postgres.sh').read_text()
workflow = (ROOT / '.github/workflows/postgres-validation.yml').read_text()
check_group_wiring(runner, workflow)
for damaged in [
    runner.replace('group-a ]]; then', 'group-b ]]; then'),
    runner.replace('SELECT system_identifier,pg_postmaster_start_time()', 'SELECT 1,now()'),
    runner.replace("printf 'develop-before", "printf 'develop-missing"),
    runner.replace('python3 scripts/verify-candidate-http.py', 'echo scripts/verify-candidate-http.py'),
]:
    rejects(lambda damaged=damaged: check_group_wiring(damaged, workflow))
for damaged in [
    workflow.replace('fail-fast: false', 'fail-fast: true'),
    workflow.replace('group: [group-a, group-b]', 'group: [group-a]'),
    workflow.replace('needs: [checks, postgres]', 'needs: [checks]'),
    workflow.replace('    if: always()', '    if: success()'),
    workflow.replace('test "$POSTGRES_RESULT" = success', 'test "$POSTGRES_RESULT" != unknown'),
    workflow.replace('          set -euo pipefail', '          set -eu'),
    workflow.replace('-attempt-${{ github.run_attempt }}', ''),
    workflow.replace('run: bash scripts/verify.sh', 'run: echo skipped'),
    workflow.replace('timeout-minutes: 50', 'timeout-minutes: 25'),
    workflow.replace('merge-multiple: false', 'merge-multiple: true'),
]:
    rejects(lambda damaged=damaged: check_group_wiring(runner, damaged))

# A green first job cannot hide a still-running/failed second cluster. Exercise
# all paginated jobs, exact attempt identity and required steps independently.
spec = importlib.util.spec_from_file_location('ci_jobs', ROOT / 'scripts/check-integration-ci-jobs.py')
ci = importlib.util.module_from_spec(spec); spec.loader.exec_module(ci)
run = {'id': 17, 'run_attempt': 2, 'head_sha': 'a' * 40, 'status': 'completed', 'conclusion': 'success'}
jobs = [{'id': i + 100, 'name': name, 'run_id': 17, 'run_attempt': 2, 'head_sha': 'a' * 40, 'status': 'completed', 'conclusion': 'success', 'steps': [{'name': step, 'status': 'completed', 'conclusion': 'success'} for step in names]} for i, (name, names) in enumerate(ci.EXPECTED_STEPS.items())]
pages = [{'total_count': 4, 'jobs': jobs[:2]}, {'total_count': 4, 'jobs': jobs[2:]}]
assert len(ci.check(run, pages, 'a' * 40, 17, 2)) == 4
rejects(lambda: ci.check(run, pages[:1], 'a' * 40, 17, 2))
rejects(lambda: ci.check(run, pages + pages, 'a' * 40, 17, 2))
rejects(lambda: ci.check(run, pages, 'a' * 40, 17, 1))
for group_name in ('PostgreSQL group-a', 'PostgreSQL group-b', 'contracts'):
    for change in ({'status': 'in_progress', 'conclusion': None}, {'conclusion': 'failure'}, {'conclusion': 'skipped'}, {'head_sha': 'b' * 40}, {'steps': []}):
        changed = copy.deepcopy(pages)
        target = next(job for page in changed for job in page['jobs'] if job['name'] == group_name)
        target.update(change)
        rejects(lambda changed=changed: ci.check(run, changed, 'a' * 40, 17, 2))

# Refresh tooling produces deterministic drafts for later exact suites, while
# new cases and changed helper/fixture sources cannot silently activate them.
spec = importlib.util.spec_from_file_location('refresh_groups', ROOT / 'scripts/refresh-integration-groups.py')
refresh_tool = importlib.util.module_from_spec(spec); spec.loader.exec_module(refresh_tool)
functions, fixture_files = refresh_tool.inventory()
args = (plan, audit, gate.EXPECTED, functions, fixture_files, plan['base_commit'], sorted(gate.EXPECTED['before']))
first = refresh_tool.refresh(*args)
second = refresh_tool.refresh(*args)
assert first == second
# The old catalogue remains historical. Refresh must still flag each changed
# dependency, even when the separate local-mode review explicitly accepts it.
local_review = read_json(ROOT / 'docs/local-disposable-pg-review.json')
reviewed_fixture_changes = {'fixture-source:' + entry['source'] for entry in local_review['reviewed_changes']
                           if entry['source'].startswith('tests/integration/')}
reviewed_function_changes = {'dependency:' + entry['test'] for section in ('restart_cases', 'composed_host_cases')
                            for entry in audit[section]
                            if functions[entry['test']]['function_sha256'] != entry['function_sha256']}
assert set(first[0]['review_required']) == reviewed_fixture_changes | reviewed_function_changes
assert first[0]['groups'] == plan['groups']
future_expected = {phase: set(names) for phase, names in gate.EXPECTED.items()}
future_name = 'TestPGFutureSyntheticRestartPersistence'
future_expected['before'].add(future_name); future_expected['after'].add(future_name)
future_functions = copy.deepcopy(functions)
future_functions[future_name] = {'source': 'tests/integration/future_test.go', 'body': 'synthetic offline guard fixture', 'function_sha256': 'f' * 64}
future_files = {**fixture_files, 'tests/integration/future_test.go': 'e' * 64}
future_plan, future_audit, future_report = refresh_tool.refresh(plan, audit, future_expected, future_functions, future_files, 'b' * 40, sorted(future_expected['before']))
assert future_report['added_cases'] == [future_name] and future_plan['review_required']
rejects(lambda: validate(future_plan, future_expected))
reviewed = copy.deepcopy(future_plan); reviewed['review_required'] = []
validate(reviewed, future_expected, set(future_expected['before']))
assert any(entry['test'] == future_name for entry in future_audit['restart_cases'])
changed_files = dict(fixture_files); changed_files[next(iter(changed_files))] = '0' * 64
assert refresh_tool.refresh(plan, audit, gate.EXPECTED, functions, changed_files, plan['base_commit'], sorted(gate.EXPECTED['before']))[0]['review_required']
rejects(lambda: refresh_tool.refresh(plan, audit, gate.EXPECTED, functions, fixture_files, plan['base_commit'], sorted(gate.EXPECTED['before'])[1:]))
removed = next(iter(gate.EXPECTED['before'] - gate.EXPECTED['after']))
smaller_expected = {phase: set(names) - {removed} for phase, names in gate.EXPECTED.items()}
smaller_functions = {name: value for name, value in functions.items() if name != removed}
rejects(lambda: refresh_tool.refresh(plan, audit, smaller_expected, smaller_functions, fixture_files, plan['base_commit'], sorted(smaller_expected['before'])))

# The authoritative runner checks also keep all schema installs, binary builds,
# -race, count, deadlines and actual stop/start checks intact.
subprocess.run([sys.executable, str(ROOT / 'scripts/test-integration-wiring.py')], check=True)
print(f'Verified explicit two-group coverage, same-group restart selectors and {rejections} assignment/evidence rejections; synthetic guard events only, no PostgreSQL executed')
