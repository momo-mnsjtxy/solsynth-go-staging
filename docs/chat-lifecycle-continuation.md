# Owned text-room lifecycle

Subsequent plaintext thread and forwarding support is documented in
`chat-threads-forwarding.md`; the original bounded lifecycle is described below.

This continuation implements group-room creation, joined-room listing, metadata
updates, room deletion, invitations, public-community joining, leaving/removal,
notification preferences, read markers, and text send/edit/delete. It is an
independently specified subset, not complete Messager or Solian compatibility.

## One durable authority

`internal/chatpg` writes the existing `chat_rooms`, `chat_members` and
`chat_messages` tables. `internal/messagerpg` remains the only message
read/sync/unread implementation. No second membership or message collection is
created. Existing read-only projection rows have no `owned_owner_id` and are
deliberately not writable. New owned rooms use canonical authority account UUIDs
and are immediately readable through the existing room sync, exact/range
recovery, message list/detail and unread paths.

Install `internal/authoritypg/schema.sql` before `internal/chatpg/schema.sql`.
The latter creates shared base tables if missing, then adds owned lifecycle
columns. In existing disposable CI, retain the original messagerpg fixture and
its evidence unchanged; install chatpg **after** that fixture. Constructors and
executables never run DDL or seed policies/accounts. This is a fresh owned schema
and additive projection extension, not a verified legacy production migration.
Do not run mixed legacy writers: only cooperating owned writers share the room
row-lock protocol.

Pass the same explicit plaintext and three distinct notification enum mappings
to chatpg and messagerpg. The combined host can replace its sync-only handler with
`(&rooms.Service{Repository: cr, Authority: proofIdentity{identity}, Reader: mr}).Handler()`
under its existing `bindIdentity`. The authority interface matches the account
module's proof adapter. The standalone `cmd/chat-service` accepts only the public
verifier configuration; it requires `--serve`, loopback listen address, injected
DSN-variable name, public PEM/key ID/issuer/audience, and all four policy flags:
`--none-encryption-mode`, `--notify-all`, `--notify-mentions`, `--notify-none`.

## Routes and deliberate policy

- `GET/POST /api/chat`; `GET/PATCH/DELETE /api/chat/{room}`
- `GET /api/chat/{room}/members`; `GET/POST/DELETE .../members/me`
- `POST /api/chat/invites/{room}` with `related_user_id`
- `GET /api/chat/invites`; `POST .../{room}/accept` or `/decline`
- `DELETE /api/chat/{room}/members/{account_id}`: the authored controller calls
  its parameter memberId but actually matches AccountId; this route does too
- `PATCH .../members/me/notify` with `notify_level`
- `POST .../members/me/read` with `message_id`: independent route, monotonic
  marker tied to a visible same-room message rather than a client wall clock
- `POST .../messages`, `PATCH/DELETE .../messages/{message}`
- Existing message GET, room POST sync and GET unread are delegated unchanged

Only the room owner can change room metadata, invite, remove or delete the room.
Only an active sender can edit/delete their own text. Owner membership cannot be
removed or abandoned. Private invitations require acceptance and grant no prior
read/write access. Joining requires both public and community flags. A removal
blocks self-rejoining until the owner explicitly reinvites. Reads of membership
lists require active membership even in public rooms; invitations are visible
only to the owner and invitee. Other people's read markers are always masked.
Private room metadata also requires active membership. New authenticated
lifecycle projections use `X-Solsynth-Projection: owned-text-room-v1`.

All mutations lock the room first. Sequence allocation, content changes, and
`messages.update`/`messages.delete` rows commit together. Events carry the target
in `meta.message_id` and never inherit the original client-message identifier.
Text sends can deduplicate a client identifier (legacy `nonce` is accepted) per
sender and room; reusing it with a different original payload is a conflict.
Edits cannot reassign identifiers or reply targets. Deletes are retry-safe and
hide the original and historical edit snapshots while retaining a content-free
delete event. No previous plaintext is added to event metadata. Replies must
reference a live text message in the same room. Deleted rooms/members/messages
are consistently hidden by existing reads.

The list envelope includes `rooms` but does not invent a groups result. Room and
member DTOs are limited projections; account/sender/realm hydration and client
model defaults remain unresolved. Room/member lists are bounded to 100 rows with
take/offset. Text content is nonblank valid text up to 4096 Unicode code points;
the existing heading-display formatter is used on HTTP output. This is not the
excluded shared sanitizer implementation or a guarantee of HTML sanitization.

## Explicit remaining boundaries

The original slice left media, E2EE/MLS, calls, attachments, threads, bot identities, realms,
slugs, pin/reaction writes, groups, embeds, locations, payments and other provider
fields/routes unsupported. Subsequent slices extend several of these boundaries;
see their continuation documents. Remaining unsupported routes fail with 501.
Unknown or duplicate JSON fields fail with 400.
The server does not deliver websocket/push events, external invite notifications,
moderation messages, email or audit RPCs. Invitation acceptance is a local consent
policy and does not claim legacy relationship-block/provider parity. The user
must poll the existing sync path. Timeouts, block-management, mention parsing,
global/room-membership sync, scheduled jobs, complete frontend compatibility and
production rollout/rollback are not complete. Existing source sync timestamp,
ordering and limited hydration behavior is preserved rather than silently changed.

## Licensed evidence

Authored controller behavior was inspected in the retained AGPL DysonNetwork
snapshot at `4ee2d0cebb9da8284a92483fee6dce795bf6947c`:
`inventory/operation-sources/DysonNetwork.Messager/Chat/ChatRoomController.cs`
(room/list/invite/join/leave/removal) and `ChatController.cs`
(text request, sender-only edits/deletes and permissions). Sequence-event and
identifier behavior follows `ports/messager/source/ChatService.cs` and the
retained licensed Solian `ports/messager/contract-evidence` client handlers.
Their source manifests and license notices remain the provenance authority.
No excluded Shared/Spec/NeTo definitions, generated bindings, or canceled
repository source were fetched or reconstructed.

## Verification and required CI

Focused build, vet, ordinary and race tests pass for rooms, chatpg and
chat-service. The entire integration package compiles and its tests correctly
skip without the marked disposable PostgreSQL environment. No local listener,
production connection or real account/credential was used. This does not claim
actual database execution here.

Run the guarded integration runner with chatpg schema installed after messagerpg.
Require these before-restart cases, with no skip accepted:

- `TestPGChatRoomTextLifecycle`
- `TestPGChatMembershipPrivacyAndPolicy`
- `TestPGChatConcurrentSequencesAndRollback`
- `TestPGChatConcurrentRemovalAndSend`
- `TestPGChatAuthorityAndUnsupportedBoundaries`
- `TestPGChatRestartPersistence`

Run `TestPGChatRestartPersistence` again after restarting the same disposable
cluster. It persists only synthetic IDs, verifies memberships/text/events, and
checks that the next send continues sequence allocation. Tests cover separate
database pools, concurrent duplicate sends and sequence allocation, removal/send
serialization, transactional failure rollback, cross-room operations, invitation
privacy, private receipts, replay after deletion, live grant denial and session
revocation, and existing unread/sync consumption of real lifecycle state.
