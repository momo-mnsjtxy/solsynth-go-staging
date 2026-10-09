# Friend overview

This AGPL-derived operation follows the complete authored FriendsController at
DysonNetwork `4ee2d0cebb9da8284a92483fee6dce795bf6947c` (blob
`ea1b00027cce94342ac274164c01c71055b1a3a8`) and the complete licensed Solian
friends_overview.dart at `ffd0838e5f04cb40bea311a39118ebeedad210c1` (blob
`f9b188460193a9902a9c9c631d1d70900b2a3da3`). Retained files and license hashes
are in source/manifest.json. No excluded Shared/protocol implementation,
Socommon, Suki, provider integration, canceled retrieval, or fortune prose was
used. Existing canonical account, social, presence and reviewed activity ports
remain the data sources.

GET `/api/friends/overview` and `/passport/friends/overview` require the current
original bearer proof. The client shape is an array of objects with `account`,
`status`, and `activities`. Empty activity collections are arrays. The canonical
public account profile is used, with an empty profile object when absent so the
authored caller can safely access profile fields. No owner birthday, contacts,
device/session identities, credentials, nested catalog object, or private
activity document is synthesized. Profile artwork stays null because the
canonical transaction account projection does not hydrate media.

The default selects friends who have visible online presence or visible live
activities. When that set is empty, it falls back to friends with an authorized
status update in the last 24 hours. `includeOffline=true` adds that recent set
even when others are active; it does not reveal every historical friend.
Visibility is evaluated before fallback, paging or count. Invisible accounts
are absent. A hidden status cannot affect inclusion or reveal its timestamps,
label or metadata through a default status. Explicit private activities cannot
affect inclusion, nested contents, artwork, tags or count.

The owned consistency boundary is one read-committed transaction. Discovery
checks the original authority without locks, acquires all candidate account
locks in canonical sorted order, and rechecks authority and accepted bilateral
friendship. It excludes effective blocks in either direction, expired or
one-way friendships, disabled accounts and current disable punishments. Source
activity sessions retain their recorded account version and durable
AuthorizationEpoch; current source scope/session eligibility is checked after
all artwork waits. Ordinary access refresh does not rewrite that admission.
Final checks reuse the original proof. Reads cannot renew a lease or create an
event. Canonical artwork ownership, availability and retention checks run twice
with the complete lock set, so an earlier reference cannot survive a later wait.
After all status/profile hydration and the final original-proof check, a fresh
database clock removes newly expired activities before choosing the recent
fallback, computing the visible count, or slicing a page.

The authored operation is unpaginated. Owned `offset` and `take` extensions
provide deterministic account-ID ordering and a visible-only `X-Total`; take is
1–200 and offset 0–100000. Unpaginated results preserve the authored list shape.
At most 1000 friend candidates and 1000 potentially visible activities are
materialized; larger requests fail closed with 503 before nested projection.
All query keys, repeated parameters, malformed encodings and spoofed identity
headers are rejected. Responses are no-store.

The retained complete authored activity broadcast method proves event payload
fields `account_id`, `activities`, and `timestamp`; the authored Solian caller
proves event names `account.presence.activities.updated` and
`account.status.updated`. This overview patch does not yet emit those events.
Friend-audience durable local replay is a separate change. No live external
provider, notification channel, WebSocket fanout or guessed packet is added.

Verification distinguishes offline compilation/unit/race/SQL grammar from real
PostgreSQL. Six exact PG cases are registered, including wait/cancellation,
privacy, paging/capacity and a paired restart marker. The marker stores original
pre-target credentials, public verification keys and elapsed wall clock; its
after phase never issues credentials, refreshes, seeds target rows or rewrites
authority. Independent source, fixture and grouping review is complete in this
delivery; coordinator integration and real PG execution remain pending. Actual
PG execution remains coordinator private CI only.

The independent review adds executable single-connection synthetic SQL-driver
coverage for late activity expiry and fallback/count, source withdrawal after a
file wait, original-reader expiry/cancellation, hidden status metadata, current
friend removal before paging, and earlier artwork retention across a later file
wait. The late-expiry regression fails against the frozen implementation. These
tests exercise Go transaction composition; they do not claim to execute SQL or
prove PostgreSQL locking. The existing PG concurrency case additionally requires
a one-connection pool and a final real-proof check delayed past the lease deadline.
All six original PG case names and the saved restart marker remain unchanged.
