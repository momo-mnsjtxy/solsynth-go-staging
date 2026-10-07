# Messager room-sync selection port

This is a runnable, dependency-free Go port of one pure C# behavior, not a
replacement messaging service. It performs no network requests and stores no data.

## Provenance and license

- Upstream: https://github.com/Solsynth/DysonNetwork
- Immutable commit: `4ee2d0cebb9da8284a92483fee6dce795bf6947c`
- Implementation: [ChatService.cs lines 2094–2238 and 3254–3261](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatService.cs#L2094)
- HTTP integration context: [ChatController.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs)
- Repository license: GNU AGPL version 3, copied verbatim to `source/LICENSE.txt`
- No differing license headers were found in either selected C# file
- Original source snapshots are included unmodified in `source/`
- Modification date: 2026-10-07; translation retains AGPL-3.0-only notices

This is a source-informed port, not a clean-room reimplementation. No implementation
from an unlicensed repository is incorporated.

## Preserved behavior

`NewRecoveryPlan` filters nonpositive exact IDs, deduplicates and sorts them.
Ranges are first reversed if needed, then discarded entirely if their lower
bound is nonpositive. Remaining ranges are sorted and adjacent/overlapping
ranges merged. Exact IDs and inclusive ranges form a union.

If no usable selections remain, `UsesSequences()` is false: the caller must
use the existing strict timestamp filter. `MatchesSequence` alone is not an
authorization or full room-sync predicate.

The unchecked signed overflow of upstream `previous.EndSequence + 1` at
MaxInt64 is preserved: some redundant overlapping ranges remain separate.
Their union selection is unchanged. Tests document this compatibility quirk;
changing it should be a separate reviewed behavior change.

`SequenceRange` preserves `start_sequence` / `end_sequence` JSON names and
signed 64-bit integer precision. RecoveryPlan is a new internal Go API, not a
claim that the existing HTTP API has been migrated. Defensive copies prevent
callers from mutating a built plan.

## Integration gaps

Not ported: authentication, room visibility, membership, storage, counter
allocation, migrations, thread exclusion, timestamp conversion, pagination,
sender/reaction/thread hydration, response timestamps or counts, WebSocket/gRPC
transport, LiveKit calls, notification delivery, queues or scheduled jobs.

Upstream GetSyncDataAsync filters the room and excludes thread rows, orders by
room sequence then creation time, applies a limit and hydrates records. Those
constraints must remain in an eventual adapter. Local route
`POST /api/chat/{roomId}/sync` uses a 1000-record limit; this package adds no
route or server and cannot be pointed at a deployment.

## Verification

Six local tests cover fixed normalizations, fallback, zero values, aliasing,
64-bit JSON, overflow compatibility and 1000 deterministic synthetic randomized
selection cases. Go 1.26.4: `go test -race -cover ./ports/messager` passed with
100% statement coverage; `go vet ./ports/messager` passed. No original C# runtime,
database, frontend, service integration, or production test was executed.

The implementation uses Go 1.23-compatible standard library features; only
Go 1.26.4 was actually executed.
