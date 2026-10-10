#!/usr/bin/env python3
"""Offline static relation-name guard, not database readiness or type validation."""
import json
from pathlib import Path
import re
from pglast import parser
from fixture_template import ROOT, fingerprint


def catalog(paths):
    names = set()
    for path in paths:
        # Each runner install is a fresh psql connection with the default path.
        schema = 'public'
        for wrapper in json.loads(parser.parse_sql_json((ROOT / path).read_text()))['stmts']:
            stmt = wrapper['stmt']
            if 'VariableSetStmt' in stmt and stmt['VariableSetStmt'].get('name') == 'search_path':
                raise ValueError('Review explicit schema-install search_path before static resolution: ' + path)
            for kind, field in [('CreateStmt', 'relation'), ('ViewStmt', 'view'), ('CreateSeqStmt', 'sequence')]:
                if kind in stmt:
                    rel = stmt[kind][field]
                    names.add((rel.get('schemaname', schema), rel['relname']))
            if 'IndexStmt' in stmt:
                item = stmt['IndexStmt']
                if item.get('idxname'):
                    # PostgreSQL creates an index in its target table's schema.
                    names.add((item['relation'].get('schemaname', schema), item['idxname']))
    return names


def resolve(name, names):
    parts = name.split('.')
    if len(parts) == 1:
        target = ('public', parts[0])
    elif len(parts) == 2:
        target = tuple(parts)
    else:
        raise ValueError('unsupported static relation name: ' + name)
    if target[0] == 'pg_catalog':
        return 'builtin'  # Server-owned catalog names are outside authored DDL.
    if target not in names:
        raise ValueError('static readiness relation absent at default search_path: ' + name)
    return 'authored'


def main():
    _, paths = fingerprint()
    names = catalog(paths)
    checked, builtins, unresolved = set(), set(), 0
    for path in sorted((ROOT / 'internal').rglob('*.go')):
        if path.name.endswith('_test.go'):
            continue
        text = path.read_text()
        literal = re.findall(r"to_regclass\('([A-Za-z0-9_.]+)'\)", text)
        for name in literal:
            (builtins if resolve(name, names) == 'builtin' else checked).add(name)
        calls = len(re.findall(r'\bto_regclass\s*\(', text))
        if path == ROOT / 'internal/application/readiness.go':
            blocks = re.findall(r'FROM \(VALUES (.*?)\) AS required\(name\)', text, re.S)
            if not blocks or len(blocks) != text.count('to_regclass(name)'):
                raise ValueError('complete primary readiness VALUES inventories required')
            for block in blocks:
                required = re.findall(r"\('([A-Za-z0-9_.]+)'\)", block)
                if not required or re.sub(r"\('[A-Za-z0-9_.]+'\)|[,\s]", '', block):
                    raise ValueError('unsupported primary readiness inventory expression')
                for name in required:
                    (builtins if resolve(name, names) == 'builtin' else checked).add(name)
            calls -= len(blocks)
        unresolved += max(0, calls - len(literal))
    for bad in ['owned_collection_subscription_live', 'owned_taxonomy.missing_collection_index', 'public.missing_owned_table']:
        try:
            resolve(bad, names)
        except ValueError:
            pass
        else:
            raise ValueError('negative relation-resolution control accepted: ' + bad)
    assert resolve('owned_taxonomy.owned_collection_subscription_live', names) == 'authored'
    assert resolve('owned_authority_accounts', names) == 'authored'
    assert resolve('pg_catalog.pg_class', names) == 'builtin'
    print(f'Checked {len(checked)} distinct static readiness relation names against {len(names)} explicit table/view/sequence/index declarations; three missing/search-path controls rejected. {len(builtins)} pg_catalog references and {unresolved} dynamic/unrecognized calls excluded. No server, SQL types, constraints or actual readiness executed.')


if __name__ == '__main__':
    main()
