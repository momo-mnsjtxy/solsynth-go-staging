#!/usr/bin/env python3
"""Test the result guard itself; synthetic events are never database evidence."""
import importlib.util
import re
from pathlib import Path

root=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('guard', root/'scripts/check-integration-results.py')
guard=importlib.util.module_from_spec(spec)
spec.loader.exec_module(guard)
found=set()
for path in (root/'tests/integration').glob('*_test.go'):
    found.update(re.findall(r'^func (TestPG\w+)\(',path.read_text(),re.M))
assert found==guard.EXPECTED['before'], 'required PG cases differ from compiled source declarations'
for phase,names in guard.EXPECTED.items():
    valid=[{'Test':name,'Action':'pass'} for name in sorted(names)]
    assert guard.check(phase,valid)==len(names)
    invalid=[[],valid[:-1],valid+[{'Test':sorted(names)[0],'Action':'skip'}],valid+[{'Action':'fail','Package':'synthetic'}]]
    for events in invalid:
        try:
            guard.check(phase,events)
        except ValueError:
            pass
        else:
            raise AssertionError('guard accepted missing/skipped/failed events')
print('Verified PostgreSQL guard declarations, missing/skip/failure rejection; no database tests executed')
