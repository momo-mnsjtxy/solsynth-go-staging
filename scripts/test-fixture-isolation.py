#!/usr/bin/env python3
"""Offline guards and simulated catalog operations; never invokes PostgreSQL."""
import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import tempfile
import unittest

import fixture_template as fixture


class Catalog:
    def __init__(self):
        self.calls = []
        self.tables = [['public', name.split('.')[1]] for name in fixture.BASELINES] + [['wallet_v1', 'funds']]
        self.extensions = [['plpgsql', '1.0']]
        self.source_dirty = False
        self.template_dirty = False
        self.busy = None
        self.wrong_inventory = False
        self.wrong_extensions = False
        self.bad_baseline = False
        self.bad_seal = False
        self.name = None
        self.comment = None

    def tool(self, binary, database, args, text=None):
        self.calls.append(('tool', binary, database, args, text))
        assert binary == 'pg_dump' and database == 'postgres'
        assert args == ['--schema-only', '--clean', '--if-exists', '--no-owner', '--no-privileges']
        return '-- synthetic schema-only dump, no application data'

    def sql(self, database, query):
        self.calls.append(('sql', database, query))
        if query == fixture.FOREIGN_TABLES:
            return '0'
        if 'pg_stat_activity' in query:
            target = query.split("datname='")[1].split("'")[0]
            return '1' if self.busy == target or (self.busy == 'template' and target != 'postgres') else '0'
        if query.startswith('SELECT system_identifier'):
            return '12345'
        if query.startswith('CREATE DATABASE'):
            assert database == 'template1' and 'WITH TEMPLATE template0' in query
            self.name = query.split('"')[1]
        elif query.startswith('COMMENT ON DATABASE'):
            assert database == 'template1'
            self.comment = query.split(" IS '")[1][:-1]
        elif query.startswith('ALTER DATABASE'):
            assert database == 'template1' and query.endswith('ALLOW_CONNECTIONS false')
        elif query.startswith("SELECT '") and 'WHERE EXISTS' in query:
            if database == 'postgres' and self.source_dirty or database != 'postgres' and self.template_dirty:
                return 'wallet_v1.funds'
        return ''

    def value(self, database, query):
        self.calls.append(('value', database, query))
        if query == fixture.TABLES:
            return self.tables[:-1] if self.wrong_inventory and database != 'postgres' else self.tables
        if query == fixture.EXTENSIONS:
            return [] if self.wrong_extensions and database != 'postgres' else self.extensions
        if "'allow_connections'" in query:
            return {'oid': 42, 'allow_connections': self.bad_seal, 'comment': self.comment}
        if self.bad_baseline and query.startswith('SELECT COALESCE(jsonb_agg(to_jsonb(t)'):
            return [{'unexpected': True}]
        return []  # Both source and reviewed baseline queries get the same fake rows.


class IsolationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.parent = Path(self.temp.name)
        self.run = self.parent / 'run.Abcdef12'
        self.run.mkdir(mode=0o700)
        (self.run / 'socket').mkdir(mode=0o700)
        marker = self.run / '.solsynth-disposable'
        marker.write_text('synthetic-only\n')
        marker.chmod(0o600)
        self.env = {'GITHUB_ACTIONS': 'true', 'PG_TEST_PRIVATE_CI': 'true',
                    'PG_TEST_ROOT': str(self.parent), 'SOLSYNTH_DISPOSABLE_PG': str(self.run),
                    'PG_TEST_DSN': f'host={self.run}/socket port=55432 user=synthetic dbname=postgres sslmode=disable',
                    'PG_TEST_CANDIDATE': 'a' * 40, 'GITHUB_SHA': 'a' * 40}

    def tearDown(self):
        self.temp.cleanup()

    def prepare(self, pg):
        return fixture.prepare(pg, self.run, 'a'*40, 'b'*64, ['schema.sql'], fixture.baseline_queries())

    def test_private_root_controls(self):
        self.assertEqual(fixture.validate_environment(self.env), self.run)
        for key, value in [('GITHUB_ACTIONS', 'false'), ('PG_TEST_PRIVATE_CI', 'false'),
                           ('PG_TEST_DSN', self.env['PG_TEST_DSN']+' password=never-log-this'),
                           ('PG_TEST_DSN', self.env['PG_TEST_DSN']+' dbname=production'),
                           ('PG_TEST_CANDIDATE', 'main'), ('GITHUB_SHA', 'c'*40),
                           ('PG_TEST_ROOT', str(self.parent.parent))]:
            with self.subTest(key=key), self.assertRaises(ValueError):
                fixture.validate_environment({**self.env, key: value})
        (self.run / '.solsynth-disposable').write_text('unrelated\n')
        with self.assertRaises(ValueError):
            fixture.validate_environment(self.env)

    def test_marker_and_socket_permissions(self):
        marker = self.run / '.solsynth-disposable'
        marker.chmod(0o644)
        with self.assertRaises(ValueError):
            fixture.validate_environment(self.env)
        marker.chmod(0o600)
        (self.run / 'socket').rmdir()
        (self.run / 'socket').symlink_to(self.parent, target_is_directory=True)
        with self.assertRaises(ValueError):
            fixture.validate_environment(self.env)

    def test_unique_bounded_identifiers(self):
        for name in ['postgres; DROP DATABASE postgres', '"postgres"', 'UPPER', 'a'*64, 'schema.table', '']:
            with self.subTest(name=name), self.assertRaises(ValueError):
                fixture.identifier(name)
        self.assertEqual(fixture.identifier('solsynth_fixture_composer_abc'), '"solsynth_fixture_composer_abc"')

    def test_exact_source_baseline(self):
        queries = fixture.baseline_queries()
        self.assertEqual(set(queries), set(fixture.BASELINES))
        progression = queries['public.owned_progression_definitions']
        self.assertEqual(progression.count('::jsonb'), 58)
        self.assertNotIn('INSERT', progression)
        # Pinned PG16 syntax parser proves the generated VALUES union is valid SQL.
        from pglast import parse_sql
        for query in queries.values():
            parse_sql(query)
        parse_sql(fixture.TABLES)
        parse_sql(fixture.EXTENSIONS)

    def test_fingerprint_changes_with_schema_bytes_and_order(self):
        root = self.parent / 'source'
        (root / 'scripts').mkdir(parents=True)
        (root / 'a.sql').write_text('CREATE TABLE a(id int);')
        (root / 'b.sql').write_text('CREATE TABLE b(id int);')
        prefix = '"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f '
        runner = root / 'scripts/integration-postgres.sh'
        runner.write_text(prefix+'a.sql\n'+prefix+'b.sql\n')
        first = fixture.fingerprint(root)[0]
        (root / 'a.sql').write_text('CREATE TABLE a(id bigint);')
        self.assertNotEqual(first, fixture.fingerprint(root)[0])
        (root / 'a.sql').write_text('CREATE TABLE a(id int);')
        runner.write_text(prefix+'b.sql\n'+prefix+'a.sql\n')
        self.assertNotEqual(first, fixture.fingerprint(root)[0])
        runner.write_text(prefix+'a.sql\n'+prefix+'a.sql\n')
        with self.assertRaises(ValueError):
            fixture.fingerprint(root)

    def test_complete_empty_template_sequence(self):
        pg = Catalog()
        result = self.prepare(pg)
        self.assertEqual(result['baseline_rows_copied'], 0)
        self.assertEqual(json.loads((self.run / 'fixture-template.json').read_text()), result)
        self.assertEqual((self.run / 'fixture-template.json').stat().st_mode & 0o777, 0o600)
        self.assertLessEqual(len(result['name']), 63)
        calls = pg.calls
        dump = next(i for i,c in enumerate(calls) if c[0] == 'tool')
        create = next(i for i,c in enumerate(calls) if c[0]=='sql' and c[2].startswith('CREATE DATABASE'))
        restore = next(i for i,c in enumerate(calls) if c[0]=='sql' and c[1]==result['name'] and c[2].startswith('-- synthetic'))
        seal = next(i for i,c in enumerate(calls) if c[0]=='sql' and c[2].startswith('ALTER DATABASE'))
        self.assertTrue(dump < create < restore < seal)
        self.assertTrue(any(c[0]=='sql' and c[1]==result['name'] and 'WHERE EXISTS' in c[2] for c in calls[restore:seal]))
        self.assertNotIn('pg_terminate_backend', str(calls))
        self.assertNotIn('PG_TEST_DSN', (self.run / 'fixture-template.json').read_text())
        with self.assertRaises(ValueError):
            self.prepare(pg)

    def test_fail_closed_before_marker(self):
        for flag in ['source_dirty', 'template_dirty', 'wrong_inventory', 'wrong_extensions', 'bad_baseline', 'bad_seal']:
            pg = Catalog()
            setattr(pg, flag, True)
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                self.prepare(pg)
            self.assertFalse((self.run / 'fixture-template.json').exists())
            if flag in ['source_dirty', 'bad_baseline']:
                self.assertFalse(any(c[0]=='tool' for c in pg.calls))
        for target in ['postgres', 'template']:
            pg = Catalog()
            pg.busy = target
            with self.subTest(busy=target), self.assertRaises(ValueError):
                self.prepare(pg)
            self.assertFalse((self.run / 'fixture-template.json').exists())


def check_wiring(runner, workflow, composer, helper):
    start = runner.index('python3 scripts/fixture_template.py prepare')
    installs = list(fixture.INSTALL.finditer(runner))
    before = runner.index('PG_TEST_PHASE=before ')
    if runner.count('python3 scripts/fixture_template.py prepare') != 1 or not max(m.end() for m in installs) < start < before:
        raise ValueError('template must follow every install and precede before fixtures')
    if 'export PG_TEST_CANDIDATE="${GITHUB_SHA:?exact CI candidate required}"' not in runner or 'PG_TEST_SCHEMA_SHA256=$(python3 scripts/fixture_template.py fingerprint)' not in runner:
        raise ValueError('candidate/schema binding missing')
    if 'PG_TEST_PRIVATE_CI: ${{ github.event.repository.private }}' not in workflow:
        raise ValueError('private repository gate missing')
    body = composer.split('func TestPGPostCompositionSavedRestartPersistence',1)[1].split('\nfunc ',1)[0]
    before_body, after_body = body.split('loadRestartFixture(t, marker, &state)',1)
    if 'createIsolatedRestartDatabase(t, "composer")' not in before_body or 'newRestartCompositionFixture(t, db)' not in before_body or 'openIsolatedRestartDatabase(t, state.Database)' not in after_body:
        raise ValueError('restart must retain the same isolated database')
    if 'db := database(t)' in body or any(x in after_body for x in ['newRestartCompositionFixture', 'createIsolatedRestartDatabase', 'SeedSynthetic', 'IssueSession', 'authorityKeys(']):
        raise ValueError('after must not create or seed a database/credential')
    for required in ['ObjectState: compositionObjectState(t, db, p.ID)', 'compositionObjectState(t, db, state.PostID)', 'restoreAuthoritySnapshot(t, state.Authority)', 'viewer.token', 'state.AuthorityState']:
        if required not in body:
            raise ValueError('original row/proof assertion missing')
    open_body = helper.split('func openIsolatedRestartDatabase',1)[1].split('\nfunc ',1)[0]
    if 'createIsolatedRestartDatabase' in open_body or 'ExecContext' in open_body or 'verifiedIsolatedDatabase(t, c, template, saved)' not in open_body:
        raise ValueError('after opening must be read only')


class WiringTests(unittest.TestCase):
    def test_reviewed_dependency_inventory(self):
        audit = json.loads((fixture.ROOT / 'docs/ci-two-groups-dependencies.json').read_text())
        review = audit['composer_database_isolation_review']
        schema_hash, paths = fixture.fingerprint()
        self.assertEqual(review['schema_sha256'], schema_hash)
        self.assertEqual(review['ordered_schema_files'], paths)
        self.assertEqual(review['before_clone_baseline_rows'], [])
        self.assertEqual(review['runtime_case_counts'], {'before': 962, 'after': 112})
        self.assertEqual({x['table'] for x in review['source_baselines']}, set(fixture.BASELINES))
        self.assertEqual(sum(x['rows'] for x in review['source_baselines']), 61)
        self.assertTrue(all(x['copied_to_template'] is False for x in review['source_baselines']))
        for entry in review['dependency_sources']:
            self.assertEqual(entry['sha256'], hashlib.sha256((fixture.ROOT / entry['source']).read_bytes()).hexdigest(), entry['source'])

    def test_runner_and_after_failure_controls(self):
        root = fixture.ROOT
        files = [root/'scripts/integration-postgres.sh', root/'.github/workflows/postgres-validation.yml', root/'tests/integration/post_composition_test.go', root/'tests/integration/disposable_database_test.go']
        original = [p.read_text() for p in files]
        check_wiring(*original)
        mutations = [
            (0, 'python3 scripts/fixture_template.py prepare', ''),
            (0, 'PG_TEST_PHASE=before ', 'python3 scripts/fixture_template.py prepare\nPG_TEST_PHASE=before '),
            (0, 'export PG_TEST_CANDIDATE=', 'export MISSING_CANDIDATE='),
            (0, 'PG_TEST_SCHEMA_SHA256=$(python3 scripts/fixture_template.py fingerprint)', 'PG_TEST_SCHEMA_SHA256=stale'),
            (1, 'PG_TEST_PRIVATE_CI:', 'MISSING_PRIVATE_CI:'),
            (2, 'openIsolatedRestartDatabase(t, state.Database)', 'database(t)'),
            (2, 'newRestartCompositionFixture(t, db)', 'newCompositionFixture(t)'),
            (2, 'loadRestartFixture(t, marker, &state)', 'loadRestartFixture(t, marker, &state)\n createIsolatedRestartDatabase(t, "composer")'),
            (2, 'ObjectState: compositionObjectState(t, db, p.ID)', 'ObjectState: ""'),
            (2, 'restoreAuthoritySnapshot(t, state.Authority)', 'freshAuthorityConfig(t)'),
            (3, 'return verifiedIsolatedDatabase(t, c, template, saved)\n}', 'return createIsolatedRestartDatabase(t, "composer")\n}'),
        ]
        for index, old, new in mutations:
            values = original.copy()
            self.assertIn(old, values[index])
            values[index] = values[index].replace(old,new)
            with self.subTest(mutation=old), self.assertRaises(ValueError):
                check_wiring(*values)


if __name__ == '__main__':
    unittest.main()
