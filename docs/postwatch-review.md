# Independent PostWatch compatibility review

Reviewed 2026-10-07 against licensed DysonNetwork commit
`4ee2d0cebb9da8284a92483fee6dce795bf6947c`. This is a behavior/schema
review, not a deployment assessment. No production requests were made.
Implementation files were not edited by this reviewer.

## Evidence

- [Controller](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostWatchController.cs)
- [Service](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostWatchService.cs)
- [Own model and enum declarations](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Models/PostWatch.cs)
- [Migration](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Migrations/20260927043440_ReplacePostSubscriptionsWithWatchPreferences.cs)
- [JSON startup configuration](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Startup/ServiceCollectionExtensions.cs)
- [Middleware order](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Startup/ApplicationConfiguration.cs)

## Confirmed matches

GET/PUT success returns exactly three sources in bookmark/reaction/reply order.
Response field names are source and notify_reactions/replies/chains/forwards/edits;
there is no account_id. Request fields intentionally differ: group names are
bookmark/reaction/reply, with reactions/replies/chains/forwards/edits booleans.

Nullable booleans preserve existing values, false is an actual update, null
groups create no update, and empty groups create default rows when absent.
Defaults match the source: bookmark enables all events; reaction/reply enable
chains and edits only. Unknown events return false.

Eleven isolated in-process Go HTTP probes are recorded in
`postwatch-review-probes.jsonl`. Mixed-case keys work; null group/field and
empty group preserve defaults; string/number bools and array-valued groups
return 400; unknown keys are ignored. These are Go observations, not differential
C# runtime tests. Microsoft documents [case-insensitive web defaults](https://learn.microsoft.com/en-us/dotnet/standard/serialization/system-text-json/character-casing),
so ordinary ASCII casing should not be described as a known mismatch.

## Exact correction/verification requests

### R1 — Keep the entire patch transaction in the SQL adapter

Current LoadPreferences followed by full-row SavePreferences can overwrite an
unrelated concurrent field update when two WatchService instances share storage.
The private mutex protects only one instance. The planned WatchPreferencePatcher
must receive nullable updates, load and merge them under one account-scoped
database transaction, then persist and return preferences. Adding a transaction
only around SavePreferences is insufficient.

Acceptance: two separate service/adapter instances concurrently patch different
fields for the same account/source and preserve both changes. Also test first
insert races, rollback, cancellation and a failure before commit. Content worker
confirmed this design is in progress; this report does not certify unreviewed SQL.
The local JSON adapter remains a single-process, single-shared-service fixture.

### R2 — Do not assume the legacy unique index enforces one live row

The migration creates a unique index on account_id, source, deleted_at, with
deleted_at nullable. PostgreSQL's [default unique-index null behavior](https://www.postgresql.org/docs/current/ddl-constraints.html#DDL-CONSTRAINTS-UNIQUE-CONSTRAINTS)
permits multiple otherwise-equal rows with null indexed values. Thus duplicate
live rows are possible under the declared schema. Do not use an assumed
ON CONFLICT(account_id,source) target, silently select one duplicate, or claim
that the old index serializes creation.

Acceptance: query live rows, detect duplicates and return a typed failure
without changing data. Serialize new Go writers using the planned transaction
advisory lock on a canonical account identity, including the no-row case.
Document that legacy writers which do not take that lock are not coordinated.
A partial unique-index migration or data repair is a separate reviewed change,
not something this adapter should run automatically.

### R3 — Preserve database enum and row metadata

Own enum declarations establish Bookmark=0, Reaction=1, Reply=2; event ordinals
are Reactions=0, Replies=1, Chains=2, Forwards=3, Edits=4. Migration source is
integer, while public HTTP source is a lowercase string. Do not store Go string
constants directly into source or infer SQL enum labels.

The table requires UUID id/account_id, all five nonnull booleans, nonnull
created_at/updated_at timestamptz and nullable deleted_at. Inserts need explicit
values: migration supplies no generated ID or timestamp defaults. Updates
must retain id/created_at, update updated_at, and preserve historical deleted
rows. Do not interpret an invalid numeric source as a known default silently.
Reject or explicitly document handling for malformed legacy enum values.

### R4 — Narrow the HTTP compatibility claim

Current Go handler accepts JSON bodies with text/plain or absent Content-Type
(observed 200). ASP.NET [FromBody] uses selected input formatters, so media-type
acceptance needs a golden baseline before claiming HTTP parity. Add tests for
application/json, application/*+json, text/plain, absent type and charset.
Implement a verified content-type policy or keep this an explicit subset
difference. Existing 1MiB cap, generic error envelopes and missing implicit HEAD
are already documented differences.

Duplicate outer group keys are another concrete test target: Go currently merges
the two objects in
`{"bookmark":{"replies":false},"bookmark":{"edits":false}}`,
leaving both flags false. System.Text.Json's [default object replacement](https://learn.microsoft.com/en-us/dotnet/standard/serialization/system-text-json/populate-properties)
suggests different behavior for duplicate group properties. This is a
source/framework-based hypothesis, not confirmed C# execution. Obtain a .NET
oracle before changing it; do not silently reject duplicates while claiming
unchanged compatibility.

### R5 — Preserve permission-before-body behavior, but do not invent errors

Source middleware order is authentication, authorization, RemotePermissionMiddleware,
then controller mapping. PUT has AskPermission(PostSubscriptionsManage) and
Authorize; GET has Authorize only. Go checks identity, then PUT permission,
then body decoding, which is the expected high-level order. Add tests proving
denied/unauthenticated malformed requests do not reach storage.

The exact permission string and shared middleware failure envelope remain
unknown. The controller explicitly emits the known 401 ApiError when
CurrentUser is absent, but earlier authentication middleware may challenge
differently. Distinguish this from the generic Go 403/400/500/503 responses.
Do not label every 401 response fully equivalent solely because the controller
contains a matching object.

## SQL implementation follow-up (12:00 UTC)

Read-only review of newly added `internal/watchpg/repository.go` confirms the
planned mechanisms are now present: WatchService dispatches nullable updates to
WatchPreferencePatcher; the adapter begins READ COMMITTED, takes an account
advisory transaction lock before reading, selects live rows FOR UPDATE, rejects
duplicate live source rows, patches under the lock, and returns only after
commit. Deferred rollback covers intermediate errors. It does not run DDL or
assume an ON CONFLICT target. Numeric source mapping and required insert fields
match the own declarations; updates retain id/created_at and require one row.
R1–R3's core implementation requests are therefore addressed in source review;
real database execution remains the validator's responsibility.

Explicit differences still deserve documentation: canonical lowercase UUIDs
are required by the Go adapter, malformed stored enum integers fail closed
rather than being ignored, and historical rows are filtered with deleted_at
IS NULL. The latter is the intended live-record boundary, while inherited
ModelBase global filters were not established by this review. Unconditional
updated_at writes for an unchanged/empty filter group should not be claimed as
exact EF tracking parity without the shared timestamp hook.

Added `tests/contract/watch_http_binding_test.go` in coordination with the
content worker and validator. Seventeen request-binding cases plus authentication/
permission-order checks pass with the race detector. The tests assert the
documented local subset, not unresolved duplicate-key/media-type parity.

## Limits and next checkpoint

No .NET runtime differential or live PostgreSQL execution was performed by
this reviewer. SQL source review is recorded above; actual locking, isolation,
restart and concurrent-insert behavior still require database testing.
Existing fake driver tests, if added, can verify
SQL arguments/control flow but cannot establish actual PostgreSQL locking,
schema or isolation behavior.
