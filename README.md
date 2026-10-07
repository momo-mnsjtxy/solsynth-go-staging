# Solsynth Go migration workspace

**Status: initial, local-only domain ports and migration evidence. This is not a functional replacement for the Solsynth backend.**

The requested target is backend functional parity while retaining the existing frontend clients. The organization contains multiple products and already-migrated Go services. Replacing clients or rebuilding existing Go services is not part of this initial stage.

## Implemented so far

The three initial policy packages below derive from the licensed DysonNetwork revision `4ee2d0cebb9da8284a92483fee6dce795bf6947c`, with source snapshots, licenses, and per-package provenance retained:

- `ports/passport/username`: account username normalization and candidate generation with injected lookup, randomness, and clock
- `ports/sphere`: deterministic timeline ranking and selection, separated from database, identity, visibility, and moderation signal resolution
- `ports/messager`: room-sync exact-sequence and inclusive-range recovery selection
- `ports/passport/board`: partial transport-independent board service using pinned licensed client/server contracts, with mandatory validation/cache/repository interfaces

The policy and board packages are Go library functions with synthetic contract tests. A loopback-only development HTTP service for post-watch preferences is implemented and tested with an atomic local JSON persistence adapter. A transport-independent Passport board service is also included with explicit unresolved production adapters. No production HTTP service, upstream database migration, authentication implementation, or deployment has been completed. See `docs/parity-matrix.json` and package parity inventories for the remaining work and evidence.

## Test locally

Use a real Go compiler (Go 1.25+); this workspace was verified with Go 1.26.4. The `/usr/bin/go` executable in the current environment is not the Go compiler.

```sh
GOPATH=/workspace/shared/go GOMODCACHE=/workspace/shared/go/pkg/mod \
  GO_BIN=/workspace/shared/toolchains/go/bin/go GOCACHE="$PWD/.cache/go-build" bash ./scripts/verify.sh
```

The initial policy ports use the standard library; the PostgreSQL command now uses pgx v5.10.0 with dependencies pinned in go.mod/go.sum. Run `go mod download` once in a network-enabled development environment before the offline verification script. Tests are synthetic and do not contact Solsynth production or use personal data. Passing them establishes only the cases checked. No .NET differential runtime oracle or full existing-client end-to-end test has run.

## Architecture direction

Preserve current service boundaries and transport contracts until measured evidence supports consolidation. The first refactoring boundary separates deterministic domain policy from infrastructure adapters. Database access, account lookup, authorization, external RPC, and event delivery must stay explicit dependencies rather than being replaced by successful-looking in-memory stubs.

Existing Stargate, DysonFS/FileSystem, Metoer, Blade, and other Go components require license and interface review before integration. In particular, absence of a license is not permission to copy an implementation. The backend migration must preserve client-facing JSON names, null behavior, integer widths, timestamps, pagination, authorization failures, event ordering, and operational behavior. The compatibility inventory records known differences.

Do not deploy these packages as a replacement API. Completion requires every endpoint, authorization rule, database transition, event, storage action, scheduled job, failure mode, and migration/rollback procedure to be mapped and tested against the accepted baseline.

## Inventory and provenance limits

`inventory/sosys-repositories.json` lists all 19 repositories visible on the two public SoSYS organization pages at review time. It does not establish private repository coverage or repository-level mirror/archive status. The organization API returned 403 and was not retried; later repository-page retrieval was cancelled and not retried. The raw successfully read organization pages are retained as inventory evidence.

The source snapshots preserve exact upstream revisions and license notices; they are not a clone of complete Git history. A Stargate clone was cancelled and was not retried. No upstream commits, branches, pull requests, deployments, accounts, or credentials were created.

## Licensing

The derived initial ports are distributed under AGPL-3.0 with upstream license text preserved. See `LICENSE.txt` and package provenance. No unlicensed implementation is included. Other repositories remain subject to independent license review; interface-only independent implementation must record its lawful contract sources and must not be represented as clean-room if implementation was inspected.

## Post-watch preference development slice

The Go host implements the licensed post-watch preference service contract at `GET/PUT /api/posts/watch`, with a local atomic JSON persistence adapter. This is a development adapter, not a migration of the PostgreSQL schema. It supports one server process per data file; do not share its file across processes. The file contains account preference data and should remain private.

Authentication fails closed by default. Production authentication and permission middleware are not implemented. For synthetic local testing only, explicitly provide a fixture account and the matching `X-Test-Account` header. The command refuses non-loopback listen addresses. Never use actual account data with this development authentication mode.

```sh
GOTOOLCHAIN=local GOCACHE="$PWD/.cache/go-build" \
  /workspace/shared/toolchains/go/bin/go run -buildvcs=false ./cmd/solsynth-go \
  --listen 127.0.0.1:8080 --data /tmp/solsynth-watch-demo.json \
  --fixture-account synthetic-test-account
```

Example fixture requests and the precise payload contract are covered by the HTTP contract tests. The remaining production gates include a verified identity adapter, real permission semantics, a PostgreSQL migration/rollback, concurrency across replicas, matching upstream error serialization, and existing-client tests.

## PostgreSQL adapter checkpoint

The command accepts `--storage postgres --postgres-dsn-env SOLSYNTH_POSTGRES_DSN`. The named environment variable supplies the connection string, which is never logged. Authentication still fails closed unless explicitly using the synthetic loopback fixture mode; PostgreSQL support does not create production authentication. Use canonical lowercase UUIDs for fixture account IDs in PostgreSQL mode.

The adapter targets the licensed `post_watch_preferences` schema, applies no DDL at startup, and supports transactionally merged preference patches across cooperating Go processes. The original nullable unique index can permit duplicate live rows; the adapter refuses them instead of selecting an arbitrary preference. Existing C# writers do not share its advisory-lock protocol, so mixed-writer deployment is unsupported.

Run the separate `scripts/integration-postgres.sh` only in a suitable disposable environment. Ordinary `go test ./...` skips actual database cases when no disposable test DSN is configured; a package-level `ok` does not mean PostgreSQL was tested. See the dedicated integration verification record for whether real tests ran. Exact Shared audit hooks, legacy concurrent writers and production auth remain explicit gaps.

## Actual CI validation and watcher boundary

The preference adapter's six required real PostgreSQL cases and post-restart persistence assertion passed without skips on PostgreSQL 16.15 in [the private validation run](https://github.com/momo-mnsjtxy/solsynth-go-staging/actions/runs/37625612233), at commit `f08a88900dd49c1c2c4ba174dd75578448c519e1`. Ordinary build/vet/race checks passed too. The earlier local socket limitation remains a property of the local environment, not the final database test outcome; the dated result is retained in `docs/ci-result-2026-10-07.json`.

Watcher and one-hop chain queries are a separate compatibility candidate. They are disabled in ordinary `watchpg.New(db)` construction and return an explicit policy error. Only the clearly named synthetic-compatibility constructor enables the fixed soft-delete-only policy for testing. No watcher HTTP endpoint or notification delivery is wired. The complete upstream global-filter policy remains unverified; passing candidate tests cannot establish production recipient or visibility parity. Authentication also remains fail-closed pending the contracts described in `internal/auth/HTTP_RECONSTRUCTION.md`.

## Excluded protocol repository

The unlicensed Solsynth protocol-definition repository is excluded from this work. Its `.proto`/schema files, descriptor exports and generated bindings must not be copied or used as an interface-only substitute. Existing code and synthetic SQL projections are grounded in the separately verified AGPL DysonNetwork/Solian callers, models and migrations. No excluded protocol definitions or bindings are included. Authentication and other missing contracts remain unresolved; the earlier suggestion to supply that repository’s protocol exports is withdrawn.

## Board PostgreSQL compatibility candidate

`internal/boardpg` adds real SQL persistence for the existing transport-independent board service, grounded in licensed DysonNetwork-owned migrations and its pinned current EF snapshot. Account-scoped transactions now include the read used to preserve app-owned payloads, validation and replacement/save; cache invalidation occurs after commit. JSON decoding preserves large numbers, and failed replacements must roll back instead of discarding retained tombstones or changing the source indexes.

Normal `boardpg.New(db)` construction is disabled. The isolated compatibility-test constructor explicitly selects a candidate active-row/audit policy because the excluded shared hooks remain unverified. No board HTTP endpoint, command wiring, authentication, permission adapter or permissive payload validator is added. Cooperating Go writers share the lock protocol; mixed legacy/Go writers are unsupported. The actual-PG workflow requires the five board cases alongside the previous ten cases and checks both preference and board state after a server restart.
