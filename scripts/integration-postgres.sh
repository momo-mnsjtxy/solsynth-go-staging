#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
# Only this explicit command-line opt-in may select local execution. Do not
# manufacture CI variables; every fixture still verifies the private cluster.
backend_e2e=0
local_tcp=0
case "$#:${1:-}:${2:-}" in
  0::) [[ "${PG_TEST_MODE:-}" != local-disposable ]] || { echo 'Local disposable mode requires --local-disposable' >&2; exit 2; } ;;
  1:--local-disposable:) export PG_TEST_MODE=local-disposable ;;
  2:--local-disposable:--backend-e2e|2:--backend-e2e:--local-disposable)
    export PG_TEST_MODE=local-disposable
    backend_e2e=1 ;;
  3:--local-disposable:--backend-e2e)
    [[ "$3" == --local-tcp ]] || { echo 'unknown local backend transport option' >&2; exit 2; }
    export PG_TEST_MODE=local-disposable
    backend_e2e=1
    local_tcp=1 ;;
  *) echo 'usage: integration-postgres.sh [--local-disposable [--backend-e2e [--local-tcp]]]' >&2; exit 2 ;;
esac
if [[ "$local_tcp" != 1 && -n "${PG_TEST_LOCAL_TRANSPORT:-}${PG_TEST_LOCAL_PORT:-}${PG_TEST_LOCAL_PASSWORD:-}" ]]; then
  echo 'Local transport requires explicit --local-tcp for the local backend suite' >&2
  exit 2
fi
export PG_BIN="${PG_BIN:-/workspace/shared/toolchains/postgres/bin}"
GO_BIN="${GO_BIN:-/workspace/shared/toolchains/go/bin/go}"
root="${PG_TEST_ROOT:-/workspace/shared/solsynth-go-integration}"
[[ "$root" == /* ]] || { echo 'PG_TEST_ROOT must be absolute' >&2; exit 2; }
group="${PG_TEST_GROUP:-all}"
case "$group" in all|group-a|group-b) ;; *) echo 'unknown PG_TEST_GROUP' >&2; exit 2 ;; esac
if [[ "$backend_e2e" == 1 ]]; then group=backend-e2e; fi
python3 scripts/check-integration-groups.py validate
# Validate authorization and clean source before creating a run directory.
# CI binds GITHUB_SHA; local mode derives its candidate from the actual HEAD.
export PG_TEST_CANDIDATE
PG_TEST_CANDIDATE=$(python3 scripts/fixture_template.py candidate)
export PG_TEST_SCHEMA_SHA256
PG_TEST_SCHEMA_SHA256=$(python3 scripts/fixture_template.py fingerprint)
umask 077
mkdir -p "$root"
run=$(mktemp -d "$root/run.XXXXXXXX")
mkdir "$run/socket"
printf 'synthetic-only\n' > "$run/.solsynth-disposable"
export PG_TEST_ROOT="$root"
if [[ "$local_tcp" == 1 ]]; then
  export PG_TEST_LOCAL_TRANSPORT=loopback-tcp
  # Probe an available loopback port and use a synthetic SCRAM credential.
  # PostgreSQL must still bind the selected port successfully; no retry can
  # attach this suite to an existing listener or database.
  read -r PG_TEST_LOCAL_PORT PG_TEST_LOCAL_PASSWORD < <(python3 - <<'PY'
import secrets
import socket
with socket.socket() as listener:
    listener.bind(('127.0.0.1', 0))
    print(listener.getsockname()[1], secrets.token_hex(32))
PY
  )
  export PG_TEST_LOCAL_PORT PG_TEST_LOCAL_PASSWORD
  export PG_TEST_DSN="host=127.0.0.1 port=$PG_TEST_LOCAL_PORT user=synthetic dbname=postgres sslmode=disable password=$PG_TEST_LOCAL_PASSWORD"
else
export PG_TEST_DSN="host=$run/socket port=55432 user=synthetic dbname=postgres sslmode=disable"
fi
export SOLSYNTH_DISPOSABLE_PG="$run"
export PG_TEST_REQUIRED=1
export GOTOOLCHAIN=local
export GOFLAGS="${GOFLAGS:-} -buildvcs=false"
export GOCACHE="${GOCACHE:-$("$GO_BIN" env GOCACHE)}"
export GOPATH="${GOPATH:-$("$GO_BIN" env GOPATH)}"
export GOMODCACHE="${GOMODCACHE:-$("$GO_BIN" env GOMODCACHE)}"
export GOPROXY=off GOSUMDB=off
results="${PG_TEST_RESULTS:-$run/results}"
[[ "$results" == /* ]] || { echo 'PG_TEST_RESULTS must be absolute' >&2; exit 2; }
mkdir -p "$results"
mkdir "$results/$group"
: > "$run/process-smokes.txt"
running=0
cleanup() {
  status=$?
  for evidence in before.jsonl after.jsonl cluster-before.txt cluster-after.txt process-smokes.txt fixture-template.json receipt.json; do
    if [[ -f "$run/$evidence" ]]; then
      if ! cp "$run/$evidence" "$results/$group/$evidence"; then status=1; fi
    fi
  done
  if [[ "$running" == 1 ]]; then
    if ! "$PG_BIN/pg_ctl" -D "$run/data" -m fast -w stop; then status=1; fi
  fi
  printf 'Disposable integration files retained at %s\n' "$run"
  exit "$status"
}
trap cleanup EXIT
if [[ "$local_tcp" == 1 ]]; then
  printf '%s\n' "$PG_TEST_LOCAL_PASSWORD" > "$run/postgres-password"
  "$PG_BIN/initdb" -D "$run/data" -U synthetic --auth-local=trust --auth-host=scram-sha-256 --pwfile="$run/postgres-password" --no-locale > "$run/initdb.log"
else
  "$PG_BIN/initdb" -D "$run/data" -U synthetic --auth=trust --no-locale > "$run/initdb.log"
fi
start() {
  if [[ "$local_tcp" == 1 ]]; then
    "$PG_BIN/pg_ctl" -D "$run/data" -l "$run/postgres.log" -o "-p $PG_TEST_LOCAL_PORT -c listen_addresses=127.0.0.1 -c unix_socket_directories=''" -w start
  else
  "$PG_BIN/pg_ctl" -D "$run/data" -l "$run/postgres.log" -o "-k $run/socket -p 55432 -c listen_addresses='' -c unix_socket_permissions=0700" -w start
  fi
  running=1
}
start
# Owned runtime schemas precede every dependent fixture. These installs are
# explicit and apply only to the synthetic cluster created above.
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/authoritypg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/authoritypg/assurance_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/authoritypg/continuity_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/authoritypg/activation_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/authoritypg/stream_ticket_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/readerpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/accountpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/accountpg/name_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/signinpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/signinpg/address_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/signinpg/payment_pin_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/signinpg/factor_setup_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/accountsettingspg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/ticketpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/affiliationpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/notificationpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/notificationpg/devices_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/notificationpg/wellbeing_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/notificationpg/friend_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/apppg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/oauthpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/mediapg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/mediapg/usage_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/mediapg/resumable_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/mediapg/artwork_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/botpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/developmediapg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/walletpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/affiliationpg/paid_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/walletpg/pin_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/walletpg/transfer_requests_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/walletpg/funds_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/walletpg/exchange_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/commercepg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/commercepg/custom_order_identifier.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/commercepg/coupon_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/commercepg/shipping_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/commercepg/order_index_scope.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/commercepg/maintenance_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/translationpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/postpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/socialpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/federationpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/federationpg/identity_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/federationpg/discovery_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/federationpg/public_admission_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/federationpg/quote_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/federationpg/delivery_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/federationpg/broader_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/application/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/realmboostpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/socialpg/permissions_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/realmmemberpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/socialpg/expiry_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/postpg/realm_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/merchantpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/locationpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/interactionpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/interactionpg/collection_publication_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/timelinepg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/timelinepg/subscription_state_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/postmediapg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/profilemediapg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/passkitpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/ticketmediapg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/surveypg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/stickerpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f tests/integration/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/watchpg/testdata/watchers-projection.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/boardpg/testdata/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/developpg/testdata/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/developpg/testdata/packages.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/developpg/testdata/bot_commands.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/nodeinfopg/testdata/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/messagerpg/testdata/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/chatpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/botpg/execution_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/chatmediapg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/roommediapg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/chatpg/relations_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/chatpg/mls_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/presencepg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/progressionpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/accounthistorypg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/accounthistorypg/request_context_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/connectionpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/notabledaypg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/calendarmediapg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/fortunepg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/callpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/taxonomypg/testdata/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/namecardpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/rewindpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/moderationpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/postpg/scheduling_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/blogpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/livestreampg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/publishernotificationpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/adminpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/livestreampg/chat_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/livestreampg/outputs_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/livestreampg/awards_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/domaintrustpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/sponsorshippg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/publisherratingpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f internal/postawardpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/postembedpg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/chatpg/organization_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/chatpg/transient_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/mediapg/voice_schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/activitypg/schema.sql
"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/friendeventpg/schema.sql
# A prepare-only SQL guard must finish and close its source connections above
# this point. All schema installs precede the empty template; no fixture seeds do.
python3 scripts/fixture_template.py prepare
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
"$PG_BIN/psql" "$PG_TEST_DSN" -A -t -F '|' -v ON_ERROR_STOP=1 -c 'SELECT system_identifier,pg_postmaster_start_time() FROM pg_control_system()' > "$run/cluster-before.txt"
if [[ "$backend_e2e" == 1 ]]; then
  # This explicit subset validates the unified host only. Its result guard
  # cannot issue the full PostgreSQL catalogue's acceptance receipt.
PG_TEST_PHASE=before "$GO_BIN" test -timeout 10m -race -count=1 -json -run '^(TestPGBackendHTTPProcessRestart|TestPGBackendSigninHTTPProcessRestart|TestPGBackendRestartPersistence)$' ./tests/integration | tee "$run/before.jsonl"
  python3 scripts/check-backend-e2e-results.py before "$run/before.jsonl"
else
PG_TEST_PHASE=before "$GO_BIN" test -timeout 40m -race -count=1 -json -run "$(python3 scripts/check-integration-results.py --pattern before --group "$group")" ./tests/integration | tee "$run/before.jsonl"
  python3 scripts/check-integration-results.py before "$run/before.jsonl" --group "$group"
fi
if [[ "$group" == all || "$group" == group-a ]]; then
  python3 scripts/verify-develop-http.py "$run/develop-discovery"
  printf 'develop-before\n' >> "$run/process-smokes.txt"
  python3 scripts/verify-catalog-http.py "$run/solsynth-go"
  printf 'catalog-before\n' >> "$run/process-smokes.txt"
fi
"$PG_BIN/pg_ctl" -D "$run/data" -m fast -w stop
running=0
start
"$PG_BIN/psql" "$PG_TEST_DSN" -A -t -F '|' -v ON_ERROR_STOP=1 -c 'SELECT system_identifier,pg_postmaster_start_time() FROM pg_control_system()' > "$run/cluster-after.txt"
if [[ "$backend_e2e" == 1 ]]; then
PG_TEST_PHASE=after "$GO_BIN" test -timeout 10m -race -count=1 -json -run '^TestPGBackendRestartPersistence$' ./tests/integration | tee "$run/after.jsonl"
  python3 scripts/check-backend-e2e-results.py after "$run/after.jsonl"
else
PG_TEST_PHASE=after "$GO_BIN" test -timeout 10m -race -count=1 -json -run "$(python3 scripts/check-integration-results.py --pattern after --group "$group")" ./tests/integration | tee "$run/after.jsonl"
  python3 scripts/check-integration-results.py after "$run/after.jsonl" --group "$group"
fi
if [[ "$group" == all || "$group" == group-a ]]; then
  python3 scripts/verify-develop-http.py "$run/develop-discovery"
  printf 'develop-after\n' >> "$run/process-smokes.txt"
  python3 scripts/verify-catalog-http.py "$run/solsynth-go"
  printf 'catalog-after\n' >> "$run/process-smokes.txt"
  python3 scripts/verify-candidate-http.py "$run/develop-discovery" "$run/nodeinfo" "$PG_BIN/psql"
  printf 'candidate-after\n' >> "$run/process-smokes.txt"
fi
if [[ "$backend_e2e" != 1 && "$group" != all ]]; then
  python3 scripts/check-integration-groups.py finalize --group "$group" --directory "$run" \
    --commit "$PG_TEST_CANDIDATE" --run-id "${GITHUB_RUN_ID:-local}" --attempt "${GITHUB_RUN_ATTEMPT:-1}"
fi
if [[ "$backend_e2e" == 1 ]]; then
  python3 scripts/check-backend-e2e-results.py finalize "$run"
fi
