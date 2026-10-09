# Taxonomy backend continuation

## Delivered scope and acceptance boundary

This family is now a runnable Go domain, PostgreSQL adapter, HTTP handler and loopback command. It is no longer a collection of isolated validation helpers. Actual reads and writes run against an isolated disposable `taxonomy_candidate` schema. Production constructors remain closed. Every opt-in HTTP response includes `X-Solsynth-Compatibility: synthetic-taxonomy-projection; production-parity=unverified` and `Cache-Control: no-store`.

Three different kinds of evidence are deliberately separated:

1. **Observed client-facing behavior:** pinned authored controllers/services and licensed authored Solian callers establish the routes, source query predicates, counts/order, request members, mutation rules, subscription operations and the fields those callers use.
2. **Independently designed internals:** coherent read snapshots; bound SQL parameters; serialized duplicate/claim/quota checks; transactional soft deletion, subscription tombstones and link detachment; and a transactional local mutation audit ledger. These are real implemented behaviors, not claims about omitted shared hooks. They can be evolved without demanding bit-for-bit replication of unknown implementation details.
3. **Unsupported external contracts:** complete shared entity JSON (especially included owner/relationship objects), actual global authentication/permission transport, real publisher membership/default selection, remote quota entitlements/calculation, and external action-log transport. The implementation does not manufacture these contracts or claim production parity from its smaller output projection.

The current result is suitable for disposable integration and review. It is not a drop-in production Sphere service. No production endpoint, database, login, permission decision, migration, deployment or external write was used.

## Routes with substantive implementations

The following methods use actual repository reads/writes in explicit synthetic mode:

- `GET /api/posts/tags`, `GET /api/posts/categories`: slug ILIKE search, `X-Total`, source default name order, usage order, seven-day popularity order and source fallback created order.
- `GET /api/posts/tags/{slug}`, `GET /api/posts/categories/{slug}`: case-insensitive detail. Tag detail additionally trims the slug as its service does; category detail does not. Public detail does not invent a populated `Usage`; admin detail computes it.
- `POST /api/posts/tags`, `PATCH /api/posts/tags/{slug}`: normalized create, case-insensitive duplicate check, publisher ownership fixture, nullable updates, source length constraints and manager-only public edit.
- `POST /api/posts/tags/{slug}/claim`, `POST .../release`, `PATCH .../protect`: explicit claim permission, already-owned rejection, manager/owner checks, release clearing protection, real protected-tag usage counts, idempotent protection, and quota rejection.
- `GET /api/posts/tags/quota`, `GET /api/posts/tags/{slug}/quota`: owned-record SQL projection with protected-first/updated-descending order, actual used/remaining calculation and legacy slug-ignored alias. Entitlement total, level and perk level are explicit authority fixtures rather than fabricated remote-account results.
- `GET/POST /api/admin/tags`, `GET/PATCH/DELETE /api/admin/tags/{slug}`, `POST/DELETE .../{slug}/assign`, `PATCH .../{slug}/protect`, `PATCH .../{slug}/event`. Every old `/api/admin/posts/tags` alias is supported. Admin list supports slug/name/description ILIKE, owner/protected/event/unowned filters, clamp 1–200, nonnegative offset, usage/name/created/updated ordering. Event end must exist and be strictly future; disabling clears it.
- `GET/POST /api/admin/categories`, `GET/PATCH/DELETE /api/admin/categories/{slug}`: admin list/search/count/order; normalized category create/rename; duplicate/empty rejection; null-preserving and empty-string updates; transactional deletion.
- `POST /api/posts/{tags|categories}/{slug}/subscribe`, `POST .../unsubscribe`, `GET .../subscription`: real idempotent subscriptions, current-actor isolation, source error codes and source CreatedAtAction-style location.
- `GET /api/categories/subscriptions`: current-actor list/count, created-descending ordering and the source's unusual **Take(take), then Skip(offset)** window (`max(0, take-offset)` rows after offset). This is not silently replaced with the common Skip-then-Take pagination.

`ValidateTagUsage` and `IsTagAvailable` additionally implement the source service's event-expiry/protected-tag rules. The source null-publisher condition is retained and explicitly documented as **not an authorization boundary**.

## Output projection, not invented complete entity JSON

`TagProjection` contains `id`, `slug`, `name`, `description`, `owner_publisher_id`, `is_protected`, `is_event`, `event_ends_at`, and `usage`. The first eight have independent official published documentation. Pinned list actions and authored Solian detail UI independently establish usage access. `CategoryProjection` contains id/slug/name/usage, all read or assigned in the owned controllers and authored callers.

The quota record fields come directly from the service's local `Select(new ProtectedTagQuotaRecord { ... })`; the wrapper members come directly from its initializer. Lower-case/snake-case names, explicit JSON nulls and time formatting remain a local projection convention, not an inferred shared serializer specification. Subscription output is likewise an explicit id/account/category/tag/collection-id projection.

The full owner-publisher relationship is not emitted. The licensed authored Solian detail screen reads `ownerPublisher` and its name/nick/type/picture; those are concrete remaining display-contract requirements, not proof of a full model definition. Subscription-list relationship includes (`Tag`, `Category`, `Collection.Publisher`) are likewise not manufactured. Audit timestamps and unknown inherited or calculated entity fields are not represented as verified client JSON.

The three newly inspected authored Solian files import `solar_network_sdk`; their imports are evidence of dependency, not permission to inspect the excluded NeTo/Spec SDK or generated bindings. No such material was read. The published official guide is dated July 25, 2026 and lacks a verified rendered revision; its older broad admin policy cannot replace current granular controller attributes. A bounded re-open of the published guide on this pass returned a web-reader cache miss. The prior independently documented browser observations remain available; no repository/source/mirror fallback was used.

## Secure independent internals

- SQL identifiers are a fixed two-kind allowlist and fixed `taxonomy_candidate` namespace. Search strings, IDs, paging values, visibility values and times use bound parameters. Wildcard ILIKE behavior is retained intentionally.
- Public visibility is an explicit signed-int32 fixture value. The shared production enum is not guessed. Active-row predicates are explicit independent policy, consistent with the owned AppDatabase invoking `ApplySoftDeleteFilters`; the omitted shared hook implementation is not claimed to be replicated.
- Count and list share a repeatable-read transaction so rows and `X-Total` describe one database snapshot. This strengthens the original two-query race behavior.
- Table-level mutation locks serialize duplicate-slug checks despite the snapshot lacking a unique slug index. The same mechanism makes claim and protected quota changes atomic under concurrency. This simple local design favors correctness over maximum write concurrency and should be profiled before large deployment.
- Deletes atomically detach post links, tombstone matching subscriptions and tombstone the taxonomy entity. Unsubscribe tombstones its subscription. The owned AppDatabase invokes `ApplyAuditableAndSoftDelete`; precise retention/interception hooks are not copied. Unicode SQL comparison still depends on the database collation, just as the source SQL translation does. Tag-subscription tombstone cleanup is an explicit integrity improvement over the visible tag service, which does not itself enumerate them.
- A local `taxonomy_candidate.actions` ledger records every successful taxonomy create/mutation with fixture actor, admin flag, resource and operation in the **same transaction**. Ledger failure rolls back the mutation. It does not pretend to deliver external `RemoteActionLogService` events or spoof client IP metadata.
- No bearer/cookie/header principal is accepted. Credential-shaped headers/query parameters are refused in synthetic mode. Fixture authority is immutable after constructor copying, includes explicit actor/publisher/manager/grant data, and defaults to no actor or grants. It is never promoted to production authority.
- Request strings use UTF-16 code-unit limits to match the source's 128/256/4096 maximums. Null update members preserve current values; explicit empty strings update them. JSON body size is bounded and unknown members/invalid types are rejected. Blank normalized tag slugs are rejected as an explicit local validation improvement; the visible tag service itself does not contain the category controller’s explicit blank-slug check. The decoder's exact framework binding/error envelope is not claimed to match ASP.NET globally.
- The command binds only literal loopback IPs and uses environment-variable DSN indirection. It never accepts a DSN as a command argument, prints database secrets, creates the schema, or starts with a enabled production policy.

## Running on a supported disposable test host

First initialize only a disposable database with `internal/taxonomypg/testdata/schema.sql`. The main integration script owns that setup. Then build `./cmd/taxonomy` with Go 1.25+.

Anonymous projection reads can use:

```sh
taxonomy --listen 127.0.0.1:8083 \
  --postgres-dsn-env TAXONOMY_FIXTURE_DSN \
  --synthetic-public-visibility 42
```

Protected mutations additionally require `--synthetic-authority-file /path/to/local-fixture.json`. Example fixture data, all synthetic:

```json
{
  "actor_id": "10000000-0000-0000-0000-000000000001",
  "publisher_id": "10000000-0000-0000-0000-000000000002",
  "publisher_name": "fixture",
  "manager_publisher_ids": ["10000000-0000-0000-0000-000000000002"],
  "grants": ["PostsModerate", "PostCategoriesManage", "PostCategoriesSubscribe", "PostsTagsCreate", "PostsTagsUpdate", "PostsTagsClaim", "PostsTagsAssign", "PostsTagsProtect", "PostsTagsEvent", "PostsTagsDelete"],
  "protected_tag_total": 1,
  "level": 0,
  "perk_level": 0
}
```

The publisher row must exist in the fixture database for ownership writes. The grant names deliberately identify pinned C# permission constants, not an assumed serialized production permission-node scheme. Start without opt-in flags to get the closed handler.

## Verification and exact CI additions

Local execution passed:

- `go test -race ./ports/sphere/taxonomy ./internal/taxonomypg ./cmd/taxonomy`
- `go vet ./ports/sphere/taxonomy ./internal/taxonomypg ./cmd/taxonomy`
- `go test ./...` (database-dependent tests skipped without the disposable DSN)
- Exhaustive valid-Unicode-scalar .NET invariant-lower/trim comparison from a safe local standard-library oracle; Go's dotted-I and newer Unicode mappings are corrected. SDK 10.0.401 is observation evidence, not a claim about a deployed runtime or database collation.
- Hash verification for all four newly retained owned/authored source files: Git blob IDs match the pinned fetch result and SHA-256 digests are in `ports/sphere/taxonomy/source/manifest.json`.

No local PostgreSQL or HTTP socket startup was attempted, in accordance with the confirmed environment restriction. Recorder tests exercise SQL statements, argument binding, transactions, null scans, authority denial and rollback without a socket. They are not represented as real PostgreSQL execution.

The integration owner must add `internal/taxonomypg/testdata/schema.sql` to disposable schema setup, build `./cmd/taxonomy`, and require these ten real PostgreSQL tests in the pre-restart phase:

1. `TestPGTaxonomyListSearchOrder`
2. `TestPGTaxonomyAdminFiltersAndTombstones`
3. `TestPGTaxonomyCreateUpdateConstraints`
4. `TestPGTaxonomyOwnershipQuotaEvent`
5. `TestPGTaxonomyTransactionalAudit`
6. `TestPGTaxonomyConcurrentClaimAndQuota`
7. `TestPGTaxonomySubscriptionsAndDeleteRollback`
8. `TestPGTaxonomyConcurrentCreateAndSubscribe`
9. `TestPGTaxonomyPolicyErrorsAndReadOnly`
10. `TestPGTaxonomyRestartPersistence`

The post-restart phase must require `TestPGTaxonomyRestartPersistence` with `PG_TEST_PHASE=after`. This checks persisted ownership/protection/quota/subscription state. The tests intentionally reject any database that does not satisfy the existing disposable-cluster marker/DSN guard. Compilation and skip enumeration passed here; execution, restart and live HTTP are still pending CI, not passed by inference.

## Remaining decisions/contracts

1. Establish a reviewed production principal/permission adapter, including actor/session binding, scopes, restrictions, publisher membership and default-publisher resolution. Do not enable it merely because anonymous discovery or a key catalog works.
2. Agree the client-facing output contract for owner-publisher and included subscription relationships, inherited fields/null omission and framework errors. Known projections can be extended from independently eligible authored evidence; missing pieces must not come from prohibited schema/code sources.
3. Connect protected-tag entitlement totals to an approved authored calculation or verified service contract and test fallback/failure semantics. The current fixture total is explicit and does not masquerade as the shared calculator.
4. Decide whether local transactional audit is sufficient or requires delivery to the existing external action-log service. No wire format or delivery success is invented.
5. Verify disposable SQL/process/restart CI and review the independent soft-delete/locking/audit policy for the intended operational scale. These are implementation/acceptance steps, distinct from missing external contracts.

The implementation baseline is local archive commit `ac50657a6fa0fc16921ae44ca0ddedc46689f496`. Only taxonomy-specific paths were changed in the isolated implementation copy.
