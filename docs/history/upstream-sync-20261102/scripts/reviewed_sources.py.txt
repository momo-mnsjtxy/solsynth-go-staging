"""Read retained review inputs while checking explicitly reviewed local-run changes.

The original CI dependency inventory is historical evidence, not a writable hash
cache. A changed input must retain its original bytes and name its current hash
in the separately reviewed local-disposable follow-up record.
"""
import hashlib
import json
from pathlib import Path


def reviewed_bytes(root, source, expected):
    """Return the original review input only after checking both source versions."""
    root = Path(root)
    current = (root / source).read_bytes()
    if hashlib.sha256(current).hexdigest() == expected:
        return current
    review = json.loads((root / 'docs/local-disposable-pg-review.json').read_text())
    for added in review.get('new_sources', []):
        name = Path(added['source'])
        if name.is_absolute() or '..' in name.parts or not str(name).startswith('scripts/'):
            raise ValueError('invalid added review source')
        if hashlib.sha256((root / name).read_bytes()).hexdigest() != added['sha256']:
            raise ValueError('added local review source changed: ' + str(name))
    changes = [entry for entry in review['reviewed_changes'] if entry['source'] == source]
    if len(changes) != 1:
        raise ValueError('changed source has no unique follow-up review: ' + source)
    entry = changes[0]
    snapshot = Path(entry['retained_before'])
    if (snapshot.is_absolute() or '..' in snapshot.parts
            or not str(snapshot).startswith('docs/history/local-pg-baseline/')):
        raise ValueError('invalid historical source snapshot: ' + source)
    original = (root / snapshot).read_bytes()
    if (entry['before_sha256'] != expected
            or hashlib.sha256(original).hexdigest() != expected
            or hashlib.sha256(current).hexdigest() != entry['after_sha256']):
        raise ValueError('reviewed source or retained history changed: ' + source)
    return original
