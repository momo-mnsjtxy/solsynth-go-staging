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
# Owned runtime schemas precede every dependent fixture. These installs are
# explicit and apply only to the synthetic cluster created above.
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/authoritypg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/accountpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/signinpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/notificationpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/apppg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/mediapg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/mediapg/resumable_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/walletpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/commercepg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/postpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/application/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/socialpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/interactionpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f tests/integration/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/watchpg/testdata/watchers-projection.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/boardpg/testdata/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/developpg/testdata/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/developpg/testdata/packages.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/developpg/testdata/bot_commands.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/nodeinfopg/testdata/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/messagerpg/testdata/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/chatpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/taxonomypg/testdata/schema.sql
"$GO_BIN" build -o "$run/backend" ./cmd/backend
export BACKEND_BINARY="$run/backend"
"$GO_BIN" build -o "$run/passport-board" ./cmd/passport-board
"$GO_BIN" build -o "$run/messager-sync" ./cmd/messager-sync
"$GO_BIN" build -o "$run/taxonomy" ./cmd/taxonomy
export PASSPORT_BOARD_BINARY="$run/passport-board"
export MESSAGER_SYNC_BINARY="$run/messager-sync"
export TAXONOMY_BINARY="$run/taxonomy"
"$GO_BIN" build -o "$run/develop-discovery" ./cmd/develop-discovery
"$GO_BIN" build -o "$run/solsynth-go" ./cmd/solsynth-go
"$GO_BIN" build -o "$run/nodeinfo" ./cmd/nodeinfo
PG_TEST_PHASE=before "$GO_BIN" test -race -count=1 -json -run "$(python3 scripts/check-integration-results.py --pattern before)" ./tests/integration | tee "$run/before.jsonl"
python3 scripts/check-integration-results.py before "$run/before.jsonl"
python3 scripts/verify-develop-http.py "$run/develop-discovery"
python3 scripts/verify-catalog-http.py "$run/solsynth-go"
"$PG_BIN/pg_ctl" -D "$run/data" -m fast -w stop
running=0
start
PG_TEST_PHASE=after "$GO_BIN" test -race -count=1 -json -run "$(python3 scripts/check-integration-results.py --pattern after)" ./tests/integration | tee "$run/after.jsonl"
python3 scripts/check-integration-results.py after "$run/after.jsonl"
python3 scripts/verify-develop-http.py "$run/develop-discovery"
python3 scripts/verify-catalog-http.py "$run/solsynth-go"

python3 scripts/verify-candidate-http.py "$run/develop-discovery" "$run/nodeinfo" "$PG_BIN/psql"
