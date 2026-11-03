"""Read retained review inputs while checking explicitly reviewed local-run changes.

The original CI inventory and local-disposable review remain historical evidence.
Later changes form an explicit chain of retained before versions, ending at the
current file; neither older hashes nor snapshots are rewritten to accept a change.
"""
import hashlib
import json
from pathlib import Path
import re


LOCAL_REVIEW = 'docs/local-disposable-pg-review.json'
UPSTREAM_REVIEW = 'docs/upstream-sync-fixture-review-20261102.json'
ORIGINAL_REVIEW = 'docs/ci-two-groups-dependencies.json'


def _relative(value, prefix):
    name = Path(value)
    if (name.is_absolute() or '..' in name.parts or str(name) != value
            or '\\' in value or not value.startswith(prefix)):
        raise ValueError('invalid review source path: ' + value)
    return name


def _digest(data):
    return hashlib.sha256(data).hexdigest()


def _read(root, name):
    path = root / name
    if not path.resolve().is_relative_to(root.resolve()):
        raise ValueError('review path escapes source root: ' + str(name))
    return path.read_bytes()


def _reviews(root):
    original = _read(root, LOCAL_REVIEW)
    reviews = [(json.loads(original), 'docs/history/local-pg-baseline/')]
    followup = root / UPSTREAM_REVIEW
    if followup.exists():
        review = json.loads(_read(root, UPSTREAM_REVIEW))
        if (review.get('version') != 2 or review.get('previous_review') != LOCAL_REVIEW
                or review.get('previous_review_sha256') != _digest(original)):
            raise ValueError('upstream review does not preserve its predecessor')
        if 'schema_review' in review or 'original_review' in review:
            if (review.get('original_review') != ORIGINAL_REVIEW
                    or review.get('original_review_sha256') != _digest(_read(root, ORIGINAL_REVIEW))):
                raise ValueError('upstream review changed the original dependency review')
        reviews.append((review, 'docs/history/upstream-sync-20261102/'))
    return reviews


def _chain_bytes(root, source, expected, reviews):
    """Resolve one exact historical version through a unique, complete chain."""
    _relative(source, '')
    if not re.fullmatch(r'[0-9a-f]{64}', expected):
        raise ValueError('invalid reviewed source hash: ' + source)
    current = _read(root, source)
    changes = [(entry, prefix) for review, prefix in reviews
               for section in ('reviewed_changes', 'schema_changes')
               for entry in review.get(section, []) if entry['source'] == source]
    if not changes:
        if _digest(current) != expected:
            raise ValueError('changed source has no follow-up review: ' + source)
        return current
    edges, snapshots, incoming = {}, {}, set()
    for entry, prefix in changes:
        before, after = entry['before_sha256'], entry['after_sha256']
        if (not re.fullmatch(r'[0-9a-f]{64}', before)
                or not re.fullmatch(r'[0-9a-f]{64}', after)
                or before == after or before in edges or after in incoming):
            raise ValueError('ambiguous reviewed source chain: ' + source)
        snapshot = _relative(entry['retained_before'], prefix)
        if prefix.endswith('upstream-sync-20261102/') and snapshot.suffix != '.txt':
            raise ValueError('upstream history must be a noncompiled snapshot: ' + source)
        original = _read(root, snapshot)
        if _digest(original) != before:
            raise ValueError('retained review history changed: ' + source)
        edges[before], snapshots[before] = after, original
        incoming.add(after)
    starts = set(edges) - incoming
    if len(starts) != 1:
        raise ValueError('cyclic or disconnected reviewed source chain: ' + source)
    cursor, seen = starts.pop(), set()
    while cursor in edges:
        if cursor in seen:
            raise ValueError('cyclic reviewed source chain: ' + source)
        seen.add(cursor)
        cursor = edges[cursor]
    if len(seen) != len(edges) or _digest(current) != cursor:
        raise ValueError('reviewed source chain does not end at current source: ' + source)
    if expected == cursor:
        return current
    if expected not in snapshots:
        raise ValueError('requested source version is not in review history: ' + source)
    return snapshots[expected]


def reviewed_schema_fingerprint(root, original, current_paths, current_hash):
    """Validate today's schema inventory and reconstruct the unchanged old one."""
    root = Path(root)
    reviews = _reviews(root)
    if len(reviews) != 2:
        raise ValueError('schema changes require an explicit upstream review')
    review = reviews[-1][0]
    schema = review['schema_review']
    old_paths = original['ordered_schema_files']
    if (schema['before_sha256'] != original['schema_sha256']
            or schema['before_ordered_schema_files'] != old_paths
            or schema['after_sha256'] != current_hash
            or schema['after_ordered_schema_files'] != current_paths
            or len(old_paths) != len(set(old_paths))
            or len(current_paths) != len(set(current_paths))
            or [name for name in current_paths if name in old_paths] != old_paths):
        raise ValueError('schema review changed the historical or current inventory')
    schema_changes = review.get('schema_changes', [])
    changed_names = {entry['source'] for entry in schema_changes}
    if not changed_names <= set(old_paths):
        raise ValueError('schema change does not name an original installed source')
    added = review.get('new_schema_sources', [])
    if (len(added) != len({entry['source'] for entry in added})
            or {entry['source'] for entry in added} != set(current_paths) - set(old_paths)):
        raise ValueError('new installed schema inventory is not explicitly reviewed')
    for entry in added:
        name = _relative(entry['source'], 'internal/')
        if name.suffix != '.sql' or _digest(_read(root, name)) != entry['sha256']:
            raise ValueError('added schema source changed: ' + str(name))
    historical = hashlib.sha256()
    current = hashlib.sha256()
    for name in current_paths:
        if _relative(name, '').suffix != '.sql':
            raise ValueError('installed schema is not an explicit SQL source: ' + name)
        data = _read(root, name)
        current.update(name.encode() + b'\0' + data + b'\0')
    if current.hexdigest() != current_hash:
        raise ValueError('current installed schema fingerprint changed')
    for name in old_paths:
        changes = [entry for entry in schema_changes if entry['source'] == name]
        if changes:
            before = {entry['before_sha256'] for entry in changes}
            after = {entry['after_sha256'] for entry in changes}
            starts = before - after
            if len(starts) != 1:
                raise ValueError('schema history has no unique original version: ' + name)
            data = _chain_bytes(root, name, starts.pop(), reviews)
        else:
            data = _read(root, name)
        historical.update(name.encode() + b'\0' + data + b'\0')
    if historical.hexdigest() != original['schema_sha256']:
        raise ValueError('historical installed schema fingerprint changed')
    return historical.hexdigest(), list(old_paths)


def reviewed_bytes(root, source, expected):
    """Return the original review input only after checking both source versions."""
    root = Path(root)
    current = _read(root, _relative(source, ''))
    if hashlib.sha256(current).hexdigest() == expected:
        return current
    reviews = _reviews(root)
    for review, _ in reviews:
        seen = set()
        for added in review.get('new_sources', []):
            name = added['source']
            _relative(name, 'scripts/')
            if name in seen:
                raise ValueError('duplicate added review source: ' + name)
            seen.add(name)
            # A script first added by an older review can itself acquire a new
            # reviewed version. Preserve the old declaration and validate the
            # complete chain instead of refreshing its originally pinned hash.
            _chain_bytes(root, name, added['sha256'], reviews)
    return _chain_bytes(root, source, expected, reviews)
