#!/usr/bin/env python3
"""Fail closed unless each required real PostgreSQL test passed, never skipped."""
import json
import sys

EXPECTED = {
    "before": {
        "TestPGOriginalSchemaAndIsolation",
        "TestPGIndependentServicePatchConcurrency",
        "TestPGSoftDeleteAndDuplicateLiveRows",
        "TestPGTransactionRollbackAndCancellation",
        "TestPGRestartPersistence",
        "TestPGIdentityAndTimestampPreservation",
        "TestPGWatcherRecipientsAndPreferences",
        "TestPGWatcherSoftDeleteAssumptions",
        "TestPGWatcherOneHopChains",
        "TestPGWatcherInputErrors",
    },
    "after": {"TestPGRestartPersistence"},
}


def check(phase, events):
    expected = EXPECTED[phase]
    passed = set()
    for event in events:
        name, action = event.get("Test", ""), event.get("Action")
        if action == "fail" or (name.startswith("TestPG") and action == "skip"):
            raise ValueError(f"integration failed or skipped: {name or event.get('Package')}")
        if action == "pass" and name:
            passed.add(name)
    missing = expected - passed
    if missing:
        raise ValueError("missing required passes: " + ", ".join(sorted(missing)))
    return len(expected)


if __name__ == "__main__":
    try:
        phase, path = sys.argv[1:]
        with open(path, encoding="utf-8") as handle:
            count = check(phase, [json.loads(line) for line in handle if line.strip()])
        print(f"Verified {count} required PostgreSQL case(s) passed in {phase} phase; none skipped")
    except (ValueError, KeyError, OSError) as error:
        sys.exit(str(error))
