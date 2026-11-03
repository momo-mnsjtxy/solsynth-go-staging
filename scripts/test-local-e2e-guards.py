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

from reviewed_sources import LOCAL_REVIEW, ORIGINAL_REVIEW, UPSTREAM_REVIEW, reviewed_bytes, reviewed_schema_fingerprint

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
            (root / snapshot).parent.mkdir(parents=True, exist_ok=True)
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

    def chained_review(self, root):
        source, helper = 'tests/integration/example_test.go', 'scripts/helper.py'
        old, middle, current = b'original fixture\n', b'reviewed local mode\n', b'reviewed upstream fixture\n'
        helper_old, helper_new = b'original review tool\n', b'chain-aware review tool\n'
        digest = lambda content: hashlib.sha256(content).hexdigest()

        def write(name, content):
            (root / name).parent.mkdir(parents=True, exist_ok=True)
            (root / name).write_bytes(content)

        def record(name, before, after, prefix):
            snapshot = prefix + name + '.txt'
            write(snapshot, before)
            return {'source': name, 'before_sha256': digest(before),
                    'after_sha256': digest(after), 'retained_before': snapshot}

        local = {'reviewed_changes': [record(source, old, middle, 'docs/history/local-pg-baseline/')],
                 'new_sources': [{'source': helper, 'sha256': digest(helper_old)}]}
        write(LOCAL_REVIEW, json.dumps(local).encode())
        upstream = {'version': 2, 'previous_review': LOCAL_REVIEW,
                    'previous_review_sha256': digest((root / LOCAL_REVIEW).read_bytes()),
                    'reviewed_changes': [record(source, middle, current, 'docs/history/upstream-sync-20261102/'),
                                         record(helper, helper_old, helper_new, 'docs/history/upstream-sync-20261102/')],
                    'new_sources': []}
        write(UPSTREAM_REVIEW, json.dumps(upstream).encode())
        write(source, current)
        write(helper, helper_new)
        return source, old, middle, current, upstream

    def test_chained_reviews_preserve_each_version_and_original_new_sources(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source, old, middle, current, upstream = self.chained_review(root)
            for content in (old, middle, current):
                self.assertEqual(reviewed_bytes(root, source, hashlib.sha256(content).hexdigest()), content)
            # The original local review's new_sources pin remains authoritative,
            # even after its tool has acquired an explicitly reviewed new version.
            helper = root / 'scripts/helper.py'
            helper.write_bytes(helper.read_bytes() + b'unreviewed mutation')
            with self.assertRaises(ValueError):
                reviewed_bytes(root, source, hashlib.sha256(old).hexdigest())

    def test_chain_rejects_forks_cycles_missing_steps_and_history_tampering(self):
        for mutation in ('missing', 'duplicate', 'fork', 'disconnected', 'cycle',
                         'predecessor', 'current', 'history', 'snapshot-path', 'symlink'):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                source, old, middle, current, upstream = self.chained_review(root)
                entry = upstream['reviewed_changes'][0]
                if mutation == 'missing':
                    upstream['reviewed_changes'].pop(0)
                elif mutation == 'duplicate':
                    upstream['reviewed_changes'].append(dict(entry))
                elif mutation == 'fork':
                    upstream['reviewed_changes'].append({**entry, 'after_sha256': 'f' * 64})
                elif mutation == 'disconnected':
                    other_snapshot = 'docs/history/upstream-sync-20261102/disconnected.txt'
                    (root / other_snapshot).write_bytes(b'disconnected source version')
                    upstream['reviewed_changes'].append({**entry, 'source': source,
                        'before_sha256': hashlib.sha256(b'disconnected source version').hexdigest(),
                        'after_sha256': 'f' * 64, 'retained_before': other_snapshot})
                elif mutation == 'cycle':
                    entry['after_sha256'] = hashlib.sha256(old).hexdigest()
                elif mutation == 'predecessor':
                    upstream['previous_review_sha256'] = '0' * 64
                elif mutation == 'current':
                    (root / source).write_bytes(current + b'unreviewed mutation')
                elif mutation == 'history':
                    (root / entry['retained_before']).write_bytes(middle + b'changed history')
                elif mutation == 'snapshot-path':
                    entry['retained_before'] = '../outside'
                elif mutation == 'symlink':
                    snapshot = root / entry['retained_before']
                    snapshot.unlink()
                    snapshot.symlink_to(root.parent / 'outside-review-history')
                (root / UPSTREAM_REVIEW).write_text(json.dumps(upstream))
                with self.assertRaises(ValueError):
                    reviewed_bytes(root, source, hashlib.sha256(old).hexdigest())

    def test_repository_followup_retains_all_declared_before_versions(self):
        root = Path(__file__).resolve().parents[1]
        upstream = json.loads((root / UPSTREAM_REVIEW).read_text())
        for entry in upstream['reviewed_changes'] + upstream['schema_changes']:
            with self.subTest(source=entry['source']):
                self.assertEqual(reviewed_bytes(root, entry['source'], entry['before_sha256']),
                                 (root / entry['retained_before']).read_bytes())

    def test_schema_review_preserves_old_fingerprint_and_rejects_unreviewed_changes(self):
        for mutation in ('none', 'current', 'new-schema', 'history', 'missing-history',
                         'old-inventory', 'new-inventory', 'new-source-list', 'fingerprint', 'old-review'):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                _, _, _, _, review = self.chained_review(root)
                old_path, new_path = 'tests/integration/schema.sql', 'internal/example/sudo_schema.sql'
                old, current, added = b'original schema\n', b'reviewed schema\n', b'new guarded schema\n'
                (root / old_path).parent.mkdir(parents=True, exist_ok=True)
                (root / new_path).parent.mkdir(parents=True, exist_ok=True)
                (root / old_path).write_bytes(current)
                (root / new_path).write_bytes(added)
                snapshot = 'docs/history/upstream-sync-20261102/' + old_path + '.txt'
                (root / snapshot).parent.mkdir(parents=True, exist_ok=True)
                (root / snapshot).write_bytes(old)

                def fingerprint(paths, contents):
                    value = hashlib.sha256()
                    for name, content in zip(paths, contents):
                        value.update(name.encode() + b'\0' + content + b'\0')
                    return value.hexdigest()

                old_hash = fingerprint([old_path], [old])
                current_paths = [old_path, new_path]
                current_hash = fingerprint(current_paths, [current, added])
                original = {'schema_sha256': old_hash, 'ordered_schema_files': [old_path]}
                (root / ORIGINAL_REVIEW).write_text(json.dumps(original))
                review['original_review'] = ORIGINAL_REVIEW
                review['original_review_sha256'] = hashlib.sha256((root / ORIGINAL_REVIEW).read_bytes()).hexdigest()
                review['schema_review'] = {'before_sha256': old_hash, 'before_ordered_schema_files': [old_path],
                    'after_sha256': current_hash, 'after_ordered_schema_files': current_paths}
                review['schema_changes'] = [{'source': old_path, 'before_sha256': hashlib.sha256(old).hexdigest(),
                    'after_sha256': hashlib.sha256(current).hexdigest(), 'retained_before': snapshot}]
                review['new_schema_sources'] = [{'source': new_path, 'sha256': hashlib.sha256(added).hexdigest()}]
                if mutation == 'current':
                    (root / old_path).write_bytes(current + b'tampered')
                elif mutation == 'new-schema':
                    (root / new_path).write_bytes(added + b'tampered')
                elif mutation == 'history':
                    (root / snapshot).write_bytes(old + b'tampered')
                elif mutation == 'missing-history':
                    review['schema_changes'] = []
                elif mutation == 'old-inventory':
                    review['schema_review']['before_ordered_schema_files'] = []
                elif mutation == 'new-inventory':
                    review['schema_review']['after_ordered_schema_files'] = list(reversed(current_paths))
                elif mutation == 'new-source-list':
                    review['new_schema_sources'] = []
                elif mutation == 'fingerprint':
                    review['schema_review']['after_sha256'] = '0' * 64
                elif mutation == 'old-review':
                    (root / ORIGINAL_REVIEW).write_text('changed original review')
                (root / UPSTREAM_REVIEW).write_text(json.dumps(review))
                if mutation == 'none':
                    self.assertEqual(reviewed_schema_fingerprint(root, original, current_paths, current_hash),
                                     (old_hash, [old_path]))
                else:
                    with self.assertRaises(ValueError):
                        reviewed_schema_fingerprint(root, original, current_paths, current_hash)


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
