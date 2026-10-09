# Owned durable notifications and realtime

This addition is an independently authored notification runtime. It implements
durable inbox list/read/dismiss/unread, an idempotent local outbox consumer,
wallet and chat projections, leased external-delivery attempts, authenticated
WebSocket delivery, and durable HTTP/SSE replay. It does not implement Ring,
Blade gateway routing, upstream push providers, or whole-platform parity.

## Lawful evidence and contract boundary

Eight evidence/license entries are pinned and verified in
`ports/notifications/source/manifest.json`. Complete authored Solian source is
from `ffd0838e5f04cb40bea311a39118ebeedad210c1`; the limited authored Dyson HTTP
documentation excerpt is from `4ee2d0cebb9da8284a92483fee6dce795bf6947c`.
The pinned full Dyson root tree contains no notification/Ring controller or
service implementation. No excluded Shared/Spec protocol, descriptors,
generated binding, unlicensed gateway source or canceled repository is used.

Established authored-caller behavior:

- The inbox uses offset/take pagination and `X-Total`. Solian's watch calls
  `/metoer/notifications`; that exact historical spelling is an alias.
- Fetching a page marks that page read but returns each row's prior `viewed_at`.
  The authored inbox decrements its unread count using that snapshot.
- Authored HTTP documentation establishes `/api/notifications`, `/count`, and
  `/all/read`; authored callers establish tenant `dev.solsynth.solian`.
- The authored WebSocket client uses `/ws?namespace=dev.solsynth.solian`, a
  JSON `type`/`data` envelope, application `ping`/`pong`, and
  `notifications.new`. It supports bearer headers on native clients.
- The authored watch model establishes the notification projection's snake-case
  fields. `app_id`, `dismissed_at`, replay cursors and owned state transitions
  are explicit additions. Scalar count response and 204 write responses are
  owned choices; excluded SDK behavior was not inferred.

## Runtime and host wiring

Owned paths: `ports/notifications/`, `internal/notificationpg/`,
`internal/realtime/`, `tests/integration/notifications_test.go`, this document.
The only new module dependency is BSD-licensed
`github.com/gorilla/websocket v1.5.3`, pinned in `go.mod`/`go.sum`; its notice is
retained in `internal/realtime/GORILLA-LICENSE.txt`.

Construct `notificationpg.New(db, guard, app)` with the host's same-pool
`authoritypg.TransactionGuard`. Bind the original verified principal with
`guard.Bind` in private host middleware. Supply an `Identity.Account` adapter
that freshly checks that private proof. Missing guard/proof, actor substitution,
revoked/stale sessions, disabled accounts and different DB pools fail closed.
Every inbox read/write and every replay page authenticates again inside its
Read Committed transaction while holding the canonical account row lock.

`notifications.HTTP` receives that repository, identity and configured app.
`realtime.New` receives the same repository/identity plus `realtime.Config`.
Route realtime directly or through transparent transport wrappers: WebSocket
needs `http.Hijacker`; SSE needs flushing and write-deadline support. Do not hide
these behind the existing location-rewrite writer. Production host wiring is
owned by the parent integration, outside this patch's path ownership.

Canonical inbox endpoints:

- `GET /api/notifications?app=...&offset=0&take=8`: pre-view snapshot, atomic
  page-only read marking, `X-Total`; bounds 1–100 rows and offset 0–100000.
- `GET /api/notifications/count?app=...`: unread integer.
- `POST /api/notifications/all/read?app=...`: mark account/app rows read.
- Owned extensions: `POST /api/notifications/{id}/read` and
  `POST /api/notifications/{id}/dismiss`. Both are idempotent; foreign IDs 404.
- The evidenced `/metoer/notifications` alias is handled with the same policy.
  No public arbitrary notification-send or external-delivery endpoint exists.

Only the configured app is accepted. Identity/account overrides and query
credentials are rejected. Client web `tk=` authentication is deliberately
unsupported, consistent with the host's query-credential rejection; browser
clients use the host-owned POST /api/realtime/tickets adaptation described below.

## Durable storage and worker

Apply `internal/notificationpg/schema.sql` explicitly after the authority
schema. It creates `notification_v1.streams`, `inbox`, `outbox`, `events`,
`receipts`, and `deliveries`. Wallet/chat projectors additionally require the
existing wallet and chat schemas. Runtime startup executes no schema DDL.
This is an owned schema, not a legacy data migration. Rollback means stop the
worker and route mounting while retaining its data; no automatic destructive
rollback or legacy mixed-writer assumption is provided.

`Enqueue` is a trusted in-process API with a durable per-account/app source key
and SHA-256 content fingerprint. Repeating the same command returns its original
ID; changed content under the same key conflicts. `ProcessPending` atomically
creates the inbox row and its replay event with the completion receipt. Failure
or crash rolls everything back. Disabled recipients are suppressed.

Wallet projection consumes the existing actual `wallet_v1.outbox`. Participant
IDs come from canonical wallet/transaction SQL ownership, never payload claims.
Account-owned wallet events preserve the existing owned wallet payload. Wallet
transaction create/confirm/refund/expiry also create explicitly owned concise
inbox summaries for account participants. Realm fanout is intentionally omitted
pending an event-time/current-grant contract. `published_at` is not changed:
local projection receipts do not claim external publisher acknowledgment.

Chat projection consumes durable owned `chat_messages` rows and sends
`owned.chat.changed` with room ID, event message ID, sequence and mutation.
It intentionally carries no plaintext and is an invalidation/sync adapter, not
an assertion of full upstream `messages.new` compatibility. Only active joined
members present at the event are selected. Replay rechecks current membership,
ban/leave/deletion, room deletion, and join time; removed or rejoined accounts
cannot replay prejoin invalidations. Push-subscription, notify-level and
per-device upstream routing parity remains unimplemented.

Each event appends an account/app-local sequence while holding the recipient's
canonical account lock. Sequence assignment and commit are serialized, avoiding
the late-commit holes of a global SQL identity cursor. Source receipts are
per-source/event/app; projectors rescan unprocessed source rows instead of
advancing a global source cursor that could skip uncommitted events. Projection
is atomic and idempotent across crashes/replicas. Recipient lock order is stable.

`Worker.Step` processes bounded batches; `Worker.Run` polls once per second and
is context-cancelable. Transient local failures remain pending. For commands
explicitly requesting external delivery, `DispatchOne` claims a 30-second lease
and gives an injected `Sender` a 10-second deadline. Failure retries with
exponential 1-second through 5-minute backoff. Expired leases are reclaimable
after restart. Acceptance is acknowledged only with the matching lease ID.
The delivery UUID is stable across retries; adapters must deduplicate it because
crash-after-acceptance is at-least-once. Adapters must honor cancellation.

The default sender returns `DELIVERY_UNCONFIGURED`, saves no false success, and
continues retries. No external email/push/network provider, credentials or
recipient contact resolver is configured or contacted. Tests inject a synthetic
sender; persisted diagnostic codes contain no provider response or secrets.

## Realtime protocol and limits

- `/ws` implements the authored `type`/`data` envelope and `ping`/`pong` only.
  No `endpoint` service forwarding, websocket mutation, presence, duplicate-device
  semantics or push fallback is claimed. Same-origin Gorilla checks remain on.
- Owned `GET /api/realtime/replay?after=N` returns `{events,cursor,high_water}`.
  Owned `GET /api/realtime/events?after=N` provides SSE carrying the same packet
  JSON and SSE `id`. `Last-Event-ID` is accepted; conflicting cursors are rejected.
- WebSocket/SSE default to the current high water and deliver new events. An
  explicit `after` opts into durable replay. Reconnect after the last processed
  sequence; deduplicate sequences if reconnect follows uncertain receipt.
- The packet `sequence` extension is account/app-local. Hidden/dismissed events
  still advance the cursor; `owned.cursor` carries that advancement without
  hidden content. `owned.heartbeat` is a 20-second liveness signal. An invalid
  future/outside-retention cursor returns HTTP 409 before starting the stream.
- Every poll rechecks transactional authority; every event write additionally
  checks the private host identity. Revocation/expiry closes active connections
  on the next poll (default one second). Bytes already in flight cannot be
  recalled. A revoked reconnect returns 401.
- Defaults: 128 connections total, 4 per account; one 32-event page per stream;
  each durable payload at most 64 KiB; one pending incoming ping; incoming
  WebSocket frame limit 1 KiB; five-second write and 120-second read deadlines.
  Slow consumers stop fetching and are closed by the deadline, then recover
  through SQL replay. No per-account in-memory broadcast backlog is required.

Durable event retention is operator-owned and currently unbounded. There is no
automatic retention purge; backups, storage quotas and a future retention
policy remain operational work. Existing frontend reconnect and count behavior
has not been exercised end-to-end against this owned implementation.

## Verification

Locally passed: focused unit and race tests for notification HTTP scope/routes,
input validation, default delivery failure/backoff, pinned evidence, realtime
replay ordering, revocation, bounded writes, cancellation, and connection caps.
Full integration package compiles; actual PG/socket tests skip without the
marked disposable DSN. No local listener or production connection was started.

The approved disposable PostgreSQL/transport runner must require these cases:

- `TestPGNotificationsInboxOutboxIsolation`
- `TestPGNotificationsDeliveryRetryAndLeaseRecovery`
- `TestPGNotificationsConcurrentIdempotency`
- `TestPGNotificationsProjectionRollbackAndDisabledAccount`
- `TestPGNotificationsWalletInboxProjected`
- `TestPGNotificationsWalletAndChatProjection`
- `TestPGNotificationsWebSocketReplayAndRevocation`
- `TestPGNotificationsRestartPersistence`, in both `before` and `after` phases

The restart case reconstructs keys/verifier/repository after an actual database
restart, then verifies inbox rows, ordered replay and failed delivery recovery.
The WebSocket case starts its synthetic server only after `database(t)` verifies
the disposable cluster marker; it covers replay, reconnect cursor, ping/pong,
live revocation close and revoked reconnect. Actual CI outcomes must be recorded
against the combined commit; local package `ok` is not a PG/transport pass.

## Integrated browser transport

The authenticated host issues a single-use 30-second ticket from POST /api/realtime/tickets. Redeem at /ws or /api/realtime/events with ticket query parameter; raw bearer query authentication remains rejected. Tickets retain only a hash plus the original private proof in bounded process-local memory (256 total, four per account). They are checked against current authority at redemption and throughout streaming. Restarts invalidate outstanding tickets; replicas need connection affinity. Responses are no-store. The host transports WebSocket/SSE directly, preserving upgrade/flushing/deadline interfaces.
