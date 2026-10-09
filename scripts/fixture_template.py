#!/usr/bin/env python3
"""Private-CI-only schema template; import/fingerprint never connects to PostgreSQL."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
INSTALL = re.compile(r'^"\$PG_BIN/psql" "\$PG_TEST_DSN" -v ON_ERROR_STOP=1 (?:--single-transaction )?-f ([a-zA-Z0-9_./-]+)$', re.M)
BASELINES = {
    'public.owned_reader_cache_state': 'internal/readerpg/schema.sql',
    'public.owned_automod_state': 'internal/adminpg/schema.sql',
    'public.owned_domain_trust_state': 'internal/domaintrustpg/schema.sql',
    'public.owned_progression_definitions': 'internal/progressionpg/schema.sql',
}
FOREIGN_TABLES = "SELECT count(*) FROM pg_foreign_table"
TABLES = """SELECT COALESCE(jsonb_agg(jsonb_build_array(n.nspname,c.relname) ORDER BY n.nspname,c.relname),'[]') FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname NOT LIKE 'pg_%' AND n.nspname<>'information_schema' AND c.relkind IN('r','p','m','f')"""
EXTENSIONS = "SELECT COALESCE(jsonb_agg(jsonb_build_array(extname,extversion) ORDER BY extname),'[]') FROM pg_extension"


def fingerprint(root=ROOT):
    paths = INSTALL.findall((root / 'scripts/integration-postgres.sh').read_text())
    if not paths or len(paths) != len(set(paths)):
        raise ValueError('unique explicit schema inventory required')
    digest = hashlib.sha256()
    for name in paths:
        data = (root / name).read_bytes()
        digest.update(name.encode() + b'\0' + data + b'\0')
    return digest.hexdigest(), paths


def identifier(value):
    if not re.fullmatch(r'[a-z_][a-z0-9_]{0,62}', value):
        raise ValueError('invalid disposable SQL identifier')
    return '"' + value + '"'


def validate_environment(env):
    if env.get('GITHUB_ACTIONS') != 'true' or env.get('PG_TEST_PRIVATE_CI') != 'true':
        raise ValueError('template creation requires authorized private CI')
    parent = Path(env.get('PG_TEST_ROOT', ''))
    run = Path(env.get('SOLSYNTH_DISPOSABLE_PG', ''))
    if (not re.fullmatch(r'/[A-Za-z0-9_./-]+', str(run)) or not parent.is_absolute() or not run.is_absolute() or run.parent != parent
            or not re.fullmatch(r'run\.[A-Za-z0-9]{8}', run.name)
            or run.resolve() != run or parent.resolve() != parent
            or not run.is_dir() or run.stat().st_mode & 0o077):
        raise ValueError('marked private disposable root required')
    marker = run / '.solsynth-disposable'
    socket = run / 'socket'
    if (marker.is_symlink() or not marker.is_file() or marker.read_text() != 'synthetic-only\n'
            or marker.stat().st_mode & 0o077 or socket.is_symlink()
            or not socket.is_dir() or socket.stat().st_mode & 0o077):
        raise ValueError('private disposable marker and socket directory required')
    expected = f'host={run}/socket port=55432 user=synthetic dbname=postgres sslmode=disable'
    if env.get('PG_TEST_DSN') != expected or not re.fullmatch(r'[a-f0-9]{40}', env.get('PG_TEST_CANDIDATE', '')):
        raise ValueError('exact disposable connection and candidate required')
    if env.get('GITHUB_SHA') != env['PG_TEST_CANDIDATE']:
        raise ValueError('candidate differs from CI checkout')
    return run


def baseline_queries(root=ROOT):
    # These are the only installed rows. They are audited in postgres and never
    # copied to the template. New baseline tables require a deliberate review.
    expected = {name: "SELECT '[{\"singleton\":true,\"generation\":0}]'::jsonb" for name in BASELINES if not name.endswith('definitions')}
    source = (root / BASELINES['public.owned_progression_definitions']).read_text()
    values = re.findall(r"^INSERT INTO owned_progression_definitions\(kind,identifier,definition,updated_at\)VALUES(\(.*\)) ON CONFLICT\(kind,identifier\)DO NOTHING;$", source, re.M)
    if len(values) != 58:
        raise ValueError('review the schema-defined progression baseline inventory')
    expected['public.owned_progression_definitions'] = (
        'WITH expected(kind,identifier,definition,updated_at) AS (VALUES ' + ','.join(values) + ') '
        "SELECT jsonb_agg(to_jsonb(x) ORDER BY kind,identifier) FROM (SELECT kind,identifier,definition,updated_at::timestamptz FROM expected) x")
    return expected


class Postgres:
    def __init__(self, env, run):
        self.bin = Path(env['PG_BIN'])
        self.env = {'PATH': env.get('PATH', ''), 'LC_ALL': 'C', 'PGHOST': str(run / 'socket'),
                    'PGPORT': '55432', 'PGUSER': 'synthetic', 'PGSSLMODE': 'disable',
                    'PGCONNECT_TIMEOUT': '10', 'PGOPTIONS': '-c statement_timeout=120000 -c timezone=UTC'}

    def tool(self, binary, database, args, text=None):
        try:
            return subprocess.run([str(self.bin / binary), *args], input=text, text=True,
                                  env={**self.env, 'PGDATABASE': database}, check=True,
                                  stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=180).stdout
        except (subprocess.SubprocessError, OSError):
            # Driver/tool errors can include connection strings or SQL data.
            raise ValueError('private fixture PostgreSQL operation failed; no connection details emitted') from None

    def sql(self, database, sql):
        return self.tool('psql', database, ['-X', '-qAt', '-v', 'ON_ERROR_STOP=1'], sql).strip()

    def value(self, database, sql):
        return json.loads(self.sql(database, sql))


def audit_rows(pg, database, tables, baselines):
    if not tables or any(not isinstance(t, list) or len(t) != 2 for t in tables):
        raise ValueError('nonempty application relation inventory required')
    known = {'.'.join(t) for t in tables}
    if not set(baselines) <= known:
        raise ValueError('reviewed baseline table is absent')
    selects = []
    for schema, table in tables:
        name = schema + '.' + table
        qualified = identifier(schema) + '.' + identifier(table)
        if name in baselines:
            order = 'kind,identifier' if table.endswith('definitions') else 'singleton'
            actual = pg.value(database, f"SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY {order}),'[]') FROM {qualified} t")
            if actual != pg.value(database, baselines[name]):
                raise ValueError('schema baseline differs from reviewed source: ' + name)
        else:
            selects.append(f"SELECT '{name}' AS relation WHERE EXISTS(SELECT 1 FROM {qualified})")
    if selects:
        populated = pg.sql(database, ' UNION ALL '.join(selects)).splitlines()
        if populated:
            # Report relation names only, never row contents.
            if any(name not in known for name in populated):
                raise ValueError('invalid application data inventory')
            raise ValueError('unexpected application rows in: ' + ', '.join(populated))


def no_connections(pg, database):
    # Never terminate another session. PostgreSQL also refuses CREATE DATABASE
    # from a busy template; the explicit check makes ownership mistakes clearer.
    identifier(database)
    if pg.sql('template1', f"SELECT count(*) FROM pg_stat_activity WHERE datname='{database}'") != '0':
        raise ValueError('template source has active connections')


def prepare(pg, run, candidate, schema_hash, paths, baselines):
    marker = run / 'fixture-template.json'
    if marker.exists() or marker.is_symlink():
        raise ValueError('template marker already exists')
    if pg.sql('postgres', FOREIGN_TABLES) != '0':
        raise ValueError('foreign tables cannot enter a disposable template')
    tables = pg.value('postgres', TABLES)
    extensions = pg.value('postgres', EXTENSIONS)
    audit_rows(pg, 'postgres', tables, baselines)
    no_connections(pg, 'postgres')
    # Source schema includes all schemas, functions, triggers and extensions.
    # --schema-only excludes the audited seed rows, credentials and test data.
    dump = pg.tool('pg_dump', 'postgres', ['--schema-only', '--clean', '--if-exists', '--no-owner', '--no-privileges'])
    no_connections(pg, 'postgres')
    nonce = hashlib.sha256((str(run) + '\0' + candidate).encode()).hexdigest()[:24]
    name = 'solsynth_template_' + nonce
    pg.sql('template1', f'CREATE DATABASE {identifier(name)} WITH TEMPLATE template0')
    pg.sql(name, dump)
    if pg.value(name, TABLES) != tables or pg.value(name, EXTENSIONS) != extensions:
        raise ValueError('restored schema/extension inventory differs from source')
    audit_rows(pg, name, tables, {})
    comment = f'solsynth-fixture-template/v1/{candidate}/{schema_hash}/{nonce}'
    pg.sql('template1', f"COMMENT ON DATABASE {identifier(name)} IS '{comment}'")
    no_connections(pg, name)
    pg.sql('template1', f'ALTER DATABASE {identifier(name)} ALLOW_CONNECTIONS false')
    no_connections(pg, name)
    identity = pg.value('template1', f"SELECT jsonb_build_object('oid',oid::bigint,'allow_connections',datallowconn,'comment',shobj_description(oid,'pg_database')) FROM pg_database WHERE datname='{name}'")
    if identity['allow_connections'] or identity['comment'] != comment or identity['oid'] <= 0:
        raise ValueError('template identity/connection seal failed')
    cluster = pg.sql('template1', 'SELECT system_identifier::text FROM pg_control_system()')
    if not re.fullmatch(r'[0-9]+', cluster):
        raise ValueError('invalid cluster identity')
    result = {'version': 1, 'candidate': candidate, 'schema_sha256': schema_hash,
              'name': name, 'oid': identity['oid'], 'cluster': cluster, 'comment': comment,
              'schema_files': paths, 'extensions': extensions, 'application_tables': len(tables),
              'baseline_rows_copied': 0}
    # The marker contains no DSN, secrets or source rows. It stays inside the
    # private marked run directory and survives the actual PostgreSQL restart.
    fd = os.open(marker, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w') as handle:
        json.dump(result, handle, indent=2)
        handle.write('\n')
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('operation', choices=('fingerprint', 'prepare'))
    args = parser.parse_args()
    try:
        schema_hash, paths = fingerprint()
        if args.operation == 'fingerprint':
            print(schema_hash)
            return
        run = validate_environment(os.environ)
        candidate = os.environ['PG_TEST_CANDIDATE']
        head = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
        dirty = subprocess.run(['git', 'diff', '--quiet', 'HEAD', '--'], cwd=ROOT).returncode
        if head != candidate or dirty or os.environ.get('PG_TEST_SCHEMA_SHA256') != schema_hash:
            raise ValueError('template requires the exact clean candidate and schema fingerprint')
        prepare(Postgres(os.environ, run), run, candidate, schema_hash, paths, baseline_queries())
        print('Verified schema-only disposable template: zero application rows; source baseline audited; connections disabled')
    except ValueError as error:
        # Only our sanitized guard failures and relation names reach the log.
        sys.exit(str(error))
    except (KeyError, OSError, subprocess.SubprocessError):
        sys.exit('Disposable fixture template setup failed; no connection details emitted')


if __name__ == '__main__':
    main()
