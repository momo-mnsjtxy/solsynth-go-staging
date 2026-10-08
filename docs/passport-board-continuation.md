# Passport board runnable continuation

Date: 2026-10-08. Work tree initialized with an archive of
`ac50657a6fa0fc16921ae44ca0ddedc46689f496`.

## Result and run boundary

This continuation implements nine board HTTP operations backed by the existing
transactional SQL repository. It includes actual payload validation, actual
process-local grouped account-resolution caching, and an executable loopback host.
It has no successful fake persistence, permissive validation, or inferred identity
adapter. Production account/app authentication and consent/permission authority
remain mandatory integration dependencies. The command defaults to dependency
unavailable responses, without opening PostgreSQL.

To run solely against synthetic local data:

```sh
go build -buildvcs=false -o /tmp/passport-board ./cmd/passport-board
# Prepare the disposable database with internal/boardpg/testdata/schema.sql first.
# The host itself never creates/migrates tables.
/tmp/passport-board --listen 127.0.0.1:8084 \
  --synthetic-fixture cmd/passport-board/fixture.example.json \
  --postgres-dsn-env PG_TEST_DSN
```

`PG_TEST_DSN` must address a Unix socket or a literal loopback IP. Fallback hosts
are checked too. The command does not log connection strings or inspect saved
credentials. Listen addresses must be literal loopback IPs. Synthetic accounts,
app capabilities, consents, and principal grants come from the explicitly
selected fixture. Requests identify a fixture principal using
`X-Synthetic-Principal: owner`, `admin`, or `app` in the example. Real Bearer or
X-App-Secret credentials are deliberately not treated as synthetic principals.
A fixture is not a new authentication format for production.

## Implemented operations

| Method/path | Behavior and guard |
|---|---|
| GET /api/accounts/me/board | Current account, accounts.profile.board.manage, ordered list |
| PUT /api/accounts/me/board | Same grant; replace; service cache invalidation and additional self account purge |
| GET /api/accounts/{name}/board | Explicit public-route policy; account resolution; ordered list |
| GET /api/admin/accounts/{identifier}/board | accounts.view; account ID or name resolution |
| PUT /api/admin/accounts/{identifier}/board | accounts.board.manage; replace |
| POST /api/admin/accounts/{identifier}/board/items/{item}/payload | accounts.board.manage; prebuilt envelope or custom-app schema validation; payload-only write |
| DELETE /api/admin/accounts/{identifier}/board/items/{item} | accounts.board.manage; account-scoped physical deletion; no invented cache purge |
| GET /api/private/apps/{app}/board/widgets | Path app identity and declared board capability; only that app's manifests |
| POST /api/private/apps/{app}/board/payload | Path app identity, capability and target account consent; manifest/envelope validation, installed-item ownership checks, payload-only write |

The private routes normally live in Develop. This isolated combined host
implements their owned controller orchestration directly around the Passport
service; it does not pretend to implement an unsupported gRPC wire transport.
Existing gateway prefixes `/passport` and `/develop` are not remapped by this
standalone server. A later gateway integration must route appropriately.

HTTP binding preserves default order/kind zero, enabled true, nullable/omitted
payload as an empty object, nullable IDs, signed 32-bit order, MaxLength(256)
layout keys and the private controller's MaxLength(128) widget key. GUID D/N/B/P
textual forms become canonical lowercase strings; .NET's X form is not supported.
Invalid item/app route GUIDs return 404. Unknown methods return 405. The local
body bound defaults to 1 MiB. Unknown request properties are ignored as in the
source DTO binding; malformed values, null nonnullable layout scalars, multiple
JSON roots, and scalar/array payloads are rejected. JSON decoding uses UseNumber.

Self/admin/app response items retain the authored Solian client's integer kind
and fields. They are a client-consumed projection, not a claim to reproduce every
legacy entity metadata field. Case-insensitive widget comparisons use Go Unicode folding, not verified .NET
OrdinalIgnoreCase equivalence for every Unicode code point. Framework validation
details, trace IDs, error
formatting and status precedence are not byte-for-byte ASP.NET emulation.
Known source controller error codes are retained for board validation/not-found
branches. Unexpected SQL/cache failures return a generic 500 without private
details; missing authority/dependencies return 503, missing identity 401, denied
grants 403. A cache failure after commit does not roll back persisted data.

## Evidence, including conflicts resolved explicitly

`ports/passport/board/source/manifest.json` pins 17 retained owned source/license
files by repository revision, byte count and SHA-256. Its package test validates
every retained entry. Source inventories elsewhere in the baseline are unchanged.

Eligible repositories:

- Solsynth/DysonNetwork at 4ee2d0cebb9da8284a92483fee6dce795bf6947c, AGPL-3.0
- Solsynth/Solian at ffd0838e5f04cb40bea311a39118ebeedad210c1, AGPL-3.0

The research read complete owned AccountBoardService, self/public/admin
controllers, Develop CustomAppController and CustomAppService, both board
migrations and the current complete Passport EF snapshot. It also read the
complete authored Solian board models/parser/callers and dashboard screen, plus
ACCOUNT_BOARD, ACCOUNT_ADMIN_API and CUSTOM_APP_BOARD_PRIVATE_API documentation.
The standalone dashboard source is not evidence that unrelated dashboard widgets
use the account-board endpoint. The authored board provider/parser is the direct
client contract. No generated Solian SDK or excluded shared-model implementation
was inspected or copied. An inline protocol schema example in ACCOUNT_BOARD was
omitted from the retained documentation copy and is not used by this work; that
modification is marked in the manifest. No schemas, descriptors or generated
bindings from the excluded protocol repository are used.

Newly established facts beyond the old excerpt audit:

- The app-private HTTP route is concrete and authored: app-secret identity,
  declared accounts.profile.board capability, per-account app consent,
  manifest validation, and installed-item update.
- The owned documentation states exact scope strings: self
  accounts.profile.board.manage; app capability/consent accounts.profile.board;
  admin read accounts.view; admin mutations accounts.board.manage. Knowing these
  strings does not establish global permission evaluation or token freshness.
- The universal documented field envelope requires value and nonempty label,
  with optional format. Nested JSON values are allowed; empty app payload clears
  the configured state and bypasses required-field/content validation.
- ACCOUNT_BOARD contains conflicting broad claims: it says prebuilt layout
  writes enforce the envelope, then its detailed section says the server leaves
  prebuilt payload structure to clients. The actual complete service only clears
  app fields for prebuilt layouts. The implementation follows executable source,
  preserving raw prebuilt client payloads for PUT. Admin/app payload pushes
  enforce the envelope.
- Documentation examples use string kinds; authored Solian sends/reads integer
  indexes. HTTP uses integer kinds from this direct client contract.

## Authored independent policies, not missing internal-hook placeholders

The following are working, explicit choices rather than guessed old internals:

1. `board.DocumentedEnvelope` requires every field to be an object with present
   value, nonblank string label, and optional string format. It rejects extra
   envelope properties. It deep-copies values and preserves JSON numbers and
   nulls. It does not invent reserved image/background exemptions or transforms.
2. `ValidateManifestPayload` implements the owned validation sequence: enabled
   widget, object payload type, empty-payload exception, byte limit, required
   fields, envelope, declared nested value types. Null is accepted for every
   declared type, matching the owned matcher. Unrecognized declared types are
   left unconstrained by the source matcher. FieldTypes.required is metadata;
   RequiredFields drives presence checks in the actual owned service.
3. Payload byte limits use Go's serialized UTF-8 JSON size, not a claim of exact
   System.Text.Json escaping. This is a documented independent size policy.
4. `boardpg.NewWithIndependentPolicy` selects active rows by deleted_at IS NULL,
   preserves supplied IDs and active-row creation timestamps, stamps updates in
   UTC, and serializes cooperating writers by an account-scoped advisory lock.
   Read, validation, retained app payload selection and mutation occur in one
   transaction. Validation/SQL failures roll back; cache invalidation follows
   commit. The original unfiltered unique indexes remain intact. Tombstones can
   cause a safe replacement failure and are never discarded to make a write fit.
5. Admin delete physically removes exactly one active account-owned row under
   the same lock. The legacy shared Remove/SaveChanges lifecycle is not claimed.
6. `board.GroupCache` actually stores grouped account resolutions, synchronizes
   concurrent access and invalidates the account group. The self controller
   performs its additional purge. Admin delete intentionally keeps the source
   controller's lack of purge. This local cache is not a distributed hydration
   backend, and does not cache board lists.
7. Fixture account/name resolution and widget manifests are immutable explicit
   configuration snapshots. They are not fake successful responses from an
   unavailable remote authority. Each configured principal's grants are isolated,
   even when two principals name the same account. App identities cannot inherit
   account grants. Missing capability/consent fails closed.

The old `boardpg.New` remains disabled to avoid silently changing earlier callers.
The explicit independent-policy constructor is runnable and does not depend on
obtaining excluded hooks. The earlier compatibility constructor remains available
for old tests. No mixed legacy/Go writer guarantee is claimed.

## Verification

Local checks performed without opening HTTP/PostgreSQL sockets:

- Existing board service/repository unit tests remain passing
- Ten new board tests exercise recorder HTTP operations, default denial, DTO
  defaults, ownership and app payload preservation, authorization/grant isolation,
  malformed payload rejection, real grouped cache invalidation, source integrity,
  body limits, SQL-error redaction and concurrent cache access
- The command example config is parsed and validated in its test
- `go test -race ./ports/passport/board ./internal/boardpg ./cmd/passport-board`
  passes; `go vet` for those packages and tests/integration passes.
  The command builds with `-buildvcs=false` for this archive-only workspace
- Actual database/process tests compile and skip locally because the disposable
  DSN is absent. No socket-denial retry was made. These are not recorded as live
  database/process passes.

Integration-owner CI contract:

```sh
go build -buildvcs=false -o "$run/passport-board" ./cmd/passport-board
export PASSPORT_BOARD_BINARY="$run/passport-board"
```

Use the existing marked disposable cluster, original board schema, and
PG_TEST_REQUIRED=1. Before-phase required tests:

- TestPGBoardHTTPFamily: self/app/admin operations, independent pool observation,
  payload ownership, high-precision JSON, consent rejection, deletion isolation
- TestPGBoardHTTPValidationRollback: schema rejection and retained-tombstone
  replacement failure roll back the real database
- TestPGBoardHTTPProcessRestart: actual compiled disabled host returns 503;
  explicit synthetic host persists through real HTTP and application restart

After PostgreSQL itself restarts:

- TestPGBoardHTTPRestartPersistence: public HTTP read verifies the persisted
  app-owned payload and deletion state from the before phase

Process integration requires PASSPORT_BOARD_BINARY when PG_TEST_REQUIRED=1,
so omission cannot turn a mandatory process check into a silent skip. The process
fixture uses only loopback HTTP and the runner's validated disposable DSN.
Global integration scripts, source manifests and publication files are owned by
the integration coordinator and were not edited by this worker.

## Remaining external contracts

These are real integration gaps, separate from the implemented policies above:

- Verified authenticated-account identity and freshness, authoritative permission
  decisions, and global/public endpoint policy must implement `board.Authority`.
- Real app API-secret validation including non-OIDC secret type/expiry/revocation,
  and current per-account authorized-app consent must back AuthorizeApp.
  Knowing scope strings alone is insufficient authorization.
- Production account lookup must implement AccountResolver. Configured synthetic
  aliases are not live account state or an identity assertion.
- A live widget catalog must load current app capability/manifests and apply the
  selected validation contract. Synthetic snapshot catalogs do not discover new
  app definitions or react to remote revocations.
- Existing gRPC compatibility is unsupported; no wire descriptors are guessed.
  This host's explicit in-process orchestration is not an upstream RPC adapter.
- Exact full legacy item metadata/error projection and an actual existing Solian
  end-to-end run remain unverified. Current recorder/SQL tests cover the
  source-backed client-consumed projection, not complete platform parity.
- Multi-replica account hydration, mixed legacy writers, deployments, rollout
  and data migration/rollback remain integration work. No production request,
  credential, remote write, publication, deployment or account was used.
