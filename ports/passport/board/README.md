# Passport board service: partial Go port

Modified 2026-10-07. Source-derived from licensed Solsynth/DysonNetwork
4ee2d0cebb9da8284a92483fee6dce795bf6947c and licensed Solsynth/Solian
ffd0838e5f04cb40bea311a39118ebeedad210c1. GNU AGPL v3 applies; see LICENSE.txt.
This is not clean-room work and does not import NeTo implementation.

Scope correction, 2026-10-07: the unlicensed protocol repository's definitions,
schemas, descriptors and generated bindings are also excluded, including exports.
This package contains none of them. Its local JSON projection and historical
database fixture derive from the licensed sources below. No protocol export is
requested to complete its missing adapters.

## Implemented

- Ordered detached list
- Layout validation and atomic-repository replacement
- Custom-app payload retention by item identity, then widget identity
- Canonical widget keys, enabled/singleton checks, prebuilt cleanup
- App-owned and admin payload update service methods
- Explicit repository/catalog/normalizer/cache ports, failing on missing adapters
- Synthetic tests for JSON projection/enums, payload preservation, ownership,
  cancellation, errors, transaction failure, and cache failure after commit

The full [interface specification](../username/BOARD_INTERFACE_SPEC.md) distinguishes
known DTO/permission/database/RPC contracts from unknowns, with pinned sources.
The historical schema.json fixture comes from the two licensed board migrations;
it is not an executable migration or a claim that current schema is identical.

## Integration contract

Repository.Replace must be atomic and finish committing before it returns.
For full read/preserve/write serialization, a repository can implement
AccountTransactionRepository.WithinAccount. The service then runs the complete
mutation inside that transaction and invalidates cache only after commit.
Atomic Replace alone does not cover the preceding stale-payload read race.
Its error must leave the previous persisted board intact. Tests use an in-memory
fake in the package tests. The separately gated internal/boardpg candidate and
tests/integration now own actual database verification; their results must be
checked independently.
Auditing, soft deletion, first-match widget SQL semantics, and current schema
must be handled by an integration adapter. Reads must return detached values.

Catalog.Lookup must preserve existing app-board scope and widget rules. Catalog
Validate must call an authoritative validator. App payload updates and prebuilt
admin payload updates require Normalizer; custom-app admin updates require the
Catalog validator instead, as the source does. Missing validators are errors.
Layout placement itself does not run the universal payload validator because the
source does not: app-owned payload is preserved or cleared instead.

Adapters must parse IDs and supply canonical UUID strings. Case-insensitive
widget matching uses Go string operations; arbitrary Unicode equivalence with
.NET OrdinalIgnoreCase is not established. JSON payloads should use standard
JSON-decoded map[string]any, []any and scalar values. Item represents the known
client projection, not a full HTTP entity. It deliberately does not implement
HTTP binding defaults/validation; a future request adapter must apply the
documented defaults (is_enabled=true, payload={}) and MaxLength/UUID handling.

Missing Catalog/Normalizer/Cache is rejected before mutation. Cache invalidation
after a successful commit can fail: callers receive the error even though the
board has changed, matching the operation boundary rather than falsely claiming
rollback. A deployed recovery policy remains an integration responsibility.

## Explicitly unfinished

No HTTP server, JWT/authentication, permission evaluator, app-secret transport,
protobuf descriptors/adapter, verified production audit lifecycle or cache backend
is included. The separate boardpg adapter defaults disabled while row/audit policy
is unverified. Full response fields, permission wire values, RPC caller identity,
protobuf field numbers and universal-normalization edge cases remain unknown.
No bypass or permissive fallback is provided for those gaps.

This package is not a production replacement and does not demonstrate complete
backend parity. It is a testable internal service boundary ready for authorized,
documented adapters and differential fixtures.

## Sources

- [Board service](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountBoardService.cs)
- [Self controller](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountCurrentController.cs)
- [Admin controller](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountAdminController.cs)
- [Client JSON/enum](https://github.com/Solsynth/Solian/blob/ffd0838e5f04cb40bea311a39118ebeedad210c1/lib/accounts/widgets/account/board.dart)
- [Initial DDL](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Migrations/20260708150813_AddAccountBoardItems.cs)
- [Widget-key DDL](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Migrations/20260708152943_AddCustomAppWidgetKeyToAccountBoardItems.cs)

Run go test -race ./ports/passport/board and go vet ./ports/passport/board.

## Runnable HTTP continuation (2026-10-08)

The historical partial-port limits above describe the earlier checkpoint. The
new `http.go`, `envelope.go`, `synthetic.go`, and `cache.go` implement an actual
HTTP family using the existing domain operations and real `boardpg` persistence.
See `docs/passport-board-continuation.md` for the current status, operation list,
source conflicts, independent architecture decisions, testing, and outstanding
production authority contracts. The host is `cmd/passport-board` and stays
loopback-only. Without an explicit synthetic configuration every board route
fails closed. There is no fake success fallback for SQL, validation, or cache.
