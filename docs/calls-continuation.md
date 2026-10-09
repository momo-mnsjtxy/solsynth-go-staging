# Owned room-call signaling continuation

The call lifecycle is implemented against canonical `chat_rooms` and `chat_members`. It is an independently owned signaling projection, identified by `X-Solsynth-Projection: owned-room-call-v1`. It does not implement or validate the audio/video media plane.

## Runtime and contracts

The retained licensed `RealtimeCallController.cs` informs these routes beneath `/api/chat/realtime/{roomId}`:

- `GET` and `POST`: ensure the room's current call/session exists
- `GET /join?tool=…`: admit the authenticated member and return `provider`, `endpoint`, `token`, `call_id`, `room_name`, `room_title`, `is_admin`, `participants`, and bounded `expires_at`
- `GET /participants`: provider-observed participants, mapped to canonical member profiles where currently authorized
- `DELETE ?tool=…`: disconnect/revoke the caller's selected admission
- `POST /kick/{accountId}`, `/mute/{accountId}`, `/unmute/{accountId}`, `/invite/{accountId}`
- Owned extension `POST /end`: owner-only termination

The HTTP adapter requires verified authority, rejects identity assertion headers, ambiguous/unknown JSON fields, duplicate queries, encoded path alternatives, and credential queries. All responses are `no-store`. Tools use a bounded alphanumeric/underscore/hyphen identifier, not caller-selected provider identities. JSON room/member subobjects remain the existing owned room projections. These are not declarations of full upstream DTO or existing-client parity.

Call participants are admitted only while canonical membership is joined, not left, not deleted, and not banned. Moderation requires the room owner plus explicitly mapped `chat.call.kick` or `chat.call.mute`; invites require `chat.call.invite`. `is_admin` reflects ownership but provider join tokens never carry room administration. There are no fabricated DM or member-admin roles. Call timeouts apply across calls and tools; repeated kick retries retain the existing future timeout. Timeout cause/operator are persisted. Text-message timeout policy is outside this call module.

Every authenticated repository operation revalidates the opaque authority proof using the same-pool `TransactionGuard` and holds its account lock through the domain transaction. Canonical room locks serialize joins, starts and membership transitions. An authority/session epoch change cannot be overwritten by a fresh admission before cleanup. Admission keys preserve one account/member/tool slot; rejoining after a confirmed leave gets a new opaque identity. The projection bounds call slots at 100 and slots per account at eight.

## Provider implementation

`internal/rtcprovider` is a real injectable LiveKit RoomService adapter, independently authored from [official API documentation](https://docs.livekit.io/reference/other/roomservice-api/) and [official token/grant documentation](https://docs.livekit.io/frontends/reference/tokens-grants/). Its constructor requires an explicit WSS endpoint, API key, secret, bounded HTTP timeout, and asserted Cloud token-revocation support. Construction performs no I/O. Redirects, malformed acknowledgments, oversized replies, and provider errors fail closed; diagnostic responses never include provider bodies or credentials.

The adapter uses JSON Twirp room create/delete, participant list/removal, and track mute APIs. Join JWTs are HS256-signed, opaque-identity and room scoped, limited to publish/subscribe/data rights, and expire within the configured maximum of five minutes, the call lifetime, and the source session lifetime. Management credentials stay server-side. Endpoint/name are pinned in persisted call records; changing provider configuration cannot silently apply an existing call's operations to another deployment.

Removal sends an explicit Cloud revocation cutoff and requires a successful acknowledgment, including when an identity has disconnected. A generic participant-not-found error is not treated as confirmed revocation. Self-hosted LiveKit removal does not provide the same token invalidation contract, and token expiry alone does not terminate existing/reconnecting media sessions. Consequently this adapter refuses configurations without asserted Cloud revocation. Remote unmute remains subject to provider privacy configuration and can fail.

No real provider was contacted, and no live credentials, calls, media sessions, or device access were created during implementation/testing.

## Persistence, recovery and realtime

Install `callpg.Schema` explicitly after authority, chat, and notification schemas. Construction never runs DDL. Owned tables are `call_v1.calls`, `members`, `events`, `timeouts`, and `invites`.

A call is `preparing`, `active`, `ending`, or `ended`, with one non-ended call per room. The stable room/session name is committed before provider creation. Retrying a failed/crashed start recovers that name. Admissions are `admitted`, `removing`, or `left`. Removal intent is committed before remote work; a failure leaves it retryable and never produces a completed leave event or successful removal response. Ending first revokes all admitted identities, then deletes the room, then commits `ended`. Repeating successful leave/end is idempotent. Provider-observed pending participants remain visible as `revocation_pending`; admission alone never implies media connection.

Canonical member removal queues admission cleanup in the same SQL transaction via a trigger. Room deletion/encryption changes queue termination. `ProcessDue` also detects call expiry, disabled accounts, session revocation/expiry/epoch changes, account-version changes, active disable-account punishments, and invalid ancestor sessions. Work is bounded and cancellation-aware; failed items remain durable for retry, with fair ordering among pending calls. The host must schedule `ProcessDue` continuously. Canonical membership removal is immediate in the database; provider disconnection follows maintenance and is not claimed to be instantaneous. An unavailable or mismatched provider keeps remote cleanup pending.

Events are committed with local transitions and projected exactly once into `notification_v1.events`, using its canonical account/stream locks and `chat` audience. Existing SSE/WebSocket replay rechecks current room membership. Event-time membership also bounds recipients. Owned event names are `owned.call.started`, `owned.call.admitted`, `owned.call.member.left`, `owned.call.mute.changed`, `owned.call.invited`, and `owned.call.ended`. Invites are recipient-specific and deduplicated for one minute. These are owned invalidations, not unverified upstream packet enums, system-chat messages, VoIP pushes, or delivery receipts.

Host integration: `callpg.New(db, guard, provider, callpg.Config{NoneEncryptionMode, TokenTTL, CallTTL, NotificationApp})`; bind the original authority proof and route `calls.Service` ahead of ordinary room routes, including the host's gateway alias. A nil provider keeps call operations explicit HTTP 503. Keep the original provider configured and finish pending cleanup before removing the module/schema. Do not erase recovery records while provider work remains.

## Verification

Passed locally: all eight focused HTTP/repository/provider unit tests under the race detector, and `go vet` for these packages and integration tests. Provider tests inject an in-process transport and ephemeral synthetic key; no sockets are opened.

Six PostgreSQL test functions compile and are guarded by the existing disposable-cluster harness. They were skipped locally because no disposable PostgreSQL DSN was supplied. They cover lifecycle and replay, idempotency/concurrent start and restart, provider removal/deletion failures and retries, global call timeout, canonical membership/session/account revocation, expiry/room deletion, preparation recovery, HTTP boundaries, and lock waiters observing committed revocation. A successful package build is not evidence these SQL tests ran.

Source attribution: retained AGPL `ports/messager/source/DysonNetwork.Messager__Chat__RealtimeCallController.cs` and `DysonNetwork.Messager__Models__RealtimeCall.cs`, originally [DysonNetwork revision 4ee2d0ce](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/RealtimeCallController.cs). No excluded generated definitions are imported or reconstructed.

Remaining release checks: actual disposable PostgreSQL execution, authorized provider deployment/contract verification, existing-client compatibility and media-plane testing, source-compatible system messages/VoIP push, and operational capacity/latency review. Provider calls run with five-second bounds while holding the room lock to serialize effects; this favors correctness and should be measured under deployment load.
