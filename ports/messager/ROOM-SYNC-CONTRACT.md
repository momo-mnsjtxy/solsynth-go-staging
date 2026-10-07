# Room-sync interface specification and evidence limits

## Status and evidence

This specification combines licensed server source at DysonNetwork
`4ee2d0cebb9da8284a92483fee6dce795bf6947c` with licensed Solian client
source at `ffd0838e5f04cb40bea311a39118ebeedad210c1`.
Both repositories contain AGPL-3.0 licenses. Six client evidence snapshots
and their verified Git blob / SHA-256 digests are in `contract-evidence/`.
The inspected evidence does **not** establish the complete SnChatMessage DTO:
its actual model/serializer is in the uninspected shared SDK submodule.
No denied repository was accessed and no deployment was queried.

Primary evidence:

- [Server controller](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatController.cs#L1649)
- [Server selection and hydration](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Chat/ChatService.cs#L2094)
- [Client recovery call and parsing](https://github.com/Solsynth/Solian/blob/ffd0838e5f04cb40bea311a39118ebeedad210c1/lib/chat/messages_notifier.dart#L343)
- [Client local/remote message conversion](https://github.com/Solsynth/Solian/blob/ffd0838e5f04cb40bea311a39118ebeedad210c1/lib/data/message.dart)
- [Client mutation event handling](https://github.com/Solsynth/Solian/blob/ffd0838e5f04cb40bea311a39118ebeedad210c1/lib/chat/realtime/message_handler.dart)

## Transport, authorization and selection

Local route: POST /api/chat/{roomId:guid}/sync.
Gateway/client route: POST /messager/chat/{roomId}/sync.
The documented authentication mechanism is a Bearer token, while the
controller consumes an already verified CurrentUser supplied by upstream
authentication. This document does not reconstruct or replace token verification.

Authorization order:

1. Require CurrentUser; otherwise 401
2. Find the requested room; otherwise 404
3. If encrypted or nonpublic, require account membership with joined_at present
   and leave_at absent; otherwise 403
4. Public, unencrypted rooms permit an authenticated nonmember

Room-sync has no explicit AskPermission attribute in the pinned method, unlike
global sync. Shared RemotePermissionMiddleware still runs and is not specified
by this slice. Do not replace the middleware with an assumption of unrestricted
access or conflate this endpoint with anonymous public-message reads.

Request fields are last_sync_timestamp (signed 64-bit Unix milliseconds),
missing_sequences (optional signed 64-bit list), and missing_sequence_ranges
(optional start_sequence/end_sequence pairs). A nullable last_sync_message_id
is declared on the common request DTO but unused in room sync. Only timestamp
is marked Required. Framework behavior for omitted zero-value numeric fields,
explicit nulls, malformed GUIDs, overflow, unknown fields and validation error
bodies still needs an ASP.NET compatibility oracle.

Always filter room ID and null ThreadId. Valid normalized selectors choose
sequence recovery; otherwise filter CreatedAt strictly after the timestamp.
Sort room_sequence ascending then created_at ascending, take at most 1000.
No offset/page token is accepted in this method. The response includes:

- messages: selected, hydrated message records
- current_timestamp: CreatedAt of the last sequence-ordered record, or server
  current time for an empty result
- total_count: matching count before the 1000 limit
- X-Total header: same total_count

No current_message_id field exists in the room SyncResponse. Global sync is
different and must not be substituted.

## Observed message wire fields, not an exhaustive DTO

The client supplies explicit snake_case structural keys and typed conversion:

| Field group | Observed type / behavior | Remaining uncertainty |
|---|---|---|
| id, chat_room_id, sender_id | String IDs | Full serializer constraints and GUID normalization |
| type | String event/message type | Exhaustive enum/value set |
| room_sequence | Backend int64; client extracts raw integer or parseable string, with roomSequence fallback | Client web precision above 2^53; do not infer strings are accepted by server |
| content | Nullable string | Server omission versus explicit null |
| client_message_id, nonce | Nullable strings on client | Nonce is legacy in client; not proof server still emits it |
| created_at, updated_at | Client DateTime; server NodaTime Instant serialization | Exact offset/fraction spelling and null handling in shared model |
| deleted_at, edited_at | Nullable client DateTime | Omission/default serialization |
| meta | Arbitrary JSON map | Full metadata schemas by message type |
| members_mentioned | List of string identifiers after client sanitization | Whether server emits null/empty/default |
| attachments, reactions | Lists of nested objects after sanitization | Full nested DTOs and required fields |
| sender | SnChatMember object, whose account may be hydrated | Full member/account/profile/realm fields |
| replied_message_id, thread_id, forwarded_message_id | Nullable strings | Related nested object serialization |
| reactions_count | Map of symbol to count | Source of complete server count snapshot |
| reactions_made | Map of symbol to bool, per caller | Exact omitted/null policy |
| is_thread_root | Bool derived from actual replies | Full DTO default behavior |
| thread_replies_count | Integer count | Full serializer constraints |
| encryption_meta | Optional object with ciphertext, header, signature, scheme, epoch observed by sanitizer | Complete cryptographic DTO; do not invent defaults |

Client sanitization tolerates malformed/null meta as an empty map and malformed
or null mentioned-members/attachments/reactions as empty lists. That is client
robustness, **not** proof that a replacement server may emit arbitrary shapes.
The local conversion derives isDeleted from deleted_at; isDeleted is not
established as a server wire field.

Server startup selects SnakeCaseLower, enables NodaTime serialization and
allows named floating-point literals. It does not establish every inherited
model property's null-ignore settings. Do not materialize arbitrary message
numbers through float64 or reformat dates speculatively. Raw JSON preservation
plus explicitly decoded int64 cursors is the conservative boundary.

## Edits, deletions and sequence events

The C# update service permits edits to text messages and mutates the original
record, sets edited_at when content/attachments/ciphertext/encryption changes,
then inserts a separate messages.update sync row. The event references its
target with meta.message_id; it deliberately does not inherit the original
client_message_id. For a plaintext content change, previous_content may be
included. This event has its own persisted sequence.

Deletion permits text/voice messages, sets the original deleted_at and
updated_at, then inserts a separate messages.delete row with meta.message_id.
Its client_message_id comes only from the deletion request, never the target.
The deletion event uses the deletion instant for created_at/updated_at.
Voice-asset cleanup is best effort. Database global-filter visibility of the
original tombstone is still unestablished: do not add or remove tombstones
based solely on the client behavior.

The client dispatches both outer packet.type and inner message.type. It uses
meta.message_id (falling back to event ID where coded), stores mutation events,
and updates the referenced local record. A delete clears local body,
attachments and meta and marks a local tombstone. Therefore “return just the
latest text message” is insufficient for recovery parity.

## Hydration is behavior, not optional decoration

Room sync loads distinct senders through LoadMemberAccounts and substitutes
hydrated sender objects. It then:

- Computes reactions_made for the requesting account (empty map when none;
  null when no account was supplied to the helper)
- Escapes unencrypted Markdown heading markers outside fenced code blocks,
  through EscapeMarkdownHeadings called inside reaction hydration
- Computes thread_replies_count from nondeleted rows grouped by thread_id
- Sets is_thread_root only when such replies actually exist

This requires member/account lookup, per-account reaction state and thread
counts. The service does not simply echo raw rows. Model/global-filter behavior
for reactions and other nested data is still a dependency. Content escaping
also needs string/Unicode parity if translated.

## Safely implementable boundary

A future adapter can split verified components without fake success:

1. Authenticator returns a verified account identity or a typed failure;
   missing configuration fails closed
2. RoomAccessReader reads room visibility/encryption and active membership
3. MessageReader returns source-visible top-level rows under documented
   snapshot/transaction rules, preserving complete raw JSON and int64 cursors
4. Hydrator returns fully populated records or a typed dependency failure;
   never replace missing sender/accounts/reactions with successful stubs
5. Existing RecoveryPlan handles only selection normalization
6. Response writer preserves unknown JSON fields and uses a verified timestamp
   codec and error adapter once those contracts are available

Before claiming HTTP compatibility, obtain the shared model/serializer and
base filters through an allowed licensed source or an owner-supplied schema;
verify middleware, error envelopes, GUID/time validation and hydration; run
golden cases including edits/deletions, empty results and >1000 records. Until
then, only an explicitly labeled local subset can be built. No additional
handler, command, persistence layer or placeholder-success adapter was added.
