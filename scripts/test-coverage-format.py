#!/usr/bin/env python3
"""Verify lossless plan reconstruction and rejection of modified parts."""
import json,shutil,sys,tempfile
from pathlib import Path
root=Path(__file__).resolve().parents[1]
sys.path.insert(0,str(root/'inventory/operation-sources'))
from coverage_format import read_plan,canonical
original=read_plan(root/'docs/operation-coverage-plan.json')
with tempfile.TemporaryDirectory(prefix='coverage-format-') as temporary:
    target=Path(temporary)/'operation-coverage-plan.json'
    shutil.copyfile(root/'docs/operation-coverage-plan.json',target)
    info=json.loads(target.read_bytes())
    shutil.copytree(root/'docs'/info['parts_directory'],Path(temporary)/info['parts_directory'])
    assert canonical(read_plan(target))==canonical(original)
    relative=info['fields'][0]['parts'][0]['file']
    part=Path(temporary)/info['parts_directory']/relative
    part.write_bytes(part.read_bytes()+b' ')
    try:read_plan(target)
    except ValueError:pass
    else:raise AssertionError('modified part was accepted')
print('Verified lossless coverage-plan reconstruction and modified-part rejection')
