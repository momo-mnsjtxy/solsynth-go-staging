#!/usr/bin/env python3
"""Test the result guard itself; synthetic events are never database evidence."""
import importlib.util
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("guard", ROOT / "scripts/check-integration-results.py")
guard = importlib.util.module_from_spec(spec)
spec.loader.exec_module(guard)


def event(name="", action="pass", package=guard.EXPECTED_PACKAGE):
    result = {"Package": package, "Action": action}
    if name:
        result["Test"] = name
    return result


def events_for(names):
    result = []
    for name in sorted(names):
        result.extend([event(name, "run"), event(name)])
    return result + [event()]


found = set()
for path in (ROOT / "tests/integration").glob("*_test.go"):
    found.update(re.findall(r"^func (TestPG\w+)\(", path.read_text(), re.M))
assert found == guard.EXPECTED["before"], (
    "required PG cases differ from source declarations; missing="
    + str(sorted(found - guard.EXPECTED["before"]))
    + " extra=" + str(sorted(guard.EXPECTED["before"] - found))
)
assert guard.EXPECTED["after"] == {
    name for name in found if name.endswith("RestartPersistence")
}, "every database-restart assertion must run in both phases"

rejections = 0
for phase, names in guard.EXPECTED.items():
    pattern = subprocess.check_output(
        [sys.executable, str(ROOT / "scripts/check-integration-results.py"), "--pattern", phase],
        text=True,
    ).strip()
    assert {name for name in found if re.fullmatch(pattern, name)} == names, (
        "phase selector disagrees with required passes"
    )
    valid = events_for(names)
    assert guard.check(phase, valid) == len(names)
    one = sorted(names)[0]
    # Package output and nested successful subtests are legitimate go test events.
    assert guard.check(phase, [event(action="start"), event(one + "/branch", "run"),
                               event(one + "/branch")] + valid) == len(names)
    invalid = [
        [],
        valid[2:],
        valid[:-1],
        valid + [event(one, "skip")],
        valid + [event(one + "/branch", "skip")],
        valid + [event(action="fail")],
        valid + [event(one, "fail")],
        valid + [event("TestPGUnexpected", "run")],
        valid + [event(one)],
        valid + [event(one, "run")],
        [event(name) for name in sorted(names)] + [event()],
        [event(name, action, "unrelated/package") for name in sorted(names)
         for action in ("run", "pass")] + [event()],
        valid + [None],
        valid + [{"Test": None, "Action": "pass"}],
        events_for(guard.EXPECTED["after" if phase == "before" else "before"]),
    ]
    for events in invalid:
        try:
            guard.check(phase, events)
        except ValueError:
            rejections += 1
        else:
            raise AssertionError("guard accepted missing/skipped/failed/foreign/wrong-phase events")
print(
    f"Verified PostgreSQL guard: {len(found)} before / {len(guard.EXPECTED['after'])} after, "
    f"exact declarations/selectors and {rejections} negative cases; no database tests executed"
)
