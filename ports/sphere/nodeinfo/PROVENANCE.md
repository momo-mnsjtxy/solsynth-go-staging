# Sphere NodeInfo compatibility candidate

Source-informed work derived from Solsynth/DysonNetwork revision
`4ee2d0cebb9da8284a92483fee6dce795bf6947c`, modified 2026-10-08, under the
retained AGPL-3.0 license. This is not a clean-room implementation or a proven
production-compatible replacement.

`source/manifest.json` retains the exact repository paths, pinned GitHub URLs,
Git blob identifiers, SHA-256 hashes and license evidence for these unmodified
snapshots:

- Sphere-owned `ActivityPub/NodeInfoController.cs`: three GET route templates,
  both anonymous response DTOs, configuration defaults and count predicates
- Sphere-owned `Startup/ServiceCollectionExtensions.cs`: controller
  `JsonNamingPolicy.SnakeCaseLower` at lines 65–76
- Sphere-owned `AppDatabase.cs`: publisher/post DbSets, snake-case PostgreSQL
  naming and the call to Shared `ApplySoftDeleteFilters` at line 337
- Sphere-owned `Migrations/AppDatabaseModelSnapshot.cs`: `posts` mapping at
  lines 222–418 and `publishers` mapping at lines 1014–1208
- Root `LICENSE.txt`, copied byte-for-byte from the already retained pinned
  DysonNetwork license and verified against its existing blob/SHA-256 evidence

No Shared enum definition, unlicensed schema/protocol repository, `.proto`,
descriptor export or generated binding was fetched or used. The licensed
Sphere-owned migration snapshot is the SQL-column evidence; references to Shared
type names in that snapshot do not establish Shared enum values or global-filter
implementation. The Fediverse numeric enum remains a mandatory caller-supplied
synthetic fixture. Source/license integrity is checked by a package test and the
repository source-evidence gate; integrity checks alone are not a license audit.

## Preserved source details

The three source paths are `GET /.well-known/nodeinfo`,
`GET /.well-known/nodeinfo/2.0` and `GET /.well-known/nodeinfo/2.1`.
Discovery advertises `https://{domain}/nodeinfo/2.1` followed by `/nodeinfo/2.0`.
Those hrefs omit `/.well-known`. The candidate preserves this source discrepancy
and does not silently add alias routes. Missing domain configuration falls back
to `localhost`; explicitly empty domain configuration does not.

Snake-case JSON is used even when conventional NodeInfo consumers may expect
camelCase. Version 2.0 has no top-level `version` or `metadata`, but includes
`configuration.urls: {}`. Version 2.1 includes `version: "2.1"` and
`metadata: {}`, but omits `configuration.urls`. Empty inbound services serialize
as `[]`, not `null`. All source-owned constants and fields are covered by full
source-derived expected-document tests, not captured .NET responses.

Counts execute in the source order, without an added transaction or cache:

1. All publishers under the chosen candidate soft-delete policy
2. Posts where `publisher_id IS NOT NULL`, under that same policy
3. Publishers where `type <> supplied_fediverse_type AND uri IS NOT NULL`, under
   that same policy

The third value populates both `active_month` and `active_halfyear`. There is no
activity-time predicate, and empty-string URI is not null. It deliberately counts
non-Fediverse types, despite the misleading upstream variable names. Post counts
do not join publishers or exclude remote, unlisted, draft or moderated posts
with extra predicates. The retained current snapshot declares `publisher_id`
non-nullable; that nullability is preserved in the fixture instead of inventing
a nullable real-schema test. The source's redundant non-null predicate remains
in SQL. Sequential counts can observe concurrent changes between queries.

## Explicit compatibility boundaries

Normal repository and HTTP constructors deny before querying. The host also
denies by default without reading a DSN or opening PostgreSQL. Enabling isolated
synthetic operation requires all of: an explicit signed int32 Fediverse fixture,
the explicit `deleted-at-is-null` candidate soft-delete policy, and a DSN
environment-variable name. The host binds only a literal loopback address.

The following are candidates, not verified .NET parity: exclusion of
`deleted_at` rows; identity-header rejection; exact-path/case/trailing-slash
routing; 404/405/403/503 choices; HEAD rejection with no body; `Allow: GET`;
JSON error codes; Content-Type and HTTP formatting; cancellation timing; invalid
count rejection. The source clearly supplies GET templates and response data,
but full ASP.NET routing, middleware, error serialization, authorization and
transport behavior have not been observed. Authentication, production mounting,
deployment, migration and rollback are not implemented by this slice.

No DDL is executed by the host or repository. SQL counts return actual database
errors; failures never become a successful zero or partial result. Results are
scanned into signed int32 values, so overflow errors instead of truncating. An
actual multi-billion-row overflow fixture has not been run.

## Verification

- `ports/sphere/nodeinfo`: source payloads, link order/fallback, immutable domain
  configuration, exact field differences, empty arrays/objects, int32 boundary,
  fresh result slices, fail-closed constructors, routing/method/identity guards,
  cancellation, error sanitation and source/license integrity
- `internal/nodeinfopg`: disabled zero/default construction, required explicit
  candidate policy, caller-supplied enum preservation, nil database and canceled
  context; no successful SQL test doubles
- `cmd/nodeinfo`: loopback validation, explicit opt-ins, DSN environment naming,
  no default database access and startup failure redaction/cleanup
- `tests/integration/nodeinfo_test.go`: four actual PostgreSQL cases cover count
  and HTTP shapes, read-only execution, fail-closed policy, each real query
  failure, in-flight lock cancellation, and before/after restart persistence

The fixture `internal/nodeinfopg/testdata/schema.sql` creates a dedicated
`nodeinfo_candidate` schema so aggregates are not polluted by watcher tests.
It is a minimal test projection, not the full source schema; omitted production
foreign keys permit a deliberate missing-publisher fixture to verify no join.
Integration pools set `search_path=nodeinfo_candidate` on every connection.
Actual PG execution and real-process HTTP smoke results must be reported by the
integration runner. Local compilation or skipped integration cases are not a
successful database or live HTTP test. No .NET oracle or production probe ran.

## Host-meta continuation

`GET /.well-known/host-meta` is derived from the same licensed pinned repository's
`DysonNetwork.Sphere/ActivityPub/WebFingerController.cs`, retained and verified in
`inventory/operation-sources/manifest.json`. The response uses only the configured
ActivityPub domain, missing-value localhost fallback, and fixed XRD link template.
Explicit empty domain stays empty. It shares the candidate handler's default
deny, request-identity rejection and routing/method caveats. No database, account
lookup, WebFinger lookup or external request is manufactured for this response.
Escaping XML attribute metacharacters is an intentional improvement over the
source's direct interpolation; malformed configuration is not promised identical
byte output. Tests check ordinary exact output, empty/missing configuration,
configuration copy isolation, gating and valid XML encoding. This addition has
no independent .NET or actual process execution result yet.
