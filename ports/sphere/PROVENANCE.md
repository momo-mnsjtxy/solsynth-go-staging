# Sphere timeline ranking port

This is an AGPL-3.0 licensed, source-derived Go port of the deterministic ranking stage and post-watch preferences service. It is not a clean-room implementation and not a complete Sphere service.

## Pinned source

- Repository: https://github.com/Solsynth/DysonNetwork
- Commit: `4ee2d0cebb9da8284a92483fee6dce795bf6947c` (2026-10-06)
- File: [TimelineService.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Timeline/TimelineService.cs)
- License: [LICENSE.txt](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/LICENSE.txt), GNU Affero General Public License version 3
- The exact source and license are retained under `upstream/`. The selected source file contains no separate license header; the repository tree returned only the root LICENSE.txt as a license/notice file.
- Modifications: translated the selected in-memory methods to Go, replaced service lookups with explicit Signals input, injected the clock, and returned copies rather than mutating original post objects. No source from an unlicensed repository was used.

## Portable contract

`CalculateBaseRank`, `RankPosts`, and `DecayedInterestScore` port the source's base ranking formula, mode branches, bonus arithmetic, aggressive cutoff/fallback, stable ordering, repeat-publisher debug penalty, latest-mode shadowban filtering, and interest decay.

The caller supplies canonical UUID identifiers, already-visible candidate posts, an explicit clock, and resolved per-post signals. `Signals.ShadowbannedForListing` must include both post and publisher shadowban state, as the original service's lookup does. The ranking branch uses these resolved signals; Latest instead uses the post fields and ignores all signals, matching the source early return. Post.Type uses the source enum names `Article` and `Blog`. Empty or all-zero publisher IDs mean no publisher. Missing signals default to zero/false. The zero-value Mode defaults to Personalized. Other Mode values are not HTTP validated by this package.

The repeat-publisher penalty changes DebugRank but deliberately does not reorder the selected candidates. Negative and zero take return an empty non-nil list. Future dates retain the source's Infinity/NaN formula behavior; descending float comparison preserves .NET NaN ordering. Equal ranks and timestamps preserve input order. The port does not add input clamping that upstream lacks.

## Excluded and unverified

No timeline HTTP routes, production identity/authorization, visibility query, Sphere database persistence, cache, gRPC, federation, advertisements, timeline event mixing, discovery, publisher/account resolution, or migration are implemented here. Signal providers remain integration work. Full timeline response schema and production parity are not claimed.

Tests are source-derived deterministic golden cases and boundary checks. The .NET service has not been executed in this environment, so these are not differential runtime parity tests. Go uses binary64 AwardedScore input, whereas the upstream casts its model value to double; callers must perform any decimal conversion consistently. Returned posts are value copies rather than the source's mutation of shared post objects.

## Domain inventory

At the pinned source, Sphere targets .NET 10 and contains post, publisher, timeline, ActivityPub, survey, sticker, translation, reader and livestream components. Program.cs connects account, presence, Blade, Ring, Drive, personality, wallet and domain-block services. Sphere dependencies include EF Core, pgvector, Redis, gRPC, Quartz and LiveKit.

The separate official [DysonFS](https://github.com/Solsynth/DysonFS) repository is already Go (module `src.solsynth.dev/sosys/filesystem`, Go 1.26.2 at commit `dd59985bec81d38bf3b896bcd593040d39f3e42b`), AGPL-3.0 and unarchived. Its HTTP/gRPC master, media worker and optional storage mode replace the prior Drive service. It is not duplicated here. GitHub metadata reports both repositories unarchived and `mirror_url=null`; that does not prove whether manually synchronized SoSYS copies are identical.

## Post-watch vertical slice

Additional licensed source files from the same commit, retained verbatim under upstream/:

- `DysonNetwork.Sphere/Post/PostWatchService.cs`
- `DysonNetwork.Sphere/Models/PostWatch.cs`
- `DysonNetwork.Sphere/Post/PostWatchController.cs`

These files contain no separate license notices. The implementation uses their declared behavior and types, without inspecting or translating the separate shared submodule's implementation. The source model's inherited ModelBase bookkeeping and generated GUID are persistence details outside the public preferences response; this port does not claim their lifecycle parity.

`WatchService` implements effective preference defaults, nullable field updates, account/source filtering, bookmark/reactor/replier watcher union, event preference filtering, and chain-ID repository delegation. Explicit repository interfaces require live-record filtering and atomic durable row upserts. The package itself does not deliver notifications. The separate local watchstore adapter is an integration fixture rather than a PostgreSQL migration.

`NewWatchHandler` provides GET and PUT `/api/posts/watch`. PUT JSON groups are bookmark, reaction and reply, each with nullable reactions/replies/chains/forwards/edits booleans. Omitted/null fields preserve existing/default values. Success returns three rows in bookmark/reaction/reply order using source and notify_* snake_case fields. Account identifiers never come from request JSON and are not included in the response.

The mandatory WatchAuth adapter supplies authenticated AccountID and checks the semantic PostSubscriptionsManage permission on PUT. The unknown shared PermissionKeys wire constant is deliberately not guessed or copied. Missing adapters fail closed. The loopback fixture command is not production authentication.

Explicit transport/integration differences: a local 1MiB request-size limit, local generic 400/403/500/503 error envelopes (only the controller's explicit 401 envelope is matched), GET/PUT only without ASP.NET implicit HEAD behavior, Go standard-library JSON binding rather than ASP.NET model-binding semantics, no ModelBase IDs/timestamps or database migrations. Repeated/unknown service-level source updates are rejected; the source HTTP controller itself emits each of the three known source updates at most once. A private mutex serializes load/merge/save across updates to one WatchService instance; replicas still require database transactions or optimistic concurrency spanning the whole operation. This strengthening is documented rather than claimed as exact upstream concurrency behavior.

Tests cover source-default matrices, partial updates, account separation, deduplicated watcher resolution, errors/cancellation, authenticated read/write HTTP paths, denial with no writes, malformed bodies, and concurrent distinct-field patches. C# differential execution and production auth/database integration remain unverified. See parity.json for the full unported surface.

### PostgreSQL checkpoint

The optional WatchPreferencePatcher repository contract now permits atomic nullable-field updates across service instances. `internal/watchpg` implements it against the exact licensed post_watch_preferences schema, with transaction-scoped advisory account locks, live-row checks and duplicate refusal. No migration or DDL runs automatically. See its README and retained migration for schema evidence, explicit UTC timestamp policy, strict canonical UUID requirement, and the limitation that legacy C# writers do not participate in its locking protocol. The per-instance mutex remains the fallback for nontransactional repositories. Real production authentication, shared audit-hook parity and the rest of Sphere are still outside this implementation.
