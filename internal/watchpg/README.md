# PostgreSQL post-watch adapter

This adapter integrates the licensed Sphere post-watch preferences contract with PostgreSQL using `database/sql`. It does not start PostgreSQL, open its own connection, create a schema, migrate existing data, or use production credentials. The caller owns the database handle and registers a compatible driver (the application selects pgx/stdlib).

## Verified schema and source

Pinned repository: [Solsynth/DysonNetwork](https://github.com/Solsynth/DysonNetwork/tree/4ee2d0cebb9da8284a92483fee6dce795bf6947c), AGPL-3.0. Relevant licensed sources:

- [Migration 20260927043440](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Migrations/20260927043440_ReplacePostSubscriptionsWithWatchPreferences.cs), retained byte-for-byte under testdata; Git blob SHA-1 `25ca8f82c8dbcaefedc302291e2133c903328ad6`, 4194 bytes
- [AppDatabaseModelSnapshot](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Migrations/AppDatabaseModelSnapshot.cs) confirms the mapped table, columns, index and types
- [AppDatabase](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/AppDatabase.cs) declares PostWatchPreferences, snake-case naming and calls shared soft-delete/audit hooks
- [Models/PostWatch.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Models/PostWatch.cs) declares source enum order Bookmark=0, Reaction=1, Reply=2 and default flags

The table is `post_watch_preferences`, not `sn_post_watch_preferences`. It has required UUID id/account_id, integer source, five required notification booleans, required timestamptz created_at/updated_at, nullable timestamptz deleted_at, a primary key on id, and a unique index on (account_id, source, deleted_at). The test schema reproduces those declarations; it deliberately does not add a partial unique index or change NULL semantics.

The original migration also drops `post_subscriptions`. The adapter never executes that migration. `testdata/schema.sql` contains only CREATE statements for a fresh disposable test database. Do not run it against an existing deployment.

## Interface and atomic updates

`New(*sql.DB)` returns a repository implementing both `sphere.WatchPreferenceRepository` and the optional `sphere.WatchPreferencePatcher`. `WatchService.UpdatePreferences` chooses the transactional patch method when present. A full-row SavePreferences is also available, but callers performing nullable-field patches must use PatchPreferences or WatchService, rather than their own load/merge/save sequence.

For each account the adapter:

1. Requires canonical lowercase UUID spelling; noncanonical IDs are rejected, not silently aliased
2. Opens a READ COMMITTED transaction and obtains a PostgreSQL transaction-scoped advisory lock on `hashtextextended('solsynth.watch:' || account_id, 0)`
3. Reads only `deleted_at IS NULL` rows with FOR UPDATE after obtaining the lock
4. Rejects duplicate live source rows or unknown source enum integers instead of choosing one silently
5. Merges only non-nil patch fields into existing rows or source defaults
6. Updates by id/account/source, or inserts a random UUID, then commits before returning effective preferences

Two separately constructed service/repository instances cooperate through the database lock, including the absent-row race. A failed write rolls back the whole batch. Rows for other accounts or omitted sources are unchanged. Soft-deleted rows are ignored and retained; a new live row is inserted when needed.

The original nullable unique index alone permits multiple live rows because NULLs are distinct by default. The adapter does not assume ON CONFLICT can enforce live-row uniqueness and never repairs duplicate data automatically. Existing duplicates require an independently reviewed data decision.

## Boundaries

- The advisory protocol coordinates only participating adapters. Concurrent legacy C# writers or arbitrary SQL writers do not use it. A mixed-writer deployment is unsupported without additional coordination or an explicitly reviewed schema change
- Inserted rows get equal created_at/updated_at from Go's UTC clock; updates preserve id/created_at and set updated_at. The exact shared ModelBase/audit-hook behavior was not inspected or copied, so timestamp lifecycle parity is not claimed
- Strict lowercase UUID validation and refusing unknown source values are explicit adapter constraints
- The shared implementation license/access boundary remains intact. No shared source was translated. Its hook names and model usage were observed only in licensed callers
- This adapter persists preferences only. Bookmark/reactor/replier queries, notification delivery, real authorization, the rest of Sphere and existing-user-data migration are not implemented here
- There is no automatic DDL, destructive cleanup, credential management, payment action or deployment

Unit tests cover enum mapping, UUID validation/generation, account separation checks and transactional dispatch. Disposable PostgreSQL integration tests are maintained separately under tests/contract by the validator; do not confuse unit compilation with real database verification.

### Verification result for this environment

Unit tests, race checks and vet pass. Real PostgreSQL integration is **blocked, not passed**: disposable PG16 initialization succeeded, but server socket creation returned EPERM, including one reviewed escalation retry, and the server exited. Zero database integration cases executed. Six integration scenarios were prepared (multi-instance patches, rollback, soft-deletes/duplicates, identity/timestamps, account isolation and restart); ordinary Go test results skip them without a test DSN. See `docs/postgres-integration-result.json` for the recorded blocker. No alternate network route was used.
