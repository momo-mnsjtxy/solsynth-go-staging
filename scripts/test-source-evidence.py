#!/usr/bin/env python3
"""Prove that the offline evidence gate detects benign mutations and omissions."""
import importlib.util
import shutil
import tempfile
from pathlib import Path

root = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('evidence', root/'scripts/check-source-evidence.py')
evidence = importlib.util.module_from_spec(spec)
spec.loader.exec_module(evidence)


def check():
    baseline_count = evidence.check(root)
    with tempfile.TemporaryDirectory(prefix='solsynth-evidence-') as directory:
        copy = Path(directory)/'copy'
        shutil.copytree(root, copy, ignore=shutil.ignore_patterns('.git', '.cache', '__pycache__', 'bin', 'obj'))
        assert evidence.check(copy) == baseline_count
        mutations = [
            "ports/messager/mls-evidence/mls_group_manager.dart",
            "ports/messager/mls-evidence/DYSON-LICENSE.txt",
            "ports/media/source/DRIVE_API.md",
            'ports/sphere/posts/upstream/lib__posts__compose.dart',
            'ports/messager/source/ChatService.cs',
            'ports/develop/source/DysonNetwork.Develop__Identity__CustomAppPublicController.cs',
            'ports/develop/package-source/DysonNetwork.Develop__MiniApp__MiniAppService.cs',
            'internal/auth/source-docs/PERMISSIONS.md',
            'internal/auth/source-docs/LICENSE.txt',
            'inventory/operation-sources/DysonNetwork.Sphere/ActivityPub/NodeInfoController.cs',
            'ports/passport/badgediscovery/assets/whatshot.svg',
            'tests/contract/exchange-oracle/expected.json',
            'tests/contract/recovery-regression-oracle/expected.json',
            'ports/wallet/productcatalog/naming-bmp.json',
            'inventory/identity-client-evidence/solian/lib/core/network.dart',
        ]
        for relative in mutations:
            path = copy/relative
            original = path.read_bytes()
            path.write_bytes(original+b'\nbenign integrity-test marker\n')
            try:
                evidence.check(copy)
            except ValueError:
                pass
            else:
                raise AssertionError('mutation not detected: '+relative)
            finally:
                path.write_bytes(original)
        path = copy/'internal/auth/source-docs/PERMISSIONS.md'
        path.unlink()
        try:
            evidence.check(copy)
        except OSError:
            pass
        else:
            raise AssertionError('missing evidence not detected')
    print(f'Verified {baseline_count} source snapshots, {len(mutations)} mutation rejections and missing-file rejection')

if __name__ == '__main__':
    check()
