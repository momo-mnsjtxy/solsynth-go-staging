# Chat subscriptions and synchronization continuation

Independently authored Go, AGPL-3.0-only, based on committed `e330428`.
Exact retained source hashes, revisions and line ranges are recorded in
`chat-subscription-sync-source-evidence.json` and checked by an offline test.
No excluded shared implementation, generated model, protocol, descriptor,
canceled repository, production data, provider or real credential was used.

## Runtime and source contract

The existing room service handles these exact paths, including its existing
`/messager/chat/...` host alias:

- GET `/api/chat/{roomId}/subscriptions`
- GET `/api/chat/accounts/me/subscriptions`
- GET `/api/chat/accounts/me/status`
- POST `/api/chat/sync`
- POST `/api/chat/rooms/sync`

Read DTO containers and JSON names follow the retained authored controllers and
Flutter caller. Nested room/member/message objects remain the repository's
explicit owned projections; excluded full shared DTO compatibility is not
claimed. `chat.sync` is rechecked for both POST routes. Sources show no paid
entitlement on these operations; the existing paid search remains separate.
Sequence recovery is room scoped: nonempty global recovery arrays fail 501,
instead of applying meaningless room sequence numbers globally. JSON binding
rejects malformed/duplicate/unknown keys and requires a nonnull integer
`last_sync_timestamp`. The source's `DeviceJoined` route is already owned by the
existing MLS dispatcher and is not duplicated.

All reads require the current original proof, live account/session and current
room membership. They reuse canonical ordered account/realm/room locks and
final proof/membership revalidation. Global messages use the existing same-pool
message hydration (reactions, threads, safe resource references). MLS ciphertext
is opaque throughout. Removed joined-room changes carry a room ID/type/time
only, with null room/member fields. Other members' room-subscription device lists
are empty and their private read markers are cleared. Only the current account
can read its own non-credential connection identifiers. No session IDs, refresh
bytes, access tokens, MLS secrets or authorization generations are serialized.

## Owned transport lifecycle

`rooms.ChatConnections`, version 1, is an internal transport hook. Organization
implements Open/Renew/Close, SetSubscription and CurrentConnectionDevice. Open
creates a random UUID handle bound to the original authenticated session,
account version, durable authorization generation and optional owned MLS device
selector. It is not a bearer token and grants nothing by itself. Every later
call needs the original same-session proof; renewal cannot revive an expired or
closed lease. Optional MLS selectors remain account-scoped capabilities, not
claims of cryptographic device possession, and must be live and unchanged.

Connections expire after 60 seconds, subscriptions after five minutes (the
retained subscription duration). A disconnect ends websocket status; its room
subscription expires independently. Expiration is enforced on reads, including
a final clock observation after lock waits. Joining again does not revive an
old membership lease: member ID plus joined-at incarnation must still match.
Normal access-token refresh preserves recorded subscriptions through
`AuthorizationEpoch`; revocation or a durable generation change invalidates
them. A freshly verified rotated token can renew the same session's connection.
A second session on the same account cannot reuse that connection.

The new recorded connection verifier is a narrow additional transport lane.
Ordinary downstream resource eligibility methods retain their previous
Type-0-only behavior. For delegated connections, canonical OAuth binding checks
current app, publisher/owner, consent version, secret and grant scopes, plus the
ordinary parent chain. This does not turn a delegated proof into a root proof or
authorize chat permissions. Unavailable binding dependencies fail closed.

The production realtime adapter opens and renews this durable connection lease
and accepts strict `messages.subscribe` and `messages.unsubscribe` commands for
the `DysonNetwork.Messager` endpoint. Commands bind the room subscription to the
exact current WebSocket connection. Closing or unsubscribing one connection does
not remove another device's subscription. Public read receipts are stored as
durable source events and fan out only through current room access and active
subscription checks; private stream rows contain references and are rehydrated
again immediately before transport writes. No provider contact is claimed.

## Cursor behavior and bounds

Global sync has a stable `(created-at millisecond, message UUID)` cursor and
500-message pages. The ordering and predicate use the same millisecond precision
as the authored client. Only completed millisecond buckets are eligible, avoiding
skips when another insert receives a lower UUID within the current millisecond.
An empty page advances only to the latest completed millisecond.

The legacy joined-room request has no tie-breaker. Both changes and summaries
share a safe watermark; a page contains every entry in the boundary timestamp
bucket even when this exceeds 500. A changed-candidate or bucket set over 10,000
returns `CHAT_SYNC_BUCKET_OVERFLOW` (409), without advancing the cursor. The
existing aggregate maximum of 1,000 active rooms remains explicit. Sustained
historic overflow cannot progress via the legacy timestamp-only request; an
adapted recovery client must rebuild its bounded current-room snapshot. No
invented continuation field is passed off as a supported legacy contract.

Unread and last-message calculations reuse the original summary implementation
inside the same transaction. Groups are current account-owned groups. Updates,
message changes and member removals remain persisted in canonical tables; no
in-memory repository is used as a database fallback.

## Verification and restart

Offline unit/contract tests cover source integrity, exact routing and JSON keys,
authority denial, strict inputs, WebSocket command binding, bucket ties/overflow,
private serialization and root/delegated eligibility separation. Actual-PG cases in
`chat-subscription-sync-integration-cases.json` cover 503 same-millisecond message
pages, 501-room ties, explicit historic overflow, current membership/privacy,
session refresh versus durable revocation, expiry, lock-wait proof revalidation,
delegated consent withdrawal, host aliases and saved-state restart.

Additional guarded PostgreSQL cases exercise two real WebSocket connections,
subscribe/status/unsubscribe persistence, cross-instance one-use browser tickets,
durable message/read fanout and withdrawal, redrive, retention floors and typed
fund/survey/meet/calendar/location composition.

The restart case saves an original one-hour proof and public verifier material
before creating its target state. The before fixture explicitly extends only
its synthetic lease TTL to 30 minutes so an aggregate suite can cross a real
PostgreSQL restart. After restart it verifies the same subscription rows,
message page and connection with that original proof before renewing, and
rejects missing/expired proof. It does not issue/refresh credentials or seed
replacement rows after restart.

Actual PostgreSQL and private-process execution belong to the coordinator's CI.
No local sockets, server, PostgreSQL process, production or provider action was
run by this worker. A compiled or skipped fixture is not passing database proof.
