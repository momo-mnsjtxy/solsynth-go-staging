# Canonical direct and realm chat continuation

Frozen base: `8e223ab2b1e88816d910c01fff9feff5900a20e1`. This slice extends the existing canonical chat tables, social relationships, realm membership, account profiles and private authority proof. It creates no parallel identity or message store. Host composition remains a separate integration change.

## Authored contract evidence

`ports/messager/relations-source/manifest.json` records exact revisions, Git blobs, SHA-256 and retained root licenses. DysonNetwork is pinned to `4ee2d0cebb9da8284a92483fee6dce795bf6947c`; Solian to `ffd0838e5f04cb40bea311a39118ebeedad210c1`.

- Authored `ChatRoomController.cs`: direct pair creation and lookup, recipient confirmation/block/unblock, `realm_id` create/update, realm moderator management, invite requests and account-ID timeout routes.
- Authored `ChatRoomService.cs`, newly retained verbatim: canonical room/member lookup, direct-message confirmation semantics, room/member lifecycle and room event behavior. Its shared-model references were not followed into excluded sources.
- Authored `RealmChatController.cs`: authenticated realm-room metadata listing, private realm membership and public realm visibility.
- Authored `RealtimeCallController.cs`: a direct participant is a call administrator; kicking the physical room creator remains forbidden.
- Authored Solian `chat_input.dart`, `chat_room.dart` and account `profile.dart`: direct-message type `1`, recipient confirmation, timeout compose restriction, direct-room lookup/create and room sync callers.

No generated/shared DTO, Spec/protocol artifact, excluded repository or unlicensed corpus was imported. Full legacy DTO equivalence is not claimed. Existing explicitly owned room/member projections now include evidenced type, realm association, confirmation, timeout and canonical account data. Timeout cause exposes known reason/sender/since fields; the unknown shared cause-type enum is omitted.

## Runnable routes and lifecycle

The existing `rooms.Service` handler supports:

| Route | Behavior |
| --- | --- |
| `POST /api/chat/direct` | `related_user_id`, optional configured plaintext `encryption_mode`; atomically create one private pair room with two joined members |
| `GET /api/chat/direct/{accountId}` | Find the caller's existing pair and recovery metadata |
| `POST /api/chat/{roomId}/confirm` | Unconfirmed, active recipient confirms |
| `POST /api/chat/{roomId}/block` | Unconfirmed recipient leaves and locally blocks; 204 |
| `DELETE /api/chat/{roomId}/block` | That recipient restores its membership; returns its member projection |
| `GET /api/realms/{slug}/chat` | Authenticated canonical realm metadata listing, bounded `take`/`offset` |
| Existing room create/update | Accept canonical `realm_id`; require current moderator role in the old and new realm for a transfer |
| `POST /api/chat/{roomId}/members/{accountId}/timeout` | Canonical group owner or realm moderator sets `reason` and future `timeout_until`; 204 |
| `DELETE /api/chat/{roomId}/members/{accountId}/timeout` | Authorized moderator removes timeout; 204 |

Pair creation is unique across caller order. Creation confirms the creator; the recipient can read the initial conversation and must confirm before sending or joining a call. Both participants must remain joined and active. A block in either canonical social direction, local recipient block, account disable or account-disable punishment closes message reads, sync, room lists, unread counts, send, notification replay and call admission. Pair recovery metadata remains available to either pair account after leave/block, without granting message history. When normal room access is denied, the owned `recovery_only` projection contains only established room/pair identifiers and the requesting member’s joined/left/confirmed recovery state. Peer profiles/contacts, other membership details, room name/description, message content, MLS identifiers and key material are not hydrated; other legacy scalar fields are redacted defaults. A deleted requesting membership retains pair identifiers without inferred joined/confirmed state. Corrupt owners outside the pair are rejected. Direct reads lock both canonical accounts in sorted order before room rows, use read-committed visibility and revalidate the original bound proof after waits; direct creation/state-change responses use the same final visibility projection. Unguarded reads are available only through the explicit synthetic compatibility-test constructor. A soft-deleted room can be replaced for the same pair, atomically under both account locks.

A public room inside a private realm requires current realm membership. Realm sends require current realm membership even when both realm and room are public. Room membership is also required for sending, calls and private room reads. Public realm metadata listing follows the authored controller and may list private room metadata; that does not grant its messages. Room transfer immediately applies the new realm's membership policy. Physical room creator status cannot bypass withdrawn realm moderation authority.

Timeouts block text send/edit and call participation, but preserve history reads. Owned limits require a future deadline no more than 30 days away, a valid UTF-8 reason of at most 4096 characters, and no self-timeout. Realm moderators cannot timeout/remove a peer or superior. The source's fine-grained realm permission matrix remains unavailable; current canonical Normal/Moderator/Owner roles are used explicitly.

The authored invite request contains an integer `role`, but its controller never applies that value and no authored independent room-role CRUD contract was found. Omitted/zero role preserves ordinary invitation; nonzero role returns 501. This slice does not invent a room-role enum or authority table. Third-member DM expansion, MLS/E2EE, bot automatic confirmation/execution, media/profile customization, legacy system-message envelopes and fine-grained custom realm roles remain unsupported. Existing explicitly owned message events continue through canonical notification projection; relation audit entries are not represented as fabricated legacy system messages.

## Same-transaction policy and lock order

`chatpg.NewWithAuthority(db, policy, guard).WithSocial(socialRepo)` requires the canonical pool and the exact verifier-backed `TransactionGuard`. Every write validates the original opaque proof and named permission again in the same transaction before commit. No request header, public principal reconstruction or bare account ID can authorize a write.

`beginRoomWrite` first calls canonical `TransactionGuard.Preflight` in the same pool/transaction. This validates the original opaque proof, issuing verifier, current account/session/permission state and ReadCommitted isolation using only nonlocking authority reads, before any domain observation. It then resolves all actor, peer, target and old/new realm-owner accounts, locks their canonical rows in sorted order, and performs the mandatory locking Authenticate/Authorize check. Sorted realm and room locks follow, with fresh association checks and another guard check before commit. A successful preflight alone cannot authorize a write. A custom guard without the preflight contract retains its exact rejection; if it returns success, the adapter fails unavailable before domain SQL. Social writes use the same canonical account/realm locks.

Call adapters already acquire an actor authority lock. Their `LockForAction` boundary requires ReadCommitted and acquires secondary accounts, realms and room with NOWAIT. Contention therefore returns a retryable unavailable result instead of waiting in an inverted lock order. Fresh association checks reject stale links. Call admission retains these locks across provider token issuance and revalidates the private proof before commit; call creation also revalidates after provider work. Provider dependencies remain the existing explicitly configured strict-revocation adapter; tests use only the synthetic in-memory provider.

`CanReadInTransaction(ctx, tx, actor, roomID)` is an **additional** relation gate, intended for autocomplete and other projections after their normal joined/unbanned member checks in the same guarded transaction. It does not itself replace base room membership or bearer authentication. The fixed-expression `ReadSQL`, `SendSQL` and `CallSQL` predicates support canonical reader, unread, projection/replay and maintenance queries. They accept only fixed SQL parameter/identifier expressions, never request fragments. Missing relation policy excludes guarded rooms. Candidate-only legacy reader constructors keep their original projection behavior; production uses `NewWithIndependentPolicy` with the provider installed.

## Schema and host wiring

Install canonical authority/account/social and base chat schemas, then `chatpg.RelationsSchema`. Existing account, social, notification and call migrations remain their canonical sources.

- `chat_rooms`: `owned_relation_guard` defaults false and `owned_type` defaults group 0, constrained to 0/1.
- `chat_members`: nullable confirmation and timeout deadline/reason/sender/since. The sender FK references the canonical moderator membership.
- `owned_chat_direct_pairs`: sorted account pair primary key, unique canonical room FK.
- `owned_chat_realm_links`: canonical room primary key and realm FK, with `(realm_id,room_id)` index.
- `owned_chat_relations_audit`: actor/room FKs, action, optional target, DB timestamp.

The flag lets older ordinary rooms remain readable while newly linked rooms fail closed when their optional provider is missing. Updated owned readers/notifications/calls expect the new base chat columns, so install the base migration before deploying binaries.

Recommended composition, using existing instances:

```go
chatRepo, err := chatpg.NewWithAuthority(db, chatPolicy, guard)
if err != nil { return err }
chatRepo.WithSocial(socialRepo)
messageReader.WithRoomAccess(chatRepo)
notificationRepo.WithRoomAccess(chatRepo)
callRepo.WithRoomAccess(chatRepo) // when calls are configured
chatHandler := (&rooms.Service{
    Repository: chatRepo, Authority: verifier, Reader: messageReader,
    Bind: guard.Bind,
}).Handler()
```

The existing host guard middleware can retain ownership of proof binding; `Bind` supports standalone direct handler composition with the same verifier. Dispatch `/api/realms/{slug}/chat` to this handler before the broader realm handler. Preserve all other realm routes. Existing `/api/chat/...` dispatch continues normally. Add `chatRepo.RelationsReady(ctx)` to readiness. No new goroutine is created. Existing bounded `callRepo.ProcessDue(ctx, 100)` revokes admitted provider identities after timeouts, blocks, realm changes and account/session revocation; its existing scheduler cadence bounds remote revocation delay. Reads and new admission deny immediately. Notification replay rechecks the current relation policy even for previously projected events.

Named write permissions remain `chat.create`, `chat.update`, `chat.delete`, `chat.messages.create/update/delete`, existing invitation/kick permissions and `chat.members.timeout`. Realm moderation is an additional canonical role requirement, not a replacement for authority permission.

## Verification and fixture audit

Eight guarded PostgreSQL cases, including the separate preflight correction, are supplied for the parent's disposable CI. They compile locally but have not been executed by this worker:

1. `TestPGChatDirectLifecycleAndCrossPathPrivacy`
2. `TestPGChatDirectConcurrentPairAndRollback`
3. `TestPGChatRealmModerationAndRevocationAcrossReaders`
4. `TestPGChatTimeoutAndCallAdmissionRecovery`
5. `TestPGChatRealmTransferAndMissingCallPolicy`
6. `TestPGChatRelationsDisableAndProofDuringLockWait`
7. `TestPGChatRelationsDeletionAndRestartPersistence` (both real DB-restart phases)
8. `TestPGChatAuthorityPreflightSameTransactionAndNoLocks` (separate review correction): proves a successful preflight retains no account lock, rejects unbound/foreign-pool/substituted identities, and is followed by a locking guard that observes a subsequent disable.

The cases cover reverse-pair concurrency, transactional rollback without orphan room/member/pair records, confirmation and local/global blocks, private realm visibility, withdrawn moderator authority, transfer policy, missing providers, timeout expiry, call identity removal, disable observed after an account lock wait, unbound-proof denial and durable restart state. Restart files contain only synthetic UUIDs, never keys or tokens.

Fixtures share one pool and the same exact verifier instance with their guard; they bind genuine synthetic proofs. All new HTTP checks use the existing `chatCall` helper with JSON content type and explicit bodies, compatible with the parent's standardized fixture helper. No new manually constructed HTTP requests are introduced in PG tests. Relevant runtime deadlines come from the DB clock; comparison deadlines use microsecond precision. Nullable projections use nullable scans. New SQL uses typed canonical columns or explicit UUID casts, with no untyped CASE timestamp literals. ProjectChat and ProcessDue use their actual maximum batch of 100. Realm quota and wildcard synthetic grants are explicitly seeded only in fixtures.

The original `tests/transactionguard` denial-before-domain-SQL regression is preserved unchanged. A second no-socket regression verifies that an unknown successful custom guard cannot silently acquire write capability without preflight. Focused race tests include these guard regressions; affected-package vet, integration compilation and all-package tests/build are the local validation scope. PostgreSQL execution remains the parent's disposable CI responsibility. No local socket, PostgreSQL daemon, external provider, real account, credential or production deployment was used.

The host now wires the exact canonical chat policy into message reads, notifications/replay and configured calls, and routes the realm chat family before broader realm handling. Full local build/vet/race plus the unchanged transaction-guard regression pass. TestPGBackendChatRelationsComposition adds real HTTP-recorder direct confirmation/block and private realm listing coverage; actual database execution is tracked separately.
