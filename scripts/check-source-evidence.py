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
    for directory in ("ports/passport/realmboosts/source", "ports/messager/room-media-evidence", "ports/messager/source", "ports/messager/contract-evidence", "ports/develop/source",
                      "ports/develop/product-source", "ports/develop/package-source",
                      "ports/sphere/nodeinfo/source", "ports/wallet/productcatalog/source",
                      "inventory/product-client-evidence", "ports/passport/artwork/source",
                      "ports/passport/affiliation/source", "ports/passport/social/contract-evidence",
                      "inventory/operation-sources", "ports/federation/quote-evidence", "ports/passport/passkit/source", "ports/passport/social/expiry-evidence"):


        base = root / directory
        manifest = json.loads((base / "manifest.json").read_text())
        entries = manifest["files"] if isinstance(manifest, dict) else manifest
        for entry in entries:
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
    for directory in ("internal/auth/source-docs", "inventory/identity-client-evidence/solian", "inventory/identity-client-evidence/blade"):
        base = root / directory
        manifest = json.loads((base / "manifest.json").read_text())
        for entry in manifest["files"]:
            data = (base / entry["path"]).read_bytes()
            blob = hashlib.sha1(b"blob " + str(len(data)).encode() + b"\0" + data).hexdigest()
            if (hashlib.sha256(data).hexdigest() != entry["sha256"]
                    or blob != entry["git_blob_sha"] or len(data) != entry["bytes"]):
                raise ValueError(f"source evidence mismatch: {entry['path']}")
            count += 1
        data = (base / manifest["license_file"]).read_bytes()
        blob = hashlib.sha1(b"blob " + str(len(data)).encode() + b"\0" + data).hexdigest()
        if blob != manifest["license_git_blob_sha"]:
            raise ValueError("auth source license mismatch")
        count += 1
    # Recovery input hashes were independently verified before integration.
    # Freeze retained evidence and oracle fixtures, not editable Go application code.
    prefixes = ("ports/passport/badgediscovery/source/", "ports/passport/badgediscovery/assets/",
                "ports/wallet/exchangecatalog/upstream/", "tests/contract/badges-oracle/",
                "tests/contract/exchange-oracle/")
    for entry in json.loads((root / "docs/recovery-input-blobs.json").read_text()):
        if not entry["path"].startswith(prefixes):
            continue
        data = (root / entry["path"]).read_bytes()
        blob = hashlib.sha1(b"blob " + str(len(data)).encode() + b"\0" + data).hexdigest()
        if blob != entry["sha"] or len(data) != entry["size"]:
            raise ValueError(f"recovery evidence mismatch: {entry['path']}")
        count += 1
    for directory in ("tests/contract/recovery-regression-oracle", "tests/contract/wallet-product-oracle"):
        base = root / directory
        expected = json.loads((base / "validation.json").read_text())["files_sha256"]
        for name, sha256 in expected.items():
            if hashlib.sha256((base / name).read_bytes()).hexdigest() != sha256:
                raise ValueError(f"independent regression evidence mismatch: {name}")
            count += 1
    if (root / "ports/wallet/productcatalog/naming-bmp.json").read_bytes() != (root / "tests/contract/wallet-product-oracle/naming-bmp.json").read_bytes():
        raise ValueError("compiled naming table differs from independent framework evidence")
    count += 1
    base = root / "inventory/product-client-evidence/bot-command-oracle"
    for entry in json.loads((base / "fixture-manifest.json").read_text())["files"]:
        data = (base / entry["file"]).read_bytes()
        if len(data) != entry["bytes"] or hashlib.sha256(data).hexdigest() != entry["sha256"]:
            raise ValueError("documented-model fixture mismatch: " + entry["file"])
        count += 1
    for entry in json.loads((root / "ports/sphere/posts/evidence-inventory.json").read_text())["files"]:
        if hashlib.sha256((root / entry["file"]).read_bytes()).hexdigest() != entry["sha256"]:
            raise ValueError("post lifecycle evidence mismatch: " + entry["file"])
        count += 1
    base = root / "ports/media/source"
    for entry in json.loads((base / "manifest.json").read_text()):
        data = (base / entry["file"]).read_bytes()
        blob = hashlib.sha1(b"blob " + str(len(data)).encode() + b"\0" + data).hexdigest()
        if hashlib.sha256(data).hexdigest() != entry["sha256"] or blob != entry["git_blob"]:
            raise ValueError("media evidence mismatch: " + entry["file"])
        count += 1
    base = root / "ports/messager/mls-evidence"
    for manifest_name in ("manifest.json", "server-docs-manifest.json"):
        manifest = json.loads((base / manifest_name).read_text())
        for entry in manifest["files"]:
            data = (base / entry["file"]).read_bytes()
            blob = hashlib.sha1(b"blob " + str(len(data)).encode() + b"\0" + data).hexdigest()
            if len(data) != entry["bytes"] or hashlib.sha256(data).hexdigest() != entry["sha256"] or blob != entry["blob"]:
                raise ValueError("MLS source evidence mismatch: " + entry["file"])
            count += 1
        data = (base / manifest.get("license_file", "LICENSE.txt")).read_bytes()
        if hashlib.sha256(data).hexdigest() != manifest["license_sha256"]:
            raise ValueError("MLS source license mismatch")
        count += 1
    return count


if __name__ == "__main__":
    try:
        print(f"Verified {check(Path(__file__).resolve().parents[1])} pinned source/license snapshots")
    except (OSError, ValueError, KeyError) as error:
        raise SystemExit(str(error))
