#!/usr/bin/env python3
"""Offline negative controls for review history and bounded E2E result parsing."""
import contextlib
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest

from reviewed_sources import reviewed_bytes

spec = importlib.util.spec_from_file_location('backend_e2e_results', Path(__file__).with_name('check-backend-e2e-results.py'))
results = importlib.util.module_from_spec(spec)
spec.loader.exec_module(results)


class ReviewTests(unittest.TestCase):
    def test_historical_and_current_hashes_are_both_required(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = 'scripts/example.py'
            old, new = b'original reviewed source\n', b'explicit reviewed local mode\n'
            oldhash, newhash = hashlib.sha256(old).hexdigest(), hashlib.sha256(new).hexdigest()
            (root / 'scripts').mkdir()
            (root / source).write_bytes(old)
            self.assertEqual(reviewed_bytes(root, source, oldhash), old)
            (root / source).write_bytes(new)
            with self.assertRaises(OSError):
                reviewed_bytes(root, source, oldhash)
            snapshot = 'docs/history/local-pg-baseline/scripts/example.py'
            (root / snapshot).parent.mkdir(parents=True)
            (root / snapshot).write_bytes(old)
            record = {'source': source, 'before_sha256': oldhash, 'after_sha256': newhash, 'retained_before': snapshot}
            manifest = root / 'docs/local-disposable-pg-review.json'
            manifest.write_text(json.dumps({'reviewed_changes': [record]}))
            self.assertEqual(reviewed_bytes(root, source, oldhash), old)
            for path, content in [(source, new + b'changed'), (snapshot, old + b'changed')]:
                original = (root / path).read_bytes()
                (root / path).write_bytes(content)
                with self.assertRaises(ValueError):
                    reviewed_bytes(root, source, oldhash)
                (root / path).write_bytes(original)
            for changes in [[], [record, record], [{**record, 'retained_before': '../outside'}]]:
                manifest.write_text(json.dumps({'reviewed_changes': changes}))
                with self.assertRaises(ValueError):
                    reviewed_bytes(root, source, oldhash)


class ResultTests(unittest.TestCase):
    def test_actual_restart_identity_and_event_windows_required(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for phase in ('before', 'after'):
                events = [{'Time': '2026-10-18T01:00:00Z' if phase == 'before' else '2026-10-18T03:00:00Z', 'Action': action, 'Test': name}
                          for name in sorted(results.EXPECTED[phase]) for action in ('run', 'pass')]
                events.append({'Time': events[-1]['Time'], 'Action': 'pass'})
                (root / (phase + '.jsonl')).write_text(''.join(json.dumps(event) + '\n' for event in events))
            (root / 'cluster-before.txt').write_text('12345|2026-10-18 00:00:00+00\n')
            (root / 'cluster-after.txt').write_text('12345|2026-10-18 02:00:00+00\n')
            (root / 'fixture-template.json').write_text(json.dumps({'cluster':'12345', 'candidate':'a'*40, 'schema_sha256':'b'*64}))
            for invalid in ['other|2026-10-18 02:00:00+00\n', '12345|2026-10-18 00:00:00+00\n', '12345|2026-10-18 04:00:00+00\n']:
                (root / 'cluster-after.txt').write_text(invalid)
                with contextlib.redirect_stdout(io.StringIO()), self.assertRaises(ValueError):
                    results.finalize(root)
            (root / 'cluster-after.txt').write_text('12345|2026-10-18 02:00:00+00\n')
            with contextlib.redirect_stdout(io.StringIO()):
                receipt = results.finalize(root)
            self.assertFalse(receipt['full_postgresql_catalogue'])
            with contextlib.redirect_stdout(io.StringIO()), self.assertRaises(FileExistsError):
                results.finalize(root)

    def check(self, events):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'events.jsonl'
            path.write_text(''.join(json.dumps(event) + '\n' for event in events))
            with contextlib.redirect_stdout(io.StringIO()):
                results.check('before', path)

    def test_exact_cases_and_package_pass_required(self):
        names = sorted(results.EXPECTED['before'])
        good = [{'Action': action, 'Test': name} for name in names for action in ('run', 'pass')] + [{'Action': 'pass'}]
        self.check(good)
        negatives = [[], good[:-1], good[2:], good + [good[0]], good + [good[1]],
                     good + [{'Action': 'skip', 'Test': names[0] + '/child'}],
                     good + [{'Action': 'fail'}],
                     good + [{'Action': 'run', 'Test': 'TestPGUnexpected'}]]
        for events in negatives:
            with self.subTest(events=events), self.assertRaises(ValueError):
                self.check(events)


if __name__ == '__main__':
    unittest.main()
