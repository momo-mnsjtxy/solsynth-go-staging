# Chat rich messages, media and reactions

This owned implementation extends the existing canonical chat lifecycle. It uses
retained AGPL-3.0-only authored source, not excluded shared schemas or generated
bindings. The existing `owned-text-room-v1` response marker continues to describe
a bounded projection; complete legacy message/member DTO parity is not claimed.

## Supported contracts

- POST `/api/chat/{room}/messages` and the host `/messager/chat` alias accept
  content, client_message_id/nonce, same-room replied_message_id,
  attachments_id (ordered canonical UUID list, at most 32), meta (object, 16 KiB),
  and embeds (at most 16 objects, 16 KiB). Null optional rich fields mean omitted.
  Meta and embeds are client-provided JSON, stored without interpreting links,
  dereferencing external IDs, contacting providers or making trust claims.
  Nonempty embeds are represented in `meta.embeds` as in the controller.
- Attachment-only messages are accepted. Empty messages are rejected. Source
  bytes retain canonical ownership, privacy, quota accounting and lifecycle.
  Only the file owner can grant a private-file association. Seeing a private
  attachment in a room or post never authorizes redistribution elsewhere.
- PATCH supports optional content, replacement attachment IDs (empty detaches),
  and nonempty embeds. Omitted/null content preserves the old content; explicit
  empty content clears it only if content-bearing media/embeds remain. Raw meta and reply fields on edits are accepted without replacing existing
  values, matching `UpdateMessageAsync`. Edit client_message_id values are
  stored/echoed while the original send key remains independently preserved.
  Null thread_id/forwarded_message_id fields in the authored Solian envelope
  are accepted; non-null thread/forward operations remain unsupported.
- DELETE soft-deletes the original and old edit snapshots, removes live media
  associations and reactions, and appends the existing deletion event atomically.
  Room deletion immediately denies access through the room's deleted state.
- POST `.../messages/{message}/reactions` toggles one symbol for the current
  member: 200 with a reaction on addition, 204 on removal. DELETE
  `.../reactions/{symbol}` is an idempotent 204 removal. GET `.../reactions`
  returns an array and X-Total, with symbol, offset, take (1–100; default 20),
  and order=created support. All fifteen default controller symbols are accepted.
  Custom paid reactions remain unsupported. Attitude is a bounded signed
  integer stored and echoed opaquely; no shared enum mapping was inferred.
- Reaction changes append `messages.reaction.added`/`removed` recovery records
  with the evidenced message_id, symbol, reaction and reactions_count metadata.
  Existing sync/list/get reads hydrate actor-specific reactions_made. Room
  sequence allocation, count rebuilding and event insertion are one transaction.

## Authorization and live media

Writes keep the existing verified authority proof and fresh transactional
account/session/permission checks, room lock, current membership, timeout and
linked direct/realm policy. Message IDs always belong to the specified room;
edit/delete require the current sender membership. New reaction writes use the
owned `chat.messages.react` permission. No headers or request payload identities
are accepted as authority. Lock ordering is accounts, room, then sorted files.

`internal/chatmediapg` stores canonical file links and hydrates current metadata
inside the reader's transaction. Old edit records load the current original's
associations, so detached/deleted files are never revived by recovery. The
canonical media reader combines post and chat visibility. Private-file byte
access rechecks active room membership or public room visibility, linked social
policy, file state and owner status. Metadata and byte grants disappear after
member removal, room privacy/deletion, file deletion, or owner disablement.
No source file is promoted to public, copied, or recharged for a link.

Install `internal/chatmediapg/schema.sql` after canonical chat and media schema.
The existing chat schema adds independently owned reaction attitude/timestamps;
no shared schema layout is claimed. Startup performs readiness checks only.
Existing plain-text request hashes remain unchanged for upgrade-safe retries.
`owned_creation_client_id` and a unique expression index preserve send replay
identity when edit correlation IDs change, including pre-migration rows.

## Evidence

Pinned server: Solsynth/DysonNetwork
`4ee2d0cebb9da8284a92483fee6dce795bf6947c`.

- `ports/messager/source/DysonNetwork.Messager__Chat__ChatController.cs`:
  request fields at 355–389, send at 610–854, edit/delete at 1087–1338,
  default symbols and reaction routes at 1340–1498.
- `ports/messager/source/ChatService.cs`: edit events at 2240–2305,
  delete at 2434 onward, atomic reaction count/events at 2559–2789.

Pinned client: Solsynth/Solian
`ffd0838e5f04cb40bea311a39118ebeedad210c1`.

- `ports/messager/contract-evidence/lib__chat__messages_notifier.dart`:
  reaction POST and 200/204 handling at 2405–2422, recovery event handling.
- `lib__chat__data__message_repository.dart` in the same directory: send/edit/
  delete routes and map-based request/response handling at 170–220.
- `lib__chat__send__message_sender.dart`: complete authored plain/E2EE send
  payload builder at 706–774, including nullable reference fields, meta and edit
  correlation IDs. Its exact Git blob was verified before retention.
- `lib__data__message.dart`: authored local message conversion, attachment,
  reaction count and reaction-made fields.

Existing manifests retain exact source URLs, Git blob IDs, SHA-256 and licenses.
The accompanying patch file manifest records every changed implementation file.
No prohibited NeTo/Spec, canceled SoSYS/Socommon source or unlicensed Suki source
was retrieved or consulted.

## Verification and limits

Offline `go test ./...` passed; targeted HTTP validation tests exercise rich
payload bounds, exact duplicate-key rejection and reaction status/permission
behavior. New guarded PostgreSQL tests cover full host upload/send/read/edit/
reaction/delete composition, private redistribution denial, empty-edit rollback,
cross-room/account isolation, current file/membership/permission after lock
waits, reaction concurrency and event rollback, and persisted media/reactions
across restart. They are registered in both required runner phases as applicable.
Without PG_TEST_DSN they compile and skip. Actual PostgreSQL execution remains
for the parent-authorized private Actions runner; no local sockets were attempted.
All 65 maintained SQL files and nine PL/pgSQL bodies parsed under pinned pglast
6.7 / PostgreSQL 16.1. This is syntax validation, not runtime DB validation.

Still unsupported: full DTO parity, mention extraction, thread writes,
cross-room forwarding/redirect, provider-created voice/transcoding, special
fund/survey/calendar/location resolvers, custom subscription reactions, and live
websocket/push notification publication. Rich payloads are currently unencrypted.
Encrypted-room enablement stays fail-closed until authored key/epoch/membership
contracts have been researched and an owned server state machine is verified.
Ciphertext transport fields are evidenced; that alone does not establish safe
key distribution, MLS epoch authority or encrypted-room lifecycle support.
