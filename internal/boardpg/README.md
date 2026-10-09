# PostgreSQL board persistence: isolated compatibility candidate

Modified 2026-10-07. GNU AGPL v3; source license retained in
ports/passport/board/LICENSE.txt. No excluded protocol definitions, descriptor
sets or generated bindings are used.

## Default-disabled policy

New(db) rejects all operations with ErrPolicyUnverified. Only the explicitly
named NewForCompatibilityTests(db, clock) enables this isolated candidate. There
is no command/HTTP wiring. This adapter is not production-ready.

The owned current EF snapshot confirms schema, but the separate excluded shared
helpers ApplySoftDeleteFilters and ApplyAuditableAndSoftDelete remain unknown.
The candidate therefore explicitly assumes:

- Active rows are exactly deleted_at IS NULL
- Replacement physically deletes active rows of the selected account only
- Retained active IDs keep created_at; new IDs get the injected clock time
- Writes set updated_at to that clock; inserted rows have deleted_at NULL
- Existing tombstones remain unchanged

Those are test policy choices, not a claim of complete upstream EF behavior.
The clock defaults to time.Now and all written times are UTC.

## Verified schema and source evidence

Pinned DysonNetwork revision: 4ee2d0cebb9da8284a92483fee6dce795bf6947c.

- [Initial board migration](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Migrations/20260708150813_AddAccountBoardItems.cs)
- [Widget-key migration](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Migrations/20260708152943_AddCustomAppWidgetKeyToAccountBoardItems.cs)
- [Current owned EF snapshot](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Migrations/AppDatabaseModelSnapshot.cs)
- [Physical replacement call](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountBoardService.cs#L93-L109)
- [Unknown shared filter/audit hooks](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/AppDatabase.cs#L258-L276)

The current snapshot's board entity still matches the two migrations: twelve
columns, UUID primary key, unique (account_id, order), and nonunique
(account_id, custom_app_id, custom_app_widget_key). There is no partial-index
predicate or database column default shown. The SQL fixture in testdata/schema.sql
is for isolated tests only and the adapter never applies DDL.

The owned source uses ExecuteDeleteAsync for replacement; it is not a soft-delete
assignment. The global filter's precise selection remains unknown. Under the
candidate, a retained tombstone may conflict with the unfiltered unique
(account_id, order) index. That conflict rejects and rolls back the complete
replacement. The adapter never drops the tombstone or substitutes a partial index.

## Transaction and concurrency guarantee

board.Service now recognizes AccountTransactionRepository. For this adapter it
executes read, validation, app-owned payload preservation, replacement or scoped
payload save inside WithinAccount. READ COMMITTED plus the advisory transaction
lock on hashtextextended('solsynth.board:' || canonical_account_uuid, 0) ensures
a waiting cooperating writer takes its snapshot after the previous commit.

The transaction callback receives a Repository that does not implement the
transaction interface, avoiding recursive transaction entry. Every boardpg
Replace and Save entry point takes the same account lock. Validation errors,
cancellation and SQL errors cause rollback. The callback is never automatically
retried because validators can have external side effects.

Cache invalidation is exactly after successful commit, outside the callback.
A post-commit cache error is returned without claiming database rollback.

This corrects the stale-preservation gap where a transaction protected only the
final DELETE/INSERT while the service's earlier List read a stale app payload.
It is a stronger serialization boundary among cooperating Go writers, not a
claim that the legacy service already serialized the whole sequence. Legacy C#
or arbitrary SQL writers do not take this advisory lock; concurrent mixed-writer
operation is unsupported. Repository implementations without the optional
transaction interface still do not gain a cross-instance guarantee.

Catalog and normalizer calls can hold the transaction while awaiting an external
authority. Callers must provide bounded contexts; no timeout value is invented
as a legacy contract. No validator is supplied by this persistence package.

## Data preservation

- IDs must be canonical lowercase UUID strings; input IDs are never regenerated
- All queries and writes are account-scoped; transaction receivers reject access
  to another account
- Nullable widget/app fields preserve SQL NULL versus empty string
- JSON decoding uses UseNumber, preserving integers beyond 2^53, nested objects,
  arrays and nulls without float64 conversion
- The database column is SQL NOT NULL. JSON object {} and JSON null are distinct
  in direct adapter reads/writes. Root scalar/array payloads cannot be represented
  by the map-based service contract and fail explicitly
- Existing service layout/payload normalization can intentionally turn nil maps
  into empty objects; that service behavior is distinct from raw storage fidelity
- Payload saves change only payload and updated_at, scoped by account/id and
  active-row predicate, leaving ownership/layout/creation fields untouched
- Widget fallback lookup preserves the licensed ILIKE predicate and lack of
  explicit first-match ordering; the service subsequently checks exact identity

## Verification

Package tests check default-policy denial, canonical IDs, scoped transaction
denial, and JSON number/null fidelity. Actual PostgreSQL tests are owned by
tests/integration and run only through the disposable-cluster runner. Their
results must be recorded separately; source or unit tests alone do not establish
database execution, durability or production parity.

## Explicit independent storage policy

`NewWithIndependentPolicy(db, clock)` now selects the documented active-row,
audit, lock and persistence rules as an authored architecture choice. It does
not need an excluded implementation of internal hooks to work. The old default
`New(db)` remains disabled to avoid silently changing callers' expectations;
`NewForCompatibilityTests` delegates to the explicit constructor.

The new `Delete` executes an account-scoped physical deletion under the same
transaction/advisory-lock protocol. This is an explicit delete policy, not a
claim that the old shared SaveChanges hooks physically deleted rows. It leaves
cache policy to the caller; the recovered admin controller performs no purge.
The loopback board command selects this policy only in explicit synthetic mode.
Real authenticated-account and app-consent authority are still required before
any production wiring, independently of these completed storage choices.
