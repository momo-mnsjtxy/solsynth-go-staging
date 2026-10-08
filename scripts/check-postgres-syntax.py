#!/usr/bin/env python3
"""Parse all maintained SQL and PL/pgSQL without sockets or database execution."""
from pathlib import Path
import subprocess
import sys

try:
    import pglast
    from pglast import parse_sql, parse_plpgsql
    from pglast.parser import ParseError, get_postgresql_version
except ImportError:
    raise SystemExit("Install pinned scripts/sql-parser-requirements.txt into your Python environment before offline verification.")

ROOT = Path(__file__).resolve().parents[1]
if pglast.__version__ != "v6.7" or get_postgresql_version()[0] != 16:
    raise SystemExit("SQL guard requires pglast 6.7 and PostgreSQL-major-16 grammar; a newer grammar cannot certify this CI target.")

# Every tracked or newly queued SQL file is covered. Git exclusions keep tool
# homes, caches, downloaded packages and unrelated local data outside the input.
paths = sorted({p for p in subprocess.check_output(
    ["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z"], cwd=ROOT
).decode().split("\0") if p.endswith(".sql")})
if not paths:
    raise SystemExit("No SQL inputs found")
failures = []
bodies = 0
for name in paths:
    path = ROOT / name
    if path.is_symlink() or not path.is_file():
        failures.append(f"{name}: regular maintained SQL file required")
        continue
    try:
        sql = path.read_text()
        parse_sql(sql)
        bodies += len(parse_plpgsql(sql))
    except (ParseError, ValueError) as exc:
        failures.append(f"{name}: {exc}")
if failures:
    raise SystemExit("\n".join(failures))

# Guard regressions catch the actual reserved identifier failure, ordinary DDL
# damage, and a PL/pgSQL body that parse_sql alone treats as an opaque string.
for sql in (
    "CREATE TABLE synthetic_bad(freeze boolean);",
    "CREATE TABLE synthetic_bad(id uuid value text);",
    "ALTER TABLE synthetic_bad ADD CHECK((id IS NOT NULL);",
    "DO $$ BEGIN IF true THEN RAISE NOTICE 'synthetic'; END $$;",
):
    try:
        parse_sql(sql)
        parse_plpgsql(sql)
    except ParseError:
        pass
    else:
        raise SystemExit("SQL syntax negative control was accepted")
parse_sql('CREATE TABLE synthetic_ok("freeze" boolean, id uuid);')
parse_sql("SELECT $1::uuid, $2::timestamptz")
print(f"Parsed {len(paths)} SQL files and {bodies} PL/pgSQL bodies with pglast 6.7 / PostgreSQL {'.'.join(map(str, get_postgresql_version()))}; four negative controls rejected. No catalog, type, constraint, locking or transaction execution claimed.")
