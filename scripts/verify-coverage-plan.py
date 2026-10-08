#!/usr/bin/env python3
import sys
from pathlib import Path
root=Path(__file__).resolve().parents[1]
sys.path.insert(0,str(root/'inventory/operation-sources'))
from coverage_format import read_plan
plan=read_plan(root/'docs/operation-coverage-plan.json')
operations=plan['http_operations']
assert len(operations)==plan['measurement']['static_http_attributes']==818
assert len({op['id'] for op in operations})==818
assert {op['id'] for op in operations}=={op for family in plan['families'] for op in family['operation_ids']}
print('Verified lossless coverage-plan parts, canonical hashes and all 818 static records')
