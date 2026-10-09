# Chat summary, read-all, search, pins and personal groups

This slice adds the authored summary/read-all/search, room pins and personal
folder routes to the existing `/api/chat` host and its gateway aliases. It uses
canonical account/session authority, chat membership and relationship state;
new tables are installed explicitly from `internal/chatpg/organization_schema.sql`.
No startup DDL or alternate identity header is accepted.

Summary retains the room-ID dictionary with `unread_count`, `has_unread` and
`last_message`. Muted unread dots remain independent of notification counts.
Deleted message bodies are excluded even though the source last-message helper
ignored filters. Aggregate operations lock sorted accounts, realms and rooms,
recheck association and membership sets after lock waits, and revalidate the
original proof before returning or committing. A changing membership set returns
a retryable conflict. The owned resource bound is 1,000 joined rooms.

Read-all advances only current readable active memberships. It stores one latest
receipt per member atomically with the cursor updates. It includes observed
future-dated message times so a blocked subsequent send remains unread. Durable
receipts survive restart. External websocket fanout and subscription/device
status are not implemented by this slice and are not claimed as completed.

Search returns the authored room/message groups and X-Total, ordered by descending
message time and ID with global offset/take pagination. It searches current joined
rooms and unencrypted text only. `%`, `_` and backslash are literal text (explicit
hardening from source ILIKE wildcard behavior). Sender matches current canonical
UUID/name/nick locally; remote account-search ranking is not reproduced.
`--chat-search-perks='{"operator-configured-system-plan":1}'` provides the explicit
system-subscription/perk mapping. The canonical commerce adapter validates paid,
active, non-testing, current row/document state and enabled internal definitions
inside the same guarded transaction and again before returning. Each entitlement
pass evaluates a fresh database clock after all subscription and definition lock
waits, including paid cycles that begin between the initial and final passes.
Request fields,
custom app subscriptions, unpaid orders, disabled definitions and queued grants
do not confer search rights. An omitted mapping returns unavailable; an explicit
empty map denies entitlement. There is no guessed production identifier mapping.

Pins require current room membership plus owner rights for ordinary groups,
current realm moderator-or-owner status for realm rooms, or membership for DMs.
The `chat.pins.manage` owned permission is independently enforced. Pin/unpin and
`messages.pinned`/`messages.unpinned` sequence events commit atomically. Live
uniqueness serializes duplicate races; expiry hides a pin unless `includeExpired`
is true. Pin expiry accepts the authored client’s Unix-millisecond integer as
well as an ISO timestamp. Tombstoned messages never appear in pin listings. Pin DTOs intentionally
omit the message body, matching the authored JsonIgnore. The pin author projection
removes private read and moderation state. Public reads never downgrade a revoked
or supplied invalid proof to anonymous. Pin removal tombstones the pin; explicitly
pinning the same message again creates a new pin ID.

Personal group fields derive from authored controller requests and the retained
Messager EF snapshot. The group service implementation is not retained, so default
ordering (append max order + 1), null-patch-as-no-change, deterministic ties and the
1,000-group bound are explicit owned behavior. Group rows and memberships are
account-owned with a composite owner FK. Deletion clears assignments atomically;
assignment changes affect only the caller's current membership. GET self-membership
includes `chat_group_id` and `chat_group`. Other members never expose these private
fields. The upstream joined-room sync group/member response remains a separate gap.

The retained runtime JSON objects remain the repository's documented owned room,
member and message projections; full upstream entity serialization parity is not
claimed. Source field shapes, expiry selector spelling, route methods, request
limits and event type strings are covered by retained evidence hashes and HTTP
contract cases. The explicit owned permission keys are `chat.read.all`,
`chat.pins.manage` and `chat.groups.manage`; update/delete/move groups follow the
source's authentication-only permission requirement.

Validation includes offline HTTP/strict parsing, proof-before-domain SQL guards,
configuration validation, maintained SQL parsing, Go compile/vet/race checks and
nine compiled actual-PG test cases. Actual database, lock/race/rollback and restart
results are pending disposable PostgreSQL execution. Ordinary package `ok` with
no PG_TEST_DSN is not a database pass. The restart case saves original public keys,
pre-target one-hour proof, rows and response state before restart; the after phase
reuses that proof with elapsed wall time, requires original expiry enforcement,
and does not issue/refresh credentials or reseed target state.

Rollback is an application rollback leaving additive tables and canonical
`messages.pinned`/`messages.unpinned` history intact. Do not drop organization tables
without exporting personal groups, assignments, pins and read receipts. Older
applications ignore these additive records. There is no mixed legacy-writer
compatibility claim; coordinated owned writers share the documented lock protocol.
