# Post-watch recipient and chain readers

Status: disabled by default. Ordinary `watchpg.New(db)` returns `ErrUnverifiedWatcherPolicy` from both watcher and chain methods, even for empty input. Preferences remain unchanged. No new HTTP routes or outbound notifications are added.

Only the explicitly named `NewSoftDeleteOnlyCompatibilityTestCandidate(db)` constructor enables the fixed soft-delete-only policy for synthetic compatibility tests. The opt-in is private and immutable after construction, accepts no SQL or policy configuration, and is not connected to the command, HTTP handlers or event delivery. This is not a production authorization or recipient-exposure mode.

## Licensed source evidence

Both the query and schema evidence come from Solsynth/DysonNetwork commit `4ee2d0cebb9da8284a92483fee6dce795bf6947c` under its root AGPL-3.0 license:

- [PostWatchService.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostWatchService.cs), already retained in `ports/sphere/upstream`
- [AppDatabaseModelSnapshot.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Migrations/AppDatabaseModelSnapshot.cs), the repository's own generated schema snapshot
- [AppDatabase.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/AppDatabase.cs), which calls `ApplySoftDeleteFilters`

`watcher-schema-evidence.json` records observed mapped column types, nullability, table names, source line references and the snapshot blob hash. No separate Shared implementation was inspected or translated. Although entity names mention Shared, the evidence used here is the licensed parent repository's own snapshot and caller.

## Observed query behavior

`Repository` now satisfies `sphere.WatcherRepository`, but the following behavior is accessible only on the explicit compatibility-test candidate:

- WatchAccounts returns distinct bookmark account IDs for matching post IDs
- Reaction account IDs are distinct and NULL account IDs are excluded
- Replier account IDs come from reply posts joined to their publishers; NULL publisher account IDs are excluded
- The source does not require a matching live target post or add visibility, draft, realm, local/federated, reaction attitude, shadowban or recipient authorization filters at this stage; the Go queries do not invent them
- ChainPostIDs includes the head itself if live and direct chained posts whose chained_post_id equals that head. It does not recurse and does not separately require a live head
- Source queries have no ordering contract. Local SQL ordering is deterministic for reproducibility, but compatibility tests compare sets

The already-ported WatchService merges these source sets and checks effective per-source notification preferences. An account is selected at most once when any applicable source allows the event. These are candidate watcher IDs; notification authorization and actual sending remain outside this adapter.

## Explicit global-filter assumption

The Go queries apply `deleted_at IS NULL` to bookmarks, reactions, reply posts, joined publishers and chain posts. The licensed AppDatabase invokes a soft-delete-filter extension, but that extension is in a separately licensed/access-controlled Shared boundary whose implementation was not inspected. Therefore the exact predicate and EF navigation-filter behavior have not been established. Applying these explicit live-row checks is an informed compatibility assumption, not a proven reproduction of all global filters. This limitation must remain visible even if synthetic PostgreSQL integration tests pass.

Each of the three source queries is a separate read, matching the service's separate query structure; no stronger single-snapshot guarantee is claimed. Errors return without a partial recipient result. Context cancellation is passed to every SQL operation.

## Input and fixture limits

Inputs require canonical lowercase UUIDs, matching the existing preference adapter's explicit constraint. All IDs are parameter-bound; only locally generated placeholder numbers are interpolated. Repeated input post IDs are deduplicated. For the candidate only, empty WatchAccounts input returns three empty slices without accessing a database; canceled context returns context.Canceled; invalid IDs are rejected before a query. Ordinary repositories always fail closed with ErrUnverifiedWatcherPolicy for these methods.

`testdata/watchers-projection.sql` is a synthetic minimal projection of four tables. It omits the many unrelated columns and production foreign keys/indexes/constraints. It is not the full upstream schema, a migration or production deployment SQL. Cases involving physically missing referenced heads/publishers are projection-only query checks; the full upstream foreign keys can make those states unreachable. Soft-deleted referenced rows are a separate valid case. Nothing automatically runs this fixture.

Unit tests cover canonical input, bound placeholder construction, deduplication, empty input, missing dependency and cancellation. Real PostgreSQL integration scenarios and fail-on-skip checking are owned separately under tests/integration. Do not infer production model or global-filter parity from those fixture checks.
