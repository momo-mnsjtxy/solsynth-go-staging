# Owned opaque MLS delivery and chat transport

This optional backend implements authenticated account/device delivery, canonical
room binding, delivery epochs, and generation-aware reset. It does not implement
an MLS cryptographic engine. The default runtime remains disabled. Enabling it
is not evidence that an unmodified client has completed an MLS handshake.

## Installation and evidence boundary

Install `internal/chatpg/mls_schema.sql` explicitly after the owned authority,
chat, and chat-relations schemas. Startup only checks relations; it does not run
migrations. Enable the owned capability with `application.Config.EnableMLSDelivery`
or the backend's `--enable-mls-delivery` flag. The configured plaintext mapping
must differ from the authored MLS room value, 3. A missing transaction authority
still denies operations; synthetic constructors exist only for test fixtures.

The five complete Solian source files at
`ffd0838e5f04cb40bea311a39118ebeedad210c1`, three complete DysonNetwork documents at
`4ee2d0cebb9da8284a92483fee6dce795bf6947c`, and their licenses are retained under
`ports/messager/mls-evidence`. Both manifests retain the original whole-file
SHA-256, Git blob SHA-1, byte count, and source URL. The source-evidence gate
checks all of them, including license hashes. This extends the earlier
`CONTRACT-AND-STATE.md` design with implementation, without changing that retained
report. No excluded schema/generated binding was required.

This patch follows the unchanged rich-chat patch
`ce25a11b6aa9ae3c373a569b00192fe5e1f8ba459387f72b7d1ba9decd575b34`
and caller compatibility patch
`00752b027e9d75d635a6821a3fcb8b9ca8203d0f3fb185477135b7c8bf88196d`.
It must be applied as a bounded patch, not as a replacement for a newer parent
checkout containing unrelated work.

## Caller routes and owned behavior

Stargate control routes accept `/e2ee/mls/...`, `/stargate/e2ee/mls/...`, and
`/api/e2ee/mls/...` aliases through the same handler and authority. Room routes
retain `/api/chat/...` and `/messager/chat/...` aliases. The retained
`CONTRACT-AND-STATE.md` lists the exact snake_case request maps.

- Key packages: upload to `PUT devices/me/kps`; status at `GET kp/status`;
  readiness at `POST users/ready/batch`; own device revocation at
  `POST devices/{device}/revoke`. The verified account owns the device forever;
  re-upload cannot revive a revoked device or move it to a different account.
- `GET keys/{account}/devices` consumes one available package per active device
  by default. Actor and target account locks serialize concurrent claims. The
  opaque result, consumed timestamp, and claiming account are committed together.
  Re-uploading an already-consumed digest does not replenish it. A failed network
  response can lose the claim; the authored caller has no claim idempotency key.
- Package reads are limited to oneself or accounts sharing current readable room
  membership. The recovery compatibility route accepts a device ID in place of
  the account ID only when that device belongs to the caller. Both `key_package`
  and the recovery helper's `key_package_bytes` are returned. It does not turn an
  arbitrary device ID into cross-account enumeration authority.
- The readiness response uses owned snake_case entries `account_id`, `is_ready`,
  and `available_key_packages`. The caller only establishes the enclosing `users`
  array; the exact counter projection is an owned documented choice.
- `POST chat/{room}/mls/enable` authorizes the canonical room's owner/direct
  member/realm moderator under existing room policy, binds a unique group ID,
  and sets mode 3. Only the evidenced standard `ciphersuite` policy is accepted;
  arbitrary secret-bearing policy fields are unsupported. Existing plaintext
  history is not re-encrypted. Enabling is irreversible through the room API.
- `POST groups/{group}/bootstrap` atomically reserves the bootstrap device.
  Competing callers receive the durable winner, not a second reservation.
- `GET/PUT groups/{group}/groupinfo` stores and returns opaque public GroupInfo
  and ratchet tree for the exact current delivery epoch. Every accepted commit
  invalidates the previous snapshot. A current canonical member with a registered
  device can obtain a ten-minute joining grant, matching the authored sequence
  that fetches GroupInfo before reporting joined state.
- `POST groups/{group}/commit/fanout` accepts only the next delivery epoch, with
  immutable request-digest retries. A joining device may submit one current-epoch
  external transition while its grant is valid. Accepted delivery activates its
  server membership; this is not cryptographic proof that the client merged it.
- `POST groups/{group}/commit` reports an already accepted epoch. It cannot
  advance delivery state without an accepted commit envelope. The retained old
  reshare helper attempts a bare epoch report without broadcasting its commit;
  that incomplete sequence remains rejected rather than fabricating delivery.
- `POST groups/{group}/welcome/fanout` accepts only registered, non-revoked target
  devices belonging to the named current room member. An outsider target aborts
  the entire request. `POST messages/fanout` accepts the authored `room_id` plus
  encrypted `payload` map, not a guessed recipient-array schema.
- `GET envelopes/pending` returns ordered device-specific envelopes; `POST
  envelopes/{id}/ack?deviceId=...` acknowledges only that same account/device's
  row. The query selector must agree with the header. Outer type mappings are
  authored welcome=5, commit=4, application=6, proposal=7; opaque header-local
  type numbers are never parsed as these values. No proposal submission route
  was invented from the receiving enum.
- `POST chat/{room}/devices/me/joined` accepts the body's `mls_device_id` without
  a header, as the caller actually does. If a header is present, they must agree.
  Current room membership, device ownership, a bootstrap/Welcome/accepted external
  commit grant, and the current delivery epoch are all required.
- Recovery request/list/complete routes maintain per-device recovery flags.
  The older room-upgrade reshare body is accepted only for the same canonical
  room/group, epoch zero, named current member, `target_device_id:"all"`, and
  `reason:"room_upgrade_to_mls"`, under room-management authority. These flags
  do not generate cryptographic Welcomes or replace client-side recovery work.

## Authority, isolation, and ordering

Bearer verification and transaction authority establish the account. `X-Device-Id`
is an owned account's device selector, never a secret or proof of signing-key
possession. Every control operation rechecks fresh account state. Room operations
lock the complete canonical account set in the existing order, then realm and
room state, and reject association changes after a wait. Devices and groups are
locked within that transaction before mutating delivery state.

A group ID has exactly one canonical room. Device memberships and envelopes bind
the canonical member row and its joined timestamp. Leaving, removal, revocation,
blocking, disabled accounts, and membership reincarnation are rechecked on
fanout, pending reads, and acknowledgments. Leaving and rejoining cannot revive
queued bytes from the previous membership. Reset removes the previous generation's
memberships and queues atomically. This withdraws server delivery; it does not
revoke knowledge or keys already held by a former recipient.

Fanout validates all recipients before delivery, then allocates per-device
sequence numbers and inserts the entire batch plus its immutable request receipt
in one transaction. An overflow, queue bound, stale epoch, or later target failure
rolls back the complete batch. Restarts read the same unacknowledged records and
sequence order. Matching retries do not deliver twice or advance the epoch again;
changed request bodies under a reused client ID conflict.

## Reset contract for generation-aware clients

The authored reset implementation deletes its local state, posts epoch zero, and
reuses the same group ID. Bootstrap always sends state_version 1, and later
ciphertext maps carry no reset generation. It therefore cannot distinguish
successive epoch-zero groups at the delivery boundary.

The owned reset endpoint is implemented behind explicit per-request opt-in:

1. Send `X-MLS-Reset-Capability: generation-v1` and
   `X-MLS-State-Version: <current generation>` to `POST groups/{group}/reset`.
2. The body supplies `new_epoch:0`, `state_version:current+1`, and a bounded reason.
   Current room-management authority and an owned active device registration are
   required. A lost cryptographic membership does not prevent authorized reset.
3. Reset atomically increments the generation, clears GroupInfo, memberships,
   queued envelopes, and bootstrap reservation, and returns the new state.
4. Bootstrap the same group ID with both the new state_version body value and
   the new generation header. Every subsequent group write, joined/recovery
   completion, envelope acknowledgment, and canonical ciphertext mutation must
   carry `X-MLS-State-Version` for the current generation. Old or missing values
   fail. Pending and GroupInfo responses expose state_version so new clients can
   discover it. GroupInfo reads may create only the bounded joining grant.
5. Canonical encryption_meta responses also include state_version. If an incoming
   metadata object includes it, it must match the transaction's current state;
   this does not replace the required post-reset header.

Generation-one clients preserve the existing request shape. Legacy clients cannot
opt into a reset that would leave them unable to use the new state. A stale reset
retry conflicts after another reset has committed. These are authenticated
transport preconditions. The server cannot inspect opaque bytes and prove they
belong cryptographically to the advertised generation, detect a malicious client
re-labeling bytes, or validate an MLS membership transition.

## Canonical messages, attachments, and notifications

Canonical send/edit uses the same room/group epoch snapshot as message mutation
and sync-event append. Only `chat.mls.v2`, a current delivery epoch, bounded
canonical base64 ciphertext/header/signature, and reference-only metadata are
accepted for an encrypted write. Only the owned text message type is supported. Plaintext content, local plaintext_envelope,
embeds, unknown private fields, and unsupported forwarded/fund/survey references
are rejected. A plaintext room cannot accept encryption metadata.

The retained message wrapper does not add `X-Device-Id`. These writes therefore
require the verified account to have a current active registered MLS membership;
a supplied device header narrows that requirement and is checked. There is no
claim of cryptographic device possession. A future stronger possession protocol
would require client work. The authored DELETE caller sends no body, while the retained controller requires
an encryption epoch even for delete. The owned endpoint keeps the stronger
contract: an encrypted deletion must include encryption_meta with a current epoch
and may include client_message_id or nonce. It appends an opaque encrypted
tombstone atomically. Bodyless encrypted deletion is rejected until a later
client correction; ordinary plaintext deletion keeps its prior behavior.

Owned attachment links keep canonical file ownership and current room-access
rules. Public MLS rooms do not grant anonymous access through a private attachment
link. The server neither decrypts attachments nor derives keys from public IDs.
The retained client's attachment-key derivation has no secret input; its remedy
is deferred frontend work. This backend does not establish end-to-end attachment
secrecy or guarantee compatibility with every legacy encrypted-upload metadata
field. Previously public files remain governed by their explicit public setting.

Chat notifications contain only canonical room/message IDs, sequence and mutation
kind. No ciphertext, plaintext body, local envelope, filename, attachment key,
GroupInfo, or key package is added to notification previews. The existing pure
notification-text helper already returns the generic encrypted-message label.

## Explicit resource limits

Owned policy currently allows 32 registered devices per account including revoked
records; 100 available and 4096 lifetime key packages per device; 16 KiB decoded
packages; 64 KiB ciphertext; 4 KiB headers/signatures; and 128 KiB each for GroupInfo
and ratchet tree. Group operations cover at most 256 canonical members and fanout
at most 256 active recipient devices; each Welcome request covers at most 32.
Request receipts are bounded at 100,000 per group across generations.

Joining grants expire after ten minutes. Welcome grants and queue retention are
seven days; packages expire after thirty days. Pending returns up to 100 ordered
records and subsequent reads advance after acknowledgment. Each device queue is
bounded at 2048; exceeding a limit aborts the transaction. Acknowledgments are
idempotent while their retained row exists. A later fanout removes acknowledged
or expired queue rows. Retention expiry can require client recovery; these are
explicit delivery-service limits, not cryptographic guarantees.

## Verification and outstanding proof

Locally passed: full `go test ./...` (database-dependent tests skipped), focused
MLS unit/HTTP/route tests, whole-file/license evidence checks with mutation and
missing-file negative controls, and the new SQL schema parsed with pinned
PostgreSQL-16 grammar. Seven `TestPGMLS...` integration tests compile and are included
for the marked private disposable PostgreSQL runner. They cover actual caller
maps, canonical backend authority, direct-room block checks, epoch transitions,
repository reconstruction ordering, concurrent one-use claims, atomic fanout rollback, joining
expiry, reset replay rejection, membership reincarnation, device revocation,
plaintext rejection, and canonical message mutations.

Actual PostgreSQL execution, concurrent transaction outcomes, and an authored
client cryptographic handshake have not yet been established by this worker.
No production service, provider, credential, or real ciphertext was used. The
parent's private runner must establish the transaction cases before deployment;
frontend integration must establish generation-aware reset and real MLS behavior.

The separately composed `TestPGMLSRestartPersistence` writes exact synthetic queue/device IDs in the before phase and reads them after the runner stops and restarts PostgreSQL. It verifies retained ordering, one-use package consumption, device revocation, commit retry and acknowledgment. The older lifecycle case only reconstructs a repository on the same pool and is not counted as database-restart evidence. Actual execution is still pending for this checkpoint.
