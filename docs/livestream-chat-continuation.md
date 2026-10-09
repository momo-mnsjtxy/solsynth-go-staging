# Canonical livestream chat continuation

This optional slice implements persisted livestream chat, history, Editor moderation and queued LiveKit data packets. It is based on the frozen initial livestream delivery (`solsynth-go-livestream.patch`, SHA-256 `278be88ab0c51eaa3833d228126a6e8de84d81ca6363ca278b7ff3d45705a06c`) over `d64aa11c45a80b151f40a99f85d93f4a7d2f30f0`. That delivery remains unchanged. No host, CLI, commerce, egress or storage configuration is included in this patch.

## Licensed evidence and compatibility

The complete retained authored `LiveStreamController.cs` at DysonNetwork `4ee2d0cebb9da8284a92483fee6dce795bf6947c`, lines 612–750 and 973–981, provides the four chat routes, request bodies, authenticated history, active-stream send requirement, sender snapshot, soft deletion, current publisher Editor moderation, default ten-minute timeout and timeout checks across all message anchors, including deleted ones. The complete authored `LiveKitLivestreamService.cs`, lines 343–415, supplies bounded retry and per-participant data sending evidence. The newly retained `Models/LiveStreamChatMessage.cs` gives the exact known message properties and 128/4096-character bounds. `operations-source/manifest.json` records immutable blobs, byte counts and hashes. The root AGPL license is already retained. Referenced Shared, protocol and generated files were neither opened nor copied.

The authored controller uses `PermissionKeys.LiveStreamsPin` for chat sending. Its excluded constant definition was not imported and no claim about its serialized value is made. Owned policy names are explicitly `livestreams.chat.send`, `livestreams.chat.moderate` and the existing `livestreams.view`. Each policy is additional to current canonical stream/publisher/realm visibility, owner/account state and social blocking. This is a reviewed first-party resource lane: new requests require the original opaque proof from the same verifier as the guard, a root Type-0 session and fresh transaction authorization. OAuth/bot/child credentials do not acquire this lane.

The previous bounded Solian caller search remains recorded in the initial delivery; it found no eligible authored livestream caller at its exact pinned revision. No broader Solian parity is claimed. Public LiveKit documentation, read 2026-10-08, supplies the independently authored HTTP adapter contract: [RoomService API](https://docs.livekit.io/reference/other/roomservice-api/) and [Data packets](https://docs.livekit.io/transport/data/packets/). Reliable packets are best effort, support about 15 KiB of user data, and do not provide offline buffering or recipient delivery receipts.

## Routes and responses

All routes use the installed `/api/livestreams` handler and require authentication.

| Route | Request | Result |
| --- | --- | --- |
| `GET /{id}/chat` | `limit` 1–100, default 50; `offset` 0–100000, default 0 | Nondeleted messages, newest first with deterministic ID tie-breaker |
| `POST /{id}/chat` | JSON `content`; optional single `Idempotency-Key` of at most 128 bytes | Persisted message, initially `delivery_state: pending` |
| `DELETE /{id}/chat/{messageId}` | Empty body or `{}` | Idempotent soft deletion and cancellation of a still-pending message packet |
| `POST /{id}/chat/{messageId}/timeout` | JSON nullable `duration_minutes`, default 10; owned bound 0–10080; optional idempotency key | Atomic timeout plus durable timeout packet; 0 expires this anchor immediately |

Messages expose `id`, `live_stream_id`, `sender_id`, current canonical public `sender`, cached `sender_name`, `content`, nullable `timeout_until`, creation/update/deletion timestamps and the owned `delivery_state`. Disabled/missing current sender projections become null; historical display text remains stored. `sender` reuses the canonical public account DTO, never authority/session/contact/credential data. All known authored message fields are represented; unrelated excluded account definitions are not reconstructed. The inherited timestamps follow the already-owned canonical timestamp projection.

Content must be nonblank, valid bounded text of at most 4096 Unicode characters. The serialized data packet must also fit 15360 bytes, so some long multibyte or escaped messages return 413 without inserting any row. HTTP history retains the canonical account profile; the packet's owned bounded account projection omits the optional profile. Pending, accepted, cancelled and failed are queue states. `accepted` means the provider acknowledged the API request, never that a recipient read or received it. Deleted messages are excluded from history. A deleted timeout anchor continues to mute until its deadline, matching the source query. Releasing one anchor does not clear a separate active anchor.

Duplicate keys with the same request return the original message or timeout effect; mismatched content/duration/generation returns conflict. A retry does not extend a timeout, resurrect deleted content or bypass fresh authority checks. Keys and fingerprints are SHA-256 digests, not stored request keys. Each stream has explicit lifetime message and event caps, including deleted messages and terminal events. There is no implicit purge that would discard idempotency history.

The packet for a message uses its stable message ID. Timeout packets preserve `type`, `sender_id` and `duration_minutes`, adding owned stable `event_id` and absolute `timeout_until`. A consumer should deduplicate IDs and honor the absolute deadline on retries. The source does not emit a deletion packet, so this slice adds none; refresh history to observe deletion. No separate unlicensed command/event envelope is invented.

## Transactions, privacy and retry behavior

New chat/history operations use same-transaction authority-only preflight before domain observations, a complete sorted account lock set, canonical locking guard validation, realm/publisher/stream locks, fresh domain gates and final guard revalidation. Moderator target observation is itself authenticated and visible/Editor-gated, commits before the mutation begins, and only supplies an immutable sender ID. The mutation includes that target in the complete sorted account set and rechecks it under the stream lock. Opposing moderators cannot acquire their own account first and later wait on each other's target.

The outbox stores the original account/session/version/epoch and exact permission with the event. It only retains or cancels an already-authorized effect; the approved recorded-session helper creates no identity, proof, grant or credential. Processing observes at most the initial livestream cap of 100 admissions, locks the complete account set in sorted order with NOWAIT before realm/publisher/stream/event locks, then rereads current state. The run owner/starter, event actor, moderator role and every recipient must remain eligible under canonical same-transaction checks. Account disable, owner transfer/version change, session or ancestor revocation/expiry, scope or grant removal, private/realm visibility loss and blocking can cancel or exclude a packet recipient. New admissions after the observation read persisted history; no late account lock is taken.

Only explicit, nonempty lists of freshly eligible canonical admission identities reach `SendData`. Empty LiveKit destinations would broadcast to the whole room and are rejected by the adapter. Unknown/manual provider participants never receive an owned packet through this path. Room data publishing remains disabled in ordinary livestream participant tokens, so a viewer cannot impersonate a persisted server chat packet through that grant.

The existing host `ProcessDue(ctx, limit)` automatically invokes `ProcessChatDue` when chat is enabled. Each branch is independently bounded by 1–100 items. Chat processes the oldest pending event per stream per batch and never bypasses an earlier event's backoff. The default example permits three attempts; configuration permits 1–20. Failures, including lock contention, count toward that budget. Backoff is bounded to 60 seconds, and event TTL is capped by run expiry. Closed/deleted streams, changed generations, expired events, deleted messages, revoked authority or zero eligible recipients cancel safely. Accepted/failed/cancelled records do not resend. Terminal records remain visible through message state and durable SQL state for operational inspection; no manual redrive API is supplied.

The provider RPC is bounded to at most five seconds while canonical locks remain held. A successful RPC followed by failed DB commit may repeat the same stable event ID; this is at-least-once provider acceptance, not exactly-once network delivery. Message storage, queue insertion, moderation and audit failures roll back together. Unconfigured providers fail explicitly. No background goroutine or arbitrary relay is introduced.

## Host wiring and migration

Retain the initial canonical livestream constructor, same pool/verifier/guard, canonical account repository with its social/realm provider, and the already configured strict-revocation LiveKit instance. The concrete `*rtcprovider.LiveKit` now implements the optional `livestreams.DataProvider`. Enable chat only through explicit operator configuration, for example a new disabled-by-default `--enable-livestream-chat` flag requiring livestreams to be enabled. The host owner adds that flag; this slice does not change transport composition.

Before serving requests, after constructing the repository:

```go
err = streamRepo.WithChat(livestreampg.ChatConfig{
    MaxMessagesPerStream: 100000,
    MaxEventsPerStream: 200000,
    MaxAttempts: 3,
    EventTTL: 5 * time.Minute,
    SendTimeout: 3 * time.Second,
})
```

`WithChat` performs configuration checks only and must be called once during startup, before concurrent use. Missing DataProvider or account projection support rejects enablement. `Ready` automatically includes `ChatReady` when enabled. With no chat configuration, all four routes return the existing explicit unsupported response, and the prior lifecycle scheduler behavior is unchanged. The normal `Service.Handler()` already dispatches these routes after this patch; no extra route family or actor headers are needed.

Apply `internal/livestreampg/chat_schema.sql` after `internal/livestreampg/schema.sql`, which itself follows canonical authority/accounts/social/media. New tables are `owned_livestream_chat_messages` and `owned_livestream_chat_outbox`; the latter captures canonical authority sessions and references messages and `(stream_id,generation)` runs. Indexes cover history, timeout anchors, per-actor request deduplication, one message event, pending due work and per-stream FIFO. No existing table or ledger is changed. Schema bootstrap must include this file only when its feature's migration is installed; enabled readiness fails on missing tables.

Current root already contains `internal/accountpg/transaction_reads.go` with `AccountInTransaction(ctx, tx, viewer, ref)`. The file included here is byte-for-byte identical to root `916046be18d50dbe6808f12ab380b97f84669ecb`; it is carried only as a prerequisite for the older frozen base. Keep the existing root file during composition. No new account projection policy is introduced.

## Validation and exact PostgreSQL inventory

Local checks use injected RoundTrippers, httptest recorders and compile-only guarded PG fixtures. No local PostgreSQL/socket, live provider, production account, persistent credential, storage upload or payment was used. Fixtures use the same verifier that constructed their guard, the same pool, explicit publisher quota/Editor invitations, actual DB timestamps and existing typed JSON/no-body request helpers. Batch sizes are 100; nullable timestamps scan as pointers, and timestamp parameters are explicitly typed where needed.

Before restart:

- `TestPGLiveStreamChatHTTPHistoryAndIdempotency`
- `TestPGLiveStreamChatTimeoutDeleteAndExpiry`
- `TestPGLiveStreamChatOutboxRetryRecipientsAndRecordedRevocation`
- `TestPGLiveStreamChatRollbackPacketBoundsAndGeneration`
- `TestPGLiveStreamChatVisibilityAndDisableDuringLockWait`
- `TestPGLiveStreamChatRetryLimitExpiryAndUnconfigured`
- `TestPGLiveStreamChatProviderAcceptanceRollbackAndFIFO`
- `TestPGLiveStreamChatOpposingModeratorsAndParentRevocation`
- `TestPGLiveStreamChatOutboxRestartPersistence`

After a real database restart: `TestPGLiveStreamChatOutboxRestartPersistence` only. The synthetic saved record contains stream/message IDs, opaque participant identity and an invalid provider endpoint, with no credential. Parent CI owns actual database execution and must verify these names in its required inventory. The restart assertion invokes the normal `ProcessDue` hook, proves the queued event is accepted once after reconstruction, and verifies subsequent processing does not resend it.

## Precisely deferred operations

The retained source also supports `POST /{id}/egress`, `POST /{id}/egress/stop`, `POST /{id}/hls`, `POST /{id}/hls/stop`; GET awards, active awards and award leaderboard; and POST awards, which creates a payment order. These routes remain explicitly unsupported here. The two award models are retained as research evidence only. No free awards, ledger, score or payout is synthesized.

Egress/HLS requires a separately implemented explicit operator destination-ID map to validated fixed endpoints/paths, runtime-only credentials, durable egress observation/cleanup, and compatible storage with fresh authorized proxy playback or short-lived owned signed playback. Arbitrary client output URLs are not accepted. No HLS private URL may be returned as a permanent public capability.

The approved future award path must create a canonical order and fulfill only after settled payment through a typed fixed-product same-transaction hook, current stream/owner gates and unique order/purchase/transaction receipt. Existing commerce settlement/PIN/ledger behavior remains authoritative. The frozen name-card fulfillment seam demonstrates that composition; no generic client payment-complete callback is sufficient. Subscriber notifications, additional data event types and provider webhooks also remain outside this slice.

## Integrated host candidate

The host exposes explicit `--enable-livestream-chat` only together with livestreams and configured provider capability. Retention caps, attempt budget, event TTL and send deadline are bounded CLI parameters. Both SQL schemas are explicitly installed by the disposable harness; enabled readiness and the fast existing worker include chat. The composed PG case checks gateway routing, canonical sender, authenticated history, a single explicit admitted recipient, durable provider acceptance and moderator deletion. No live data packet is sent by this validation. A subsequent SQL review adds explicit insert parameter types and removes an early run-row lock before the complete sorted account set; the frozen original artifacts remain preserved.
