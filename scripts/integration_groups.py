#!/usr/bin/env python3
"""Deterministic integration assignments; no database or network operations."""
import hashlib
import json
import math
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
PLAN_PATH = ROOT / 'scripts/integration-groups.json'
GROUPS = ('group-a', 'group-b')
SMOKE_GROUP = 'group-a'
SMOKE_DEPENDENCIES = {
    'TestPGDevelopDiscoveryRestartPersistence',
    'TestPGDevelopPackageRestartPersistence',
    'TestPGDevelopBotCommandsRestartPersistence',
    'TestPGNodeInfoRestartPersistence',
}
SMOKES = ('develop-before', 'catalog-before', 'develop-after', 'catalog-after', 'candidate-after')


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError('duplicate JSON key: ' + key)
        result[key] = value
    return result


def read_json(path):
    return json.loads(Path(path).read_text(), object_pairs_hook=unique_object)


def declarations():
    result = []
    for path in sorted((ROOT / 'tests/integration').glob('*_test.go')):
        result.extend(re.findall(r'^func (TestPG\w+)\(', path.read_text(), re.M))
    if len(result) != len(set(result)):
        raise ValueError('duplicate PostgreSQL declaration')
    return set(result)


def validate(plan, expected, declared=None):
    if plan.get('version') != 1 or set(plan.get('groups', {})) != set(GROUPS):
        raise ValueError('exactly two known groups required')
    if plan.get('process_smoke_group') != SMOKE_GROUP or set(plan.get('process_smoke_dependencies', [])) != SMOKE_DEPENDENCIES:
        raise ValueError('process smoke dependencies must retain their assigned cluster')
    if plan.get('review_required', []) != []:
        raise ValueError('case/dependency changes require explicit review before using this plan')
    combined = []
    for group in GROUPS:
        names = plan['groups'][group]
        if not isinstance(names, list) or not names or any(not isinstance(n, str) for n in names):
            raise ValueError('explicit nonempty named-case list required: ' + group)
        if names != sorted(names) or len(names) != len(set(names)):
            raise ValueError('unsorted or duplicate assignment: ' + group)
        combined.extend(names)
        if not set(names) & expected['after']:
            raise ValueError('each group must include real before/after restart cases')
    if len(combined) != len(set(combined)):
        raise ValueError('case assigned to multiple groups')
    if set(combined) != expected['before']:
        raise ValueError('missing, unknown or unassigned integration cases')
    if expected['after'] != {n for n in combined if n.endswith('RestartPersistence')}:
        raise ValueError('restart registry differs from paired case declarations')
    if declared is not None and set(combined) != declared:
        raise ValueError('assignment does not cover exact source declarations')
    if not SMOKE_DEPENDENCIES <= set(plan['groups'][SMOKE_GROUP]):
        raise ValueError('smoke fixture was split from its real HTTP consumer')
    durations = plan.get('estimated_seconds', {})
    if set(durations) != set(combined) or any(type(v) not in (int, float) or not math.isfinite(v) or v < 0 for v in durations.values()):
        raise ValueError('every named case requires a finite nonnegative estimate')
    return plan


def load(expected):
    return validate(read_json(PLAN_PATH), expected, declarations())


def selected(expected, phase, group='all', plan=None):
    if phase not in ('before', 'after'):
        raise ValueError('unknown phase')
    if group == 'all':
        return set(expected[phase])
    if group not in GROUPS:
        raise ValueError('unknown integration group')
    plan = load(expected) if plan is None else validate(plan, expected)
    # One assignment controls both phases. No separate after list can move or
    # omit a marker from the disposable cluster that created it.
    return set(plan['groups'][group]) & expected[phase]


def plan_sha256():
    return hashlib.sha256(PLAN_PATH.read_bytes()).hexdigest()
