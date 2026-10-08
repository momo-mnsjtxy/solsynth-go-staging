#!/usr/bin/env python3
"""Offline SQL-looking Go call arity; no connection, type inference or dynamic SQL proof."""
import json
import sys
import pglast
from pglast import parser

if pglast.__version__ != "v6.7" or parser.get_postgresql_version()[0] != 16:
    raise SystemExit("SQL argument guard requires pinned pglast6.7 / PostgreSQL16 grammar")

def highest(sql):
    tree=json.loads(parser.parse_sql_json(sql))
    def walk(node):
        if isinstance(node,dict):
            values=[node['ParamRef']['number']] if 'ParamRef' in node else [0]
            return max(values+[walk(v) for v in node.values()])
        if isinstance(node,list): return max([0]+[walk(v) for v in node])
        return 0
    return walk(tree)

def check(sql,count):
    expected=highest(sql)
    if expected != count: raise ValueError(f"SQL expects {expected} bind arguments; Go supplies {count}")

# Quote/comment content is not a parameter, and duplicate references bind once.
check("SELECT '$99', $$ $42 $$, 1 /* $9 */ -- $4\n",0)
check("SELECT $1::text,$1::text",1)
for sql,count in [("SELECT $1",2),("SELECT $2",1),("SELECT 1",1),("SELECT $1",0)]:
    try: check(sql,count)
    except ValueError: pass
    else: raise AssertionError("wrong bind count accepted")
data=json.load(sys.stdin)
errors=[]
for call in data['calls']:
    try: check(call['sql'],call['args'])
    except Exception as e: errors.append(f"{call['path']}:{call['line']}: {e}")
if errors:
    print('\n'.join(errors),file=sys.stderr)
    raise SystemExit(1)
print(f"Checked {len(data['calls'])} static SQL calls; skipped {data['unresolved']} unresolved, {data['variadic']} variadic and {data['non_sql']} non-SQL constant calls. Four wrong-count controls rejected; quoted/comment references ignored. No SQL types, schema, dynamic construction or execution validated.")
