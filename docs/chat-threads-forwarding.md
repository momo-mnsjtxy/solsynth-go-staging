# Chat threads and text forwarding

This slice extends the canonical owned chat tables, HTTP handler and reader with
plaintext threads, live text references and explicit text-copy redirects. These
are bounded contracts, not full legacy message/member DTO or attachment
forwarding parity. Existing explicit plaintext/notification mappings are reused;
no excluded Shared enum value is inferred.

## Threads

POST `/api/chat/{room}/messages` accepts the authored `thread_id`,
`replied_message_id`, `forwarded_message_id` and `is_thread_root` fields, including
the `/messager/chat` host alias. A thread ID denotes a live same-room text root.
A root cannot itself have a thread or quoted-reply parent. A reply's optional
parent must be the root or another reply in that thread. Ordinary quoted replies
remain distinct from thread membership. Nesting is bounded to 32 reply-parent
edges; a direct root reply has depth zero. Relationships remain immutable through
edits. Omitted/null relationship fields preserve stored values; a supplied
different thread/reference conflicts. An empty explicit root hint is accepted,
while response `is_thread_root` is derived from actual live replies.

GET `/api/chat/{room}/messages/{root}/thread` returns the authored envelope
`{root,replies:[{message,depth}]}` and `X-Total`. It uses current room read policy,
including anonymous access to permitted public plaintext rooms. Invalid supplied
credentials cannot become anonymous. Paging defaults to 20, caps at 100 and limits
offset to 100000. Each reply appears once, in reverse sequence order, with actual
ancestry depth. Tombstoned ancestors supply only structural depth, never content.
This replaces the source's unbounded descendant expansion after pagination.
Root, room and sender visibility are enforced; encrypted content is excluded.
Main list/sync continues to exclude thread replies. Owned root counts use the
same-room plaintext/live-sender reply boundary as thread GET, including
detail/list/sync. Synthetic legacy projections retain the evidenced raw count.

## Live reference versus explicit copy

An ordinary `forwarded_message_id` is a live text reference. Sending requires
current source readability and current destination send authority. Every owned
list/detail/sync/thread hydration rechecks source visibility inside that read's
transaction, including each nested reference. No cached source authorization is
used. The limited `forwarded_message` preview contains only message, room and
sender IDs, content and creation time. It never hydrates source metadata, files,
profiles or cryptography. Recursion is bounded to four source messages. Deleted,
inaccessible, encrypted or cyclic references hide their identifier/preview and
set `forwarded_message_unavailable`. Readers without the canonical hydrator hide
live references by default. Ordinary reads retain their existing consistent
transaction snapshot; they do not claim to retract bytes already received.

POST `/api/chat/{destination}/messages/redirect` with `{"message_ids":[...]}` is
an explicit durable copy. It accepts 1–100 distinct ordinary plaintext messages
from one room. The actor must currently belong to source and destination and may
send to the destination. Public readability alone does not authorize this copy.
Destination readers may then read the copied text without source membership.
Source edits, deletion or membership withdrawal do not retract an existing copy.
New attempts and idempotent retries still require current source existence and
admission. This audience expansion is the explicit redirect contract, and is
never implied by an ordinary reference.

Redirects retain the source's v1 single-message and v2 history-segment envelopes:
source IDs, text, timestamps, sender_map, canonical sender/redirector attribution,
and range for multiple messages. Ordering uses full database timestamps and ID
tiebreaks; exposed timestamps retain the authored millisecond shape. Attribution
uses canonical account ID/name/nickname and membership IDs. Only room IDs are
copied. Profiles, contacts, member preferences, realm identity, arbitrary metadata,
reactions, crypto and media links are omitted. Single-message sender display uses
the canonical account nickname/name rather than inventing a realm-specific
identity. Maximum encoded snapshot size is 2 MiB. Duplicate input IDs are rejected
rather than deduplicated, an explicit owned validation choice.

Attached messages, embedded resources, nested references/redirects and encrypted
messages/rooms are unsupported for redirect. They fail explicitly; attachments
are never silently dropped from a successful copy. The authored service copies
attachment URLs/IDs under source membership without a separate owner grant. That
is insufficient for the owned file policy. Reading a private file does not grant
redistribution rights. Independent attachment IDs supplied to an ordinary send
still require existing owner/public-file authority, current lifecycle and media
permissions. No arbitrary URL is fetched or ownership transferred. Encrypted
forwarding/threads remain unsupported; no envelope is downgraded into plaintext.

## Transactions, replay and schema

Cross-room operations validate the original bound authority proof before any
source-message discovery, then gather all associated accounts/realms before locking:
sorted canonical accounts, authority proof, sorted realms, then sorted rooms.
They compare associations after waits and revalidate current source/destination
admission after media/hydration work. Current account/session/scope, membership,
linked direct/realm policy, timeouts and scheduled restrictions remain enforced.
This is current transaction revalidation, not a new recent credential-proof
requirement. Opposing cross-room writes share the same lock order. Sequences,
messages, canonical file links and redirect receipts commit or roll back together.

An optional owned `Idempotency-Key` header is single-valued, has no leading or
trailing whitespace, and is at most 128 characters. Exact source-ID sets replay
one message for the actor/destination; different source sets conflict. Source and
destination membership ID/join-time incarnations must still match. Leaving and
rejoining does not revive a request receipt. Retries never overwrite the existing
snapshot with changed source text. Old plain-text send hashes are preserved;
new thread/reference fields participate in new request hashes.

Install the additive `internal/chatpg/schema.sql` change explicitly. Startup does
not create schema. `owned_chat_redirects` records canonical actor/source/target,
membership incarnations, exact source IDs, request fingerprint and snapshot. A
database trigger forbids receipt updates. `meta.redirect` is reserved, including
null, on user writes. Hydration returns a snapshot only from its matching durable
receipt and strips arbitrary historical redirect metadata without a receipt.
Destination message deletion still hides the copy. Notification projection remains
a content-free room/message invalidation; it does not copy source text/snapshots.

## Licensed source evidence and owned choices

Complete retained AGPL-3.0-only server files are pinned at DysonNetwork
`4ee2d0cebb9da8284a92483fee6dce795bf6947c`:

- `ports/messager/source/ChatController.cs`, blob
  `29e748e8c585ed766c9e20dddfc2eae65509f9a2`: request fields 355–383,
  send checks 610–854, redirect 967–1064, thread GET 1500–1601.
- `ports/messager/source/ChatService.cs`, blob
  `e23ae267ecc68bd6481108a51cefe073d6cf0cc5`: redirect builders 786–1065,
  edit relationship preservation 2240 onward, thread hydration 3204 onward.
- Solian `ffd0838e5f04cb40bea311a39118ebeedad210c1`, retained
  `ports/messager/contract-evidence/lib__chat__send__message_sender.dart`:
  plain send/edit envelope 706–774. `lib__data__message.dart` retains
  thread/reference IDs and thread-root projection. Existing manifests pin exact
  blobs, content hashes and licenses. No generated/excluded model is used.

The authored ordinary-reference send checks existence without source-read
admission; current source visibility is an owned privacy correction. The authored
redirect permits durable copies and broader metadata; this implementation keeps
the documented text/attribution subset. Strict thread paging, ancestry limits,
immutable receipts and the optional idempotency header are owned choices.

## Verification and integration registration

These new guarded cases seed random synthetic identities through canonical
fixture setup. They have no process-smoke seed dependency:

- TestPGChatThreadsRootReplyIsolationAndPagination
- TestPGChatLiveForwardVisibilityAndNestedThreads
- TestPGChatRedirectSnapshotProvenanceAndReplay
- TestPGChatForwardRedirectWaitRevocationAndRollback
- TestPGChatRedirectConcurrencyAndMembershipIncarnation
- TestPGChatThreadForwardMediaAndEncryptionBoundaries
- TestPGBackendChatThreadsForwardAndRedirectComposition
- TestPGChatForwardFileWaitCurrentSourcePolicy
- TestPGChatThreadsForwardRestartPersistence
- TestPGChatForwardSourceDiscoveryRequiresBoundProof
- TestPGChatRedirectPrecisionAndCanonicalReceipt
- TestPGChatThreadCountsMatchVisibleReplyBoundary

Require all twelve before restart, with zero skips. Run
`TestPGChatThreadsForwardRestartPersistence` again after restarting the same
disposable cluster. It saves `chat-thread-forward.json` via
the exclusive shared restart helper, compares exact typed public thread/copy
projections, and checks that a deleted source stays unavailable. The after path
only reconstructs adapters and reads saved rows; it does not reseed. The composed
host case exercises actual routing, scopes and authority. The file-wait case
uses a pre-scheduled source-participant restriction becoming effective during a
real PostgreSQL file lock, then checks rollback. Review cases additionally cover
missing bound proof before source discovery, sub-millisecond chronology and UUID
ties, unchanged receipt replay after edits, cached/forged snapshot metadata, and
root count agreement when legacy rows fall outside thread visibility.

Offline build, vet, ordinary/race tests, source integrity, PostgreSQL 16 SQL/body
syntax and static argument checks pass in this isolated implementation. Without
PG_TEST_DSN the integration cases compile and skip; this is not database execution.
Actual multi-connection PostgreSQL and restart remain pending private CI. Required
case registration metadata is delivered separately. Groups/workflows are unchanged;
aggregate acceptance stays blocked until all new names are assigned and the
restart pair uses the same actual cluster. No full parity/production claim is made.
