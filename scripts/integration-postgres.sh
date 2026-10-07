#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
PG_BIN="${PG_BIN:-/workspace/shared/toolchains/postgres/bin}"
GO_BIN="${GO_BIN:-/workspace/shared/toolchains/go/bin/go}"
root="${PG_TEST_ROOT:-/workspace/shared/solsynth-go-integration}"
[[ "$root" == /* ]] || { echo 'PG_TEST_ROOT must be absolute' >&2; exit 2; }
umask 077
mkdir -p "$root"
run=$(mktemp -d "$root/run.XXXXXXXX")
mkdir "$run/socket"
printf 'synthetic-only\n' > "$run/.solsynth-disposable"
export PG_TEST_ROOT="$root"
export PG_TEST_DSN="host=$run/socket port=55432 user=synthetic dbname=postgres sslmode=disable"
export SOLSYNTH_DISPOSABLE_PG="$run"
export PG_TEST_REQUIRED=1
export GOTOOLCHAIN=local
export GOFLAGS="${GOFLAGS:-} -buildvcs=false"
export GOCACHE="${GOCACHE:-/tmp/solsynth-go-cache}"
export GOPATH="${GOPATH:-$("$GO_BIN" env GOPATH)}"
export GOMODCACHE="${GOMODCACHE:-$("$GO_BIN" env GOMODCACHE)}"
export GOPROXY=off GOSUMDB=off
running=0
cleanup() {
  if [[ "$running" == 1 ]]; then "$PG_BIN/pg_ctl" -D "$run/data" -m fast -w stop; fi
  printf 'Disposable integration files retained at %s\n' "$run"
}
trap cleanup EXIT
"$PG_BIN/initdb" -D "$run/data" -U synthetic --auth=trust --no-locale > "$run/initdb.log"
start() {
  "$PG_BIN/pg_ctl" -D "$run/data" -l "$run/postgres.log" -o "-k $run/socket -p 55432 -c listen_addresses='' -c unix_socket_permissions=0700" -w start
  running=1
}
start
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f tests/integration/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/watchpg/testdata/watchers-projection.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/boardpg/testdata/schema.sql
PG_TEST_PHASE=before "$GO_BIN" test -race -count=1 -json ./tests/integration | tee "$run/before.jsonl"
python3 scripts/check-integration-results.py before "$run/before.jsonl"
"$PG_BIN/pg_ctl" -D "$run/data" -m fast -w stop
running=0
start
PG_TEST_PHASE=after "$GO_BIN" test -race -count=1 -json -run '^(TestPGRestartPersistence|TestPGBoardRestartPersistence)$' ./tests/integration | tee "$run/after.jsonl"
python3 scripts/check-integration-results.py after "$run/after.jsonl"
