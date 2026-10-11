New additive feature: [virtual pets](docs/virtual-pets.md) documents the care/growth API, pet-only earned currency, explicit schema installation, and validation limits. It is separate from the historical parity scope below.

Current all-drafts integration: see [completion scope and exact validation status](docs/all-drafts-completion-scope.json), [owned backend checkpoint](docs/owned-backend-checkpoint.json), and [authenticated host](docs/authenticated-backend-host.md). The sections below retain historical checkpoints; their test counts and CI claims do not certify the current commit.

The reviewed completion scope excludes external payment channels, migration/deployment, and legacy compatibility. Internal wallet and funded rewards remain included. Configured software adapters are implemented; operator configuration, external service and NFC hardware activation, client adaptation, and the exact candidate's private PostgreSQL/restart CI are separate acceptance requirements. No production deployment or completed-platform claim follows from ordinary tests.

# Solsynth Go migration workspace

**Historical initial status: local domain ports and migration evidence. Consult the current completion scope above for later implementation and validation.**

The requested target is backend functional parity while retaining the existing frontend clients. The organization contains multiple products and already-migrated Go services. Replacing clients or rebuilding existing Go services is not part of this initial stage.

## Implemented so far

The three initial policy packages below derive from the licensed DysonNetwork revision `4ee2d0cebb9da8284a92483fee6dce795bf6947c`, with source snapshots, licenses, and per-package provenance retained:

- `ports/passport/username`: account username normalization and candidate generation with injected lookup, randomness, and clock
- `ports/sphere`: deterministic timeline ranking and selection, separated from database, identity, visibility, and moderation signal resolution
- `ports/messager`: room-sync exact-sequence and inclusive-range recovery selection
- `ports/passport/board`: partial transport-independent board service using pinned licensed client/server contracts, with mandatory validation/cache/repository interfaces

The policy and board packages are Go library functions with synthetic contract tests. A loopback-only development HTTP service for post-watch preferences is implemented and tested with an atomic local JSON persistence adapter. A transport-independent Passport board service is also included with explicit unresolved production adapters. No production HTTP service, upstream database migration, authentication implementation, or deployment has been completed. See `docs/parity-matrix.json` and package parity inventories for the remaining work and evidence.

## Test locally

Use the real Go 1.26.4 compiler and the pinned Python parser dependencies. Download and verify Go modules before the offline verification script.

```sh
GOPATH=/workspace/shared/go GOMODCACHE=/workspace/shared/go/pkg/mod \
  GO_BIN=/workspace/shared/toolchains/go/bin/go GOCACHE="$PWD/.cache/go-build" bash ./scripts/verify.sh
```

The initial policy ports use the standard library; the PostgreSQL command now uses pgx v5.10.0 with dependencies pinned in go.mod/go.sum. Run `go mod download` once in a network-enabled development environment before the offline verification script. Tests are synthetic and do not contact Solsynth production or use personal data. Passing them establishes only the cases checked. Independent .NET differential oracles cover the recovered exchange catalog and badge handlers only. No full existing-client end-to-end test has run.

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

Run `scripts/integration-postgres.sh` only through the authorized private GitHub Actions workflow with its disposable database environment. Do not run it locally or spoof its CI guards. Ordinary `go test ./...` skips actual database cases when no disposable test DSN is configured; a package-level `ok` does not mean PostgreSQL was tested. Check all four jobs and both groups' after-restart evidence for the exact candidate SHA. This paragraph's older adapter checkpoint does not describe the current owned authority implementation.

## Actual CI validation and watcher boundary

The preference adapter's six required real PostgreSQL cases and post-restart persistence assertion passed without skips on PostgreSQL 16.15 in [the private validation run](https://github.com/momo-mnsjtxy/solsynth-go-staging/actions/runs/37625612233), at commit `f08a88900dd49c1c2c4ba174dd75578448c519e1`. Ordinary build/vet/race checks passed too. The earlier local socket limitation remains a property of the local environment, not the final database test outcome; the dated result is retained in `docs/ci-result-2026-10-07.json`.

Watcher and one-hop chain queries are a separate compatibility candidate. They are disabled in ordinary `watchpg.New(db)` construction and return an explicit policy error. Only the clearly named synthetic-compatibility constructor enables the fixed soft-delete-only policy for testing. No watcher HTTP endpoint or notification delivery is wired. The complete upstream global-filter policy remains unverified; passing candidate tests cannot establish production recipient or visibility parity. Authentication also remains fail-closed pending the contracts described in `internal/auth/HTTP_RECONSTRUCTION.md`.

## Excluded protocol repository

The unlicensed Solsynth protocol-definition repository is excluded from this work. Its `.proto`/schema files, descriptor exports and generated bindings must not be copied or used as an interface-only substitute. Existing code and synthetic SQL projections are grounded in the separately verified AGPL DysonNetwork/Solian callers, models and migrations. No excluded protocol definitions or bindings are included. Authentication and other missing contracts remain unresolved; the earlier suggestion to supply that repository’s protocol exports is withdrawn.

## Board PostgreSQL compatibility candidate

`internal/boardpg` adds real SQL persistence for the existing transport-independent board service, grounded in licensed DysonNetwork-owned migrations and its pinned current EF snapshot. Account-scoped transactions now include the read used to preserve app-owned payloads, validation and replacement/save; cache invalidation occurs after commit. JSON decoding preserves large numbers, and failed replacements must roll back instead of discarding retained tombstones or changing the source indexes.

Normal `boardpg.New(db)` construction is disabled. The isolated compatibility-test constructor explicitly selects a candidate active-row/audit policy because the excluded shared hooks remain unverified. No board HTTP endpoint, command wiring, authentication, permission adapter or permissive payload validator is added. Cooperating Go writers share the lock protocol; mixed legacy/Go writers are unsupported. The actual-PG workflow requires the five board cases alongside the previous ten cases and checks both preference and board state after a server restart.

## Additional bounded policies

Five further executable policies now cover message heading formatting, unread
aggregation, placeholder state transitions, notification body construction and
publisher/realm discovery selection. They use the retained licensed snapshots and
explicit pre-resolved inputs; they add no HTTP routes or production adapters.
Their tests exercise real computations, including time, Unicode, state-transition
and selection-order boundaries. `scripts/verify.sh` now verifies 436 retained
source/license/oracle evidence entries against pinned expectations, with eleven
mutation-rejection checks and a missing-file rejection check. See
`docs/policy-followup.md` and the per-module provenance files.

## Develop discovery: real SQL and HTTP compatibility slice

`cmd/develop-discovery` serves the candidate `GET /api/apps` projection and,
with independent opt-in described below, the miniapp package-download candidate.
Its default mode returns 503 without opening a database. The enabled synthetic
mode reads PostgreSQL persistently; it has no success-data fallback or RPC stubs.
Startup never creates tables or modifies data. The source projection is six fields
with nullable description and X-Total; search/paging and related counts execute in
real SQL. No app creation/update endpoint is claimed.

Enable only against disposable synthetic data, explicitly selecting the fixture's
status number (42 is used in tests, **not an upstream Production mapping**):

```sh
go run ./cmd/develop-discovery --listen 127.0.0.1:8081   --synthetic-production-status=42 --postgres-dsn-env=PG_TEST_DSN
```

`internal/developpg/testdata/schema.sql` is a minimal test projection, not a full
schema or migration. The complete supported verification command is
`scripts/integration-postgres.sh` in an environment with Go and PostgreSQL 16.
It creates a marked private-socket database, inserts synthetic rows through
integration tests, requires thirty database cases and six restart
assertions, and launches real HTTP processes before/after the database restart.
The smoke script also restarts the application and checks default-deny behavior.

Production status mapping, shared global filters, pagination middleware and global
auth behavior remain unresolved. Synthetic soft-delete filtering, local pagination
bounds and stronger count/page snapshot consistency are explicit candidates, not
complete parity. See `ports/develop/PROVENANCE.md` for evidence and differences.
The original PostWatch authorization boundary is retained while the host also
routes the recovered catalog/badge families.


## Local recovery integration, 2026-10-08

Latest main `fcb3449501070745bbce4a0a3ba43a4df9ccf9bc` and 187 locally verified recovery blobs were reconciled. Newer policies, Develop discovery and source-integrity checks are preserved. Recovery-only catalogs, badges, tests and provenance are retained. This combined tree has not been published or validated by CI.

## Local anonymous exchange-catalog endpoint

`ports/wallet/exchangecatalog` implements the documented anonymous `GET /api/wallets/exchange` operation from the pinned, licensed Wallet controller. It reads configured exchange options; it does not execute exchanges or move funds. Source parsing, ordered case-insensitive configuration providers, decimal precision/rounding/scale, complete-response configuration errors and MVC method/path behavior are covered by an independently executed .NET 10 reference suite. The reference imports no excluded Shared/protocol assembly.

The local host accepts repeatable `--exchange-config` UTF-8 JSON files in low-to-high precedence order. Configuration is an explicit startup snapshot, not a claim to reproduce ASP.NET's complete host, environment-provider or hot-reload lifecycle. With no configuration the catalog is `[]`, matching the checked-in default. `--storage none` lets this stateless endpoint run while the persistence-backed watch endpoint remains unavailable. The host still binds loopback only, and the existing authenticated watch route remains fail-closed.

```sh
go run -buildvcs=false ./cmd/solsynth-go --storage none \
  --exchange-config appsettings.json --exchange-config appsettings.local.json
```

The local oracle records 1,793 configuration/decimal fixtures and six actual MVC method/path cases; its independently collected scalar-casing map contains 1,475 mappings. See `tests/contract/exchange-oracle/validation.json` and its reproduction instructions for exact versions, hashes and boundaries. The recovered addition is locally integrated with the latest main; remote publication and combined CI remain pending. It does not establish whole-platform parity or implement the separate protected funds-exchange POST operation.

## Local badge discovery family

The anonymous badge manifest and SVG routes are integrated through `--badge-icons`, whose default is the upstream `./Resources/BadgeIcons`. To use the included art, pass `--badge-icons ./ports/passport/badgediscovery/assets`. A missing directory keeps the manifest available without icon URLs; configured files are read on each request. The host remains loopback-only and does not grant access to protected watch operations.

The 23 badge definitions preserve the licensed controller's order and fields. Twelve included icons have verified Google Material Symbols Apache-2.0 provenance; `whatshot.svg` is newly authored replacement artwork with an intentionally different appearance. The unverified upstream image is excluded. See the asset provenance and original-art notice in the package.

File access deliberately uses safe directory containment instead of reproducing the upstream lexical path-prefix check. This is documented hardening, not a claim that every unsafe path has identical behavior. This local family does not implement badge-awarding rules, progression state, or account-badge persistence.

## Evidence chronology

`docs/local-final-checkpoint.json`, `docs/recovery-checkpoint.md`, and recovered
`*-live-smoke-result.json` files are unchanged historical observations from the
older recovery workspace. Their old remote head, absence of a local Git repository,
and local socket failures do not describe this continuation tree or negate later
CI passes. `docs/recovery-input-blobs.json` pins that input byte-for-byte.
The main baseline's CI result applies only to its exact commit; local combined
build/vet/unit/race results and pending combined PostgreSQL/HTTP process checks
are tracked separately in the continuation report.

## 2026-10-08 runnable continuation candidates

The loopback Develop host now also supports `GET /api/miniapps/{slug}/package`
behind independent `--synthetic-miniapp-stage` and `--synthetic-package-directory`
flags, together with the existing explicit discovery status/DSN flags. It performs
real SQL lookup and an atomic counter increment before returning a redirect or
reading a confined synthetic local ZIP. It never follows remote redirect targets.
The local file adapter is not a replacement for Minio/S3. Counter-before-storage
failure ordering is preserved; no fake successful download or retry is returned.
See `ports/develop/packages-PROVENANCE.md` for established behavior and differences.
Public product/entity and bot-command responses remain blocked by incomplete
lawful entity JSON evidence; no guessed serializers were added.

`cmd/nodeinfo` serves the three source-declared NodeInfo discovery/statistics
routes with real read-only PostgreSQL counts, an explicit synthetic Fediverse type
and explicit soft-delete candidate policy. Default construction remains disabled.
The source's snake_case payloads, 2.0/2.1 differences and unusual active-publisher
predicate are retained, rather than silently substituted with standard assumptions.
Its advertised `/nodeinfo/*` links differ from controller `/.well-known/nodeinfo/*`
paths; no unverified gateway alias is invented. See `ports/sphere/nodeinfo/PROVENANCE.md`.

Both families are runnable development candidates, not completed production APIs.
The combined thirteen local route operations do not measure whole-platform completion.
The integration runner now requires 30 non-skipped actual PostgreSQL cases, six
post-restart assertions, and separate compiled-process HTTP checks. Those new
checks have not run on this combined tree: local sockets are disallowed, and
private CI awaits publication approval. Ordinary package `ok` is not a DB pass.

Independent review corrected two recovered compatibility defects: comma-only
JSON containers are rejected, and badge ASCII route literals no longer accept
Unicode simple-fold substitutes that MVC rejects. Both fixes match a newly
executed .NET regression oracle, separate from the unchanged original fixtures.
The original exchange and badge oracles were also independently reproduced.

See `docs/operation-coverage-plan.md` and its machine-readable JSON for remaining
operations, dependencies and completion gates; `docs/identity-resolution-audit.md`
records the precise unresolved production contract rather than treating all
existing authentication documentation as missing.

The same explicitly gated Sphere host also serves source-backed
`GET /.well-known/host-meta` from configured domain only, without querying storage.
It preserves the missing-versus-empty domain distinction and XRD template. XML
attribute escaping is a documented improvement over direct interpolation.
The actual WebFinger account lookup is not implemented by this config-only route.

## Expanded licensed-client/document continuation

The independently verified evidence now includes all 23 authored Solian auth
Dart files plus relevant native clients/models, the complete 46-file owned Blade
Go implementation, and targeted authored product/bot consumers. Those establish
more usable HTTP and data contracts. They do not turn every client-consumed
projection into a complete server serializer or resolve all current permissions.
Socommon remains unaccessed under the existing source-permission boundary.

`cmd/develop-discovery --synthetic-bot-commands` adds
`GET /api/bots/{botId}/commands` after the existing discovery/DSN opt-in. It reads
actual PostgreSQL command configuration, preserves missing-config empty results
without writing defaults, and keeps unknown storage conventions explicitly gated.
The known DTO comes from owned BOT_CHAT_API documentation, corroborated by a
19-case independently authored documented-model fixture. That fixture is not an
oracle for the excluded original shared model or Npgsql mapping.

The PostWatch/catalog host now supports the source-documented wallet-product
catalog through independent `--synthetic-wallet-product-catalog` opt-in and
repeatable `--wallet-product-config` files. Currency must be explicit unless a
caller deliberately supplies `--synthetic-wallet-product-default-currency`.
Its configured output matches 383 reachable .NET binder/projection cases,
including decimals, provider precedence, nulls, ordering and duplicate names.
The reference has five additional direct-object diagnostics outside this Go
configuration-only API. The command never executes checkout or payment calls.
See `ports/wallet/productcatalog/PROVENANCE.md` and the contract review for the
unknown no-configuration currency constant and source/docs dictionary-casing drift.

Run `scripts/verify-dotnet-oracles.sh` to reproduce the bounded references with
certificate generation disabled before every .NET process. It uses disposable
tool homes and package-source-free NuGet configs. Retained source repositories
under inventory are read-only evidence, not imported replacement dependencies.

The machine-readable operation plan uses a compact integrity-checked index and
controller-level parts to keep publication requests bounded. Reassembly via
`inventory/operation-sources/coverage_format.py` reproduces the original complete
818-record canonical JSON byte-for-byte; no coverage fields were removed.

Publication safety review omitted five Blade deployment configuration snapshots
containing credential-shaped examples or sensitive gateway settings. Their
reference metadata remains, while all owned Go source/tests are preserved. No
credential values or certificate stores are included in the published tree.
