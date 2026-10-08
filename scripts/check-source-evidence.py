#!/usr/bin/env python3
"""Check retained licensed snapshots against their existing pinned manifests.

Offline integrity only: this does not establish a license grant, full source
coverage, or behavioral equivalence. Never refresh expected hashes here.
"""
import hashlib
import json
from pathlib import Path


def check(root):
    count = 0
    for directory in ("ports/messager/source", "ports/messager/contract-evidence", "ports/develop/source"):
        base = root / directory
        for entry in json.loads((base / "manifest.json").read_text()):
            data = (base / entry["file"]).read_bytes()
            blob = hashlib.sha1(b"blob " + str(len(data)).encode() + b"\0" + data).hexdigest()
            if hashlib.sha256(data).hexdigest() != entry["sha256"] or blob != entry["blob"]:
                raise ValueError(f"source evidence mismatch: {directory}/{entry['file']}")
            count += 1
    base = root / "ports/sphere"
    for entry in json.loads((base / "source-manifest.json").read_text())["files"]:
        data = (base / entry["snapshot"]).read_bytes()
        blob = hashlib.sha1(b"blob " + str(len(data)).encode() + b"\0" + data).hexdigest()
        if len(data) != entry["size_bytes"] or blob != entry["git_blob_sha1"]:
            raise ValueError(f"source evidence mismatch: {entry['snapshot']}")
        count += 1
    return count


if __name__ == "__main__":
    try:
        print(f"Verified {check(Path(__file__).resolve().parents[1])} pinned source/license snapshots")
    except (OSError, ValueError, KeyError) as error:
        raise SystemExit(str(error))
