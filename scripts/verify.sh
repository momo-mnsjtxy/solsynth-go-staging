#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
python3 scripts/check-workflow-yaml.py
python3 scripts/check-source-evidence.py
python3 scripts/test-source-evidence.py
python3 scripts/test-integration-guard.py
python3 scripts/test-integration-groups.py
python3 scripts/test-integration-wiring.py
python3 scripts/test-fixture-isolation.py
python3 scripts/check-postgres-syntax.py
python3 scripts/verify-coverage-plan.py
python3 scripts/test-coverage-format.py
GO_BIN="${GO_BIN:-go}"
if ! "$GO_BIN" version | grep -q '^go version go'; then
  echo 'A Go compiler is required; set GO_BIN to its executable path.' >&2
  exit 2
fi
export GOTOOLCHAIN=local
export GOPROXY=off
export GOSUMDB=off
export GOFLAGS="${GOFLAGS:-} -buildvcs=false"
gofmt_bin="$(dirname "$GO_BIN")/gofmt"
if [[ "$GO_BIN" == go ]]; then gofmt_bin=gofmt; fi
unformatted=$(find cmd internal ports tests -name '*.go' -type f -exec "$gofmt_bin" -l {} +)
if [[ -n "$unformatted" ]]; then
  printf 'Unformatted Go files:\n%s\n' "$unformatted" >&2
  exit 1
fi
"$GO_BIN" run ./scripts/sqlargs . | python3 scripts/check-sql-argument-count.py
"$GO_BIN" test ./tests/integration -list '^TestPG' | python3 scripts/check-integration-groups.py compiled
"$GO_BIN" build ./...
"$GO_BIN" vet ./...
"$GO_BIN" test -count=1 ./...
"$GO_BIN" test -race -count=1 ./...
if [[ -z "${PG_TEST_DSN:-}" ]]; then
  echo 'PostgreSQL integration tests were SKIPPED (no PG_TEST_DSN); package ok is not database validation.'
  echo 'Use scripts/integration-postgres.sh in an authorized environment with private Unix sockets.'
fi
