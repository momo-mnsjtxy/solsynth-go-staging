# Chat voice and placeholder continuation

This is an owned supported subset based on the retained licensed controller,
service, job and authored-client files listed in chat-transient-provenance.json.
It does not claim full original family or production-provider parity.

## Supported requests and state

- POST /api/chat/{room}/messages/placeholder with {"kind":"streaming"} or
  {"kind":"uploading"}, also through the existing /messager/chat alias.
  At most one active placeholder exists per room/sender. Replacement preserves
  accumulated nonblank streaming content as text, otherwise expires the old row.
- The existing /ws connection accepts the authored type/data/endpoint envelope,
  endpoint DysonNetwork.Messager, and only messages.placeholder.update and
  messages.placeholder.finalize in addition to ping. Update accepts message_id,
  content_chunk and progress. Finalize accepts message_id, content and
  attachments_id. The caller receives the same packet type with current message
  JSON, including authored heading escaping for finalized plaintext. Unknown
  packet types, endpoints, versions, duplicate fields and identity
  fields fail closed. Commands use the original connection's verified proof.
- Chunk updates refresh the source's 60-minute TTL, clamp finite progress to
  [0,1], and bound accumulated content to the existing 4096-character contract.
  An elapsed placeholder can be refreshed until the cleanup transaction expires
  it, matching the retained service. Finalization removes placeholder metadata,
  keeps the original message ID and sequence, and adds a messages.sync.finalize
  row. Exact finalize retries replay a durable receipt; conflicting retries fail.
- Finalized attachments use canonical current ownership and room media permission.
  Empty/omitted values preserve the existing request distinctions. Placeholder
  lifecycle remains plaintext-only, and voice never downgrades encrypted rooms.
- POST /api/chat/{room}/messages/voice accepts multipart file, client_message_id
  (or nonce), durationMs, repliedMessageId and forwardedMessageId as sent by the
  retained client. Equivalent explicitly listed server-style spellings are also
  accepted; supplying multiple spellings of one field is rejected.
- Voice emits type voice, voice_clip_id, the protected local voice_url, mime_type,
  size and optional duration_ms/file_name. GET /api/chat/{room}/voice/{clip}
  streams canonical stored bytes with current room read permission, no-store and
  nosniff headers and ordinary byte-range support. No remote URL is fetched.
- Delete accepts owned voice and placeholder messages as well as text. Deleting
  the message immediately removes its voice download authority; cleanup releases
  storage and quota only after the object has been removed successfully.

## Explicit owned storage policy

Voice is unavailable by default. Configure canonical media storage and explicitly
set --chat-voice-retention between 1h and 8760h. The absolute request bound is
8 MiB, further limited by the configured canonical media maximum and each user's
existing quota. The allowlist is audio/mp4, audio/m4a, audio/x-m4a, audio/mpeg,
audio/ogg, audio/wav, audio/x-wav, audio/webm, audio/aac and audio/flac; a declaration
is not a codec inspection. Nonnegative declared duration is bounded to 24 hours.
These bounds and retention configuration are owned safety policy. The retained
ChatVoiceService implementation/configuration is unavailable; no upstream default
retention, size bound, codec validation or remote object-provider behavior is
inferred. Upload additionally requires the canonical media.files.create grant.
Canonical storage refusals retain their owned media status/code (including quota,
size, idempotency, pending upload and missing/deleted files); they do not become
generic retryable chat-service failures.

The canonical media row, quota reservation and unbound clip receipt commit before
object storage. Message binding happens after storage using the original request
proof, current room policy, matching membership incarnation and current source
visibility. A failed bind conditionally abandons only an unbound clip. Compensation
locks account, quota and file and then checks the binding in a fresh statement;
a successful or uncertain message commit cannot be deleted by a failed retry.
An interrupted upload remains durably pending until canonical cleanup. A completed
but unbound clip expires at the pending deadline. Cleanup rechecks expiry after
locks, marks the file deleting and uses the existing object-before-quota-release
worker. Failed or uncertain removals retain their charge and are retryable.

Read proof, clip lifetime, source owner and current room audience are rechecked
after file/object waits. Room operations retain canonical account/realm/room lock
ordering. Membership, timeout, DM block, realm chat/upload permission, session,
scope and media quota checks are not replaced with connection-time snapshots.

## Explicit installation and remaining gaps

Install internal/chatpg/transient_schema.sql after chat schema and
internal/mediapg/voice_schema.sql after canonical chat/media schemas. The disposable
runner registers both explicitly; constructors do not run DDL. Existing duplicate
active legacy placeholders must be resolved before the unique-index migration.
The normal lifecycle worker expires placeholders; the media worker expires clips
and then retries durable object deletions. For rollback, disable command/upload
entrypoints and workers before deploying an older binary; preserve the additive
state and media receipts until all staged objects have been reconciled. No
automatic destructive rollback migration is supplied.

Original per-member WebSocket broadcast payloads remain a gap: this slice sends
authoritative command responses to the initiating connection and extends the
existing durable owned.chat.changed invalidation projection for other readers.
Existing clients still need the general original message-broadcast adapter for
full live multi-device behavior. Persisted reads/sync include canonical lifecycle
rows; full legacy DTO/sender hydration remains the existing projection boundary.
Bot/link-preview hooks on placeholder finalization and fund/survey/non-text typed
composition are not claimed. Voice forwarding uses the existing plaintext text
source validator; unsupported source message types remain rejected. External
provider configuration, unlicensed shared/protocol types and codecs are excluded.

## Verification

Ten guarded PostgreSQL tests cover lifecycle/replay, replacement and concurrent
creation, failed finalization rollback, membership/proof/MLS gates, quota and
private bytes, canonical object cleanup failure/recovery, expiry, after-wait
session/audience/lifetime changes, staged-upload proof/source/media-grant and
membership-incarnation changes, composed HTTP multipart/range and WebSocket
commands, and a paired database/process restart using only saved original bearer
credentials and public verifier material. The after phase performs reads and exact
idempotent replays; it does not seed, refresh a session or issue credentials.

Local build/vet/unit/race and offline syntax/argument guards are separate from
actual PostgreSQL execution. Database/socket/provider tests are not run locally.
New case grouping/dependency review and exact-commit actual-PG CI remain required.
