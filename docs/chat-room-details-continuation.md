# Chat room details continuation

The six pinned authored operations now have owned implementations, plus the `slug`
field on existing room creation/update. Exact source pins and boundaries are in
`chat-room-details-provenance.json`. The existing versioned room/member projections
remain the transport contract; this does not claim excluded legacy DTO parity.

Room slugs normalize to URL-safe lowercase text, with explicit empty-string clear
and null/omission retention. Live owner name precedes live realm slug lookup.
Unique active-room indexes enforce owner/realm scope under concurrent creation,
rename and realm transfer. A failed transfer rolls back both room and realm link.
Private, encrypted, deleted, disabled and inaccessible public aliases return 404.

Member profile PATCH accepts only `nick` and requires `chat.members.manage`, active
membership and a current canonical level-2 paid internal subscription selected by
an explicit operator mapping. DELETE clears Nick without a paid entitlement or
manage scope. Both retain original authority proof and room/realm transaction
locks, and atomic audit failure rolls back the member update. No media-field or
URL extension is inferred from the nickname-only authored request.

Online counts and displayed accounts use canonical presence privacy: hidden or
invisible accounts, bilateral blocks, disabled accounts and stale/revoked sessions
do not contribute. Direct rooms expose only the canonical related-member status.
There are no remote calls, sockets, or provider side effects.

The legacy `/rooms/{roomId}/messages/{messageId}` delete and duration-based timeout
route are separate guarded realm moderation operations. They preserve stronger
owned global permission, membership, timeout and role checks. Deletion uses the
existing sync event model and canonical attachment removal in the same transaction;
audit stores the supplied reason. No encrypted moderation event is invented.
Legacy remote push delivery remains outside this continuation.

The existing explicit chat schema installation installs the additional nullable
columns, two scoped indexes and detail audit table. Startup never applies DDL.
A schema reinstallation is idempotent. Old rows stay readable, and existing realm
rooms acquire their authoritative scope key when updated; no names are copied.
Mixed old/new writers that independently mutate the owned realm/slug columns are
unsupported.

Verification includes strict authored HTTP shape, anonymous proof downgrade,
Unicode bounds, original transaction preflight, scoped rename/transfer rollback,
canonical paid entitlement denial/expiry, presence privacy and lease expiry,
moderation authority/audit, concurrency and original-state restart fixtures.
Actual PostgreSQL execution belongs to the coordinator's guarded private CI;
local build/compile/SQL grammar checks do not constitute a database pass.

## Independent privacy and transaction review

The detail read admission now loads only bare metadata and the canonical realm
identifier. Online responses never invoke room media or nested realm/member
hydration. Public alias responses acquire the room lock, reload its current
association, acquire nonblocking owner/realm locks, and only then hydrate. The
same account-first alias resolver must still select that room after hydration;
renamed/recycled identities never retain an earlier alias. The existing bilateral
owner-block gate remains specific to the public alias boundary.

Online account hydration precedes one final canonical presence batch statement.
That statement evaluates the database clock after its relation locks, applying
current privacy, bilateral friendship/block state, custom-status expiry,
invisibility, disabled/punished accounts and lease/session epochs/expiry together.
Thus a table wait cannot retain an earlier Go observation time for an expired
member. Direct-message status retains the canonical owned status shape; online
account payloads omit private birthdays, session details and activities. Failed
online reads discard any partially assembled response.

The existing thirteen PostgreSQL cases now also cover a projection table wait
across lease expiry, exact canonical direct status, single-connection reads,
recycled account names, realm-owner changes, missing media on inaccessible reads,
ordinary message-delete ownership, and exact audit-failure rollback. Original
proof checks include disabled accounts and expiration during an observed account
lock wait. The expired-presence setup advances last_seen_at with expires_at so it
respects the actual lease constraints. Subscription NOWAIT conflicts return the
owned retryable conflict response. These are authored acceptance cases, not a
claim that their PostgreSQL execution occurred during this offline review.
