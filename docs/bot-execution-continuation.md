# Canonical bot message delivery and bounded enrollment

This continuation keeps normal first-party message submission:
`POST /api/chat/{roomId}/messages`. Slash commands stay unchanged message content;
the configured bot adapter interprets them. The server does not invent a command
language, run a shell, impersonate a user, or report command completion from an
HTTP acknowledgement. The initial runtime supports explicitly enrolled bots in
owned, unencrypted, ordinary group rooms only.

## Sources and compatibility

`ports/develop/bots/execution-source-evidence.json` pins 13 reviewed retained
files to their exact repository revisions, Git blobs and SHA-256 values. The
review covered the entire authored BotAccountController, BotAccountService,
BotAccountPublicController, ChatController, ChatRoomController, ChatService,
Messager startup listener, authored Solian chat input and MessageSender, and both
bot API/migration documents. DysonNetwork is pinned to
`4ee2d0cebb9da8284a92483fee6dce795bf6947c`; Solian is pinned to
`ffd0838e5f04cb40bea311a39118ebeedad210c1`. Licenses remain retained and verified.
No unlicensed NeTo/Spec/Shared schemas, descriptors, generated bindings, SoSYS,
Socommon, mirrors or Suki content was used. No new upstream source was fetched.

The current authored code supplies:

- GET `/api/chat/{roomId}/bots/commands`, with dictionaries keyed by automation
  bot ID, separate from the canonical account ID
- Existing chat message submission and durable message identity
- Bot support-chat and subscribed-event configuration
- Canonical group owner invite authority, bot join/leave and auto-approval concepts

The older BOT_CHAT_API document supplies `bot.chat.message`, the message/room/
sender/body fields and the `X-Bot-Signature: sha256=...` HMAC header. Its obsolete
private routes, webhook configuration DTO and claim of an event listener are not
asserted as current-controller parity. The retained current startup code has a
bot configuration listener; it does not implement the documented webhook runner.
The current authored clients/controllers reviewed do not introduce a separate
command-execution request. Sending commands through the ordinary message path
therefore remains intact. The new provider payload is explicitly named
`owned-bot-message-v1`, with both `bot_id` (automation ID) and `bot_account_id`
(canonical account ID) rather than copying the legacy ambiguity.

## Owned enrollment contract

GET/PUT/DELETE
`/api/private/bots/{botId}/execution/{roomId}?dev={publisher-name}&proj={project-uuid}&app={app-uuid}`
is a new owned contract. PUT accepts only `adapter` and `expires_at`. The adapter
is a preconfigured registry name, never a URL, account selector, key or permission.
GET returns metadata; DELETE revokes execution and cancels pending deliveries.
It does not remove independent canonical room membership. PUT explicitly joins
or rejoins the bot's canonical account; a ban or active timeout cannot be lifted.

Enrollment requires current publisher editor membership, the exact app/project/
bot association, an active direct publisher owner, production app status, active
bot/account, support-chat enabled, the acting developer also being the canonical
room owner and a current room member, and both named permissions:
`developers.bots.execution.manage` and existing `chat.invites.manage`.
Read requires publisher membership and room ownership. Other current developer
editors may revoke an existing enrollment. No permission is implicitly granted.

Expiry must be in the future, within 24 hours, and no later than the issuing
session expiry. The issuing ordinary session, account version, authorization
epoch, publisher membership start, owner ID/version, bot account version, issuer and bot
room join times and immutable adapter binding are recorded. Matching PUT replay
under the same original proof leaves the generation and queued work unchanged.
Replacement increments the generation and cancels older pending work.

Plain group membership is intentionally stricter than permissive legacy public
command discovery. Anonymous/public-room command reads, DMs, realm-linked rooms,
MLS or other encrypted rooms are not enabled by this continuation. Their full
canonical policy and lock protocol must be integrated before adding them.
Existing bot key CRUD remains 501; no bot session, API key or wildcard is issued.

## Atomic delivery and current authority

The optional canonical chat observer queues message IDs and original authority
snapshots inside the message transaction. Failed enqueue rolls back both message
and sequence allocation. Existing message replay does not enqueue again. Encrypted
content, edits, forwards, placeholders, and system events are not newly subscribed
through this text-send hook. No adapter is called during message submission.

Queueing requires configured `messages.new` subscription and current bot room
membership matching the enrollment. Every eligible new text message is offered;
there is no guessed slash-command parser. Only original text content and canonical IDs/timestamps are delivered; arbitrary
metadata, embeds, attachment associations and reply/forward references never leave
through this projection. Forward references and embedded encryption metadata are
rejected. Payload data is reloaded from the original stored message, and a deleted or edited message is cancelled before
transmission. Stable event IDs permit external deduplication.

The worker locks the complete sender/enroller/old-owner/current-owner/bot account
set in sorted order, then publisher/member, project/app/bot, room, enrollment,
message and outbox. NOWAIT avoids inversion with canonical mutation paths and
other workers. It checks fresh ownership, production ancestry, account versions,
disable punishments, issuer publisher membership generation, sender/bot room join
generations, current support/subscription, enrollment generation/expiry, and the
original sender and enroller sessions with exact current scopes. It preserves
per-bot/per-room event order during retry backoff. Revocations and changes cancel
pending work before provider access. The supplied publisher-owner prerequisite,
including rejection of imported organization app ancestry, remains intact.

The worker commits a bounded lease, then rechecks the complete current authority,
membership generations and original content in a second transaction immediately
before dispatch. Both transactions commit before calling the adapter. SQL account,
room and outbox locks are never held during external I/O. A lease uses an opaque
owner UUID and monotonically increasing attempt number; acknowledgement must
match both, pending state and an unexpired lease. Stale workers cannot overwrite a
new claim, acceptance or enrollment cancellation. Abandoned claims are reclaimable
after at most SendTimeout plus five seconds of SQL acknowledgement grace, capped
by the original event and enrollment expiries. Sending itself is limited to
SendTimeout and the remaining lease/authority expiry.

The final committed authority check is the dispatch observation boundary.
Revocation committed before that check prevents transmission. Revocation after
that check can proceed during external I/O and cannot retract an already in-flight
request. Provider acceptance is not strictly serialized with revocation. Rechecks
on retry always use current state; no cached proof extends authorization.

`accepted` records provider acknowledgement, never command completion. Provider
errors and timeouts are explicitly ambiguous: the provider might already have
accepted the event. SQL failure after acceptance leaves the durable lease for
recovery. Every retry retains the same event ID and payload. The transport sends
an Idempotency-Key; the receiving adapter/provider must actually deduplicate it.
Without provider deduplication, duplicate acceptance/execution is possible. There
is no exactly-once guarantee.

Attempts count committed dispatch claims, including claims whose worker crashes
or cannot complete its second authority observation. They are bounded by
MaxAttempts; errors use exponential backoff capped at a minute, and expired
abandoned claims count toward the same bound. Exhaustion is marked failed and
ambiguous. Revoked, changed or expired work becomes cancelled. Unconfigured
adapter names remain durable for their configured worker. Reconfiguration of an
adapter's bot/destination/key binding cancels old work instead of redirecting it.
The processing count includes accepted or cancelled records, never command results.

There are at most 100 enrolled targets considered per message and an explicit
per-room pending limit. Exceeding the limit fails message enqueue atomically; it
does not silently lose the promised delivery. An operator should monitor failed
and pending outbox records and explicitly decide whether to re-enroll or retry.
There is no automatic deletion or manual replay. Enrollment renewal runs only for
an explicit persisted consent bound to the original enrollment generation,
ordinary session/account version, publisher owner/member rows, room memberships,
bot version, adapter binding and MLS device eligibility. Each consent has a
caller-selected 5 minute to 24 hour extension, a 1 minute to 1 hour lead, and an
absolute deadline no later than both the authorizing session and seven days after
the request. The worker never extends that deadline. Expiry or any authority,
membership, adapter, room, bot, application or MLS-device revocation permanently
closes the consent; restoring the row cannot revive it. Re-enrollment and manual
renewal also close the prior consent, so a later enrollment must opt in again.

## Host integration

Apply `botpg.ExecutionSchema` explicitly after authority/accounts, developer apps,
bots and canonical chat schemas. No runtime constructor applies DDL. This is an unreleased initial schema: apply the reviewed ExecutionSchema to the
fresh disposable database, including issuer_room_joined_at and lease columns.
An earlier candidate schema is not a production migration baseline. The new
schemas are `owned_bot_enrollments`, `owned_bot_enrollment_renewals` and
`owned_bot_message_outbox`.

Construct adapters with immutable names, exact canonical automation bot IDs and
nonsecret binding identifiers. `internal/botdispatch.New` offers an optional HTTPS
transport using operator-supplied endpoint and signing-key bytes. It copies the
key/client and standard HTTP transport configuration, rejects credentials/query/fragment in endpoints, blocks
redirects, bounds requests/responses and signs exact JSON bytes. Its binding ID
changes when endpoint, bot or signing key changes. Arbitrary injected RoundTrippers are trusted host dependencies that must remain
immutable. Registry identity is snapshotted and detected identity drift fails
closed. The embedding host controls network destinations; no user API can configure arbitrary egress. No provider,
credential, endpoint or permission is supplied by default.

For the combined host, set `application.Config.BotExecution` with its Runtime and
Adapters. Runtime NoneEncryptionMode must match the canonical Messager policy.
The host installs the text observer, dispatches the source route for direct API
and Messager gateway paths, checks schema readiness, and invokes bounded delivery
from ProcessDue. `ProcessBotMessagesDue` is also available for an explicit worker.
No CLI secret environment parser or default deployment is introduced.

For explicit module composition:

```go
if err := botRepo.WithExecution(config, adapters...); err != nil { return err }
chatRepo.WithTextMessageObserver(botRepo)
if err := botRepo.ExecutionReady(ctx); err != nil { return err }
renewed, renewalErr := botRepo.ProcessEnrollmentRenewalsDue(ctx, 100)
processed, err := botRepo.ProcessMessagesDue(ctx, 100)
```

`WithExecution` and the chat observer setter are initialization-only. The bot and
chat repositories must share the exact canonical pool/guard/verifier. Bound
ordinary proof is required even for direct repository calls; stored reconciliation
snapshots cannot authenticate a new request or mint a credential.

## Verification and remaining gates

Synthetic in-process HTTP transport tests cover HMAC body bytes, bot binding,
key/destination binding changes, stable idempotency headers, redirect refusal,
provider errors and malformed configuration. Route and retained-source-integrity
tests cover canonical auth, cross-service routing and pinned source hashes.

Required actual PostgreSQL cases, before restart:

- TestPGBotExecutionCanonicalSendAndReplay
- TestPGBotExecutionEnrollmentScopesAndHTTP
- TestPGBotExecutionFreshRevocationAndMembership
- TestPGBotExecutionRetryOrderingAndAtomicRollback
- TestPGBotExecutionConcurrentWorkersAndRevocationWait
- TestPGBotExecutionFailureExpiryAndLateCommit
- TestPGBotExecutionRestartPersistence
- TestPGBotExecutionMetadataAndCanonicalRollback
- TestPGBotExecutionLeaseReclaimAndStaleAcknowledgement
- TestPGBotExecutionTimeoutAndRevocationDuringDelivery
- TestPGBotExecutionEnrollmentRoleAndSessionScope
- TestPGBotExecutionRechecksAfterContention
- TestPGBotReplyKeyMutationAccountBeforeKeyContention
- TestPGBotEnrollmentAutomaticRenewalPolicyHTTPAndWorker
- TestPGBotEnrollmentRenewalRevocationIsTerminal
- TestPGBotEnrollmentRenewalConcurrentWorkers
- TestPGBotEnrollmentRenewalRechecksMLSDevice

After restart: TestPGBotExecutionRestartPersistence. It saves original pending
message/event/account IDs in the disposable runner root and requires the later
process to deliver that exact saved message. It also requires a different process
ID and a later PostgreSQL postmaster start time, so relabeling the same running
process/server as an after phase cannot pass. It does not recreate an enrollment,
issue a session, generate substitute data or reduce the check to schema presence.

Local build/vet/normal/race and offline SQL validation are recorded with the
handoff. Actual PostgreSQL execution, observed locking behavior, and the real
before/after server restart remain mandatory external-runner gates; they were not
run in this socket-restricted task. No live endpoint, production account, deployed
credential, provider call, socket or remote write was used. No frontend was changed.
The owned runtime includes bot API-key rotation/revocation, purpose-specific bot
replies, direct/realm/opaque-MLS delivery, and bounded automatic enrollment
renewal. It does not claim whole-platform parity.


## Standalone opt-in configuration

The backend requires both `--enable-bot-execution` and an absolute `--bot-execution-config` JSON file. Version1 requires `allowed_origins`, `send_timeout_ms` (1–30000), `event_lifetime_seconds` (1–86400), `max_attempts` (1–20), `max_pending_per_room` (1–10000), and 1–128 adapters. Each adapter contains exact `name`, existing `bot_id`, canonical HTTPS `endpoint`, and absolute `signing_key_file`. Keys are existing raw32–128-byte files, never inline values; no account, enrollment or credential is created by configuration. Missing/unknown/duplicate/null fields, repeated bot/name/origin entries, noncanonical URLs, unsupported ports, symlink files and oversized files fail closed with generic errors.

Origins are explicit canonical HTTPS origins ending in `/`; only port443 is accepted. Construction performs no network I/O. The standalone transport rejects redirects/proxies, verifies all resolved addresses are public on every attempt and dials one validated address directly with the original TLS hostname. Hostname changes and mixed public/private DNS answers fail closed. Tests use synthetic resolvers/RoundTrippers and inert temporary key bytes only; no live destination is activated.
