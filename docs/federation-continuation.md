# ActivityPub owned continuation

The adapter adds a coherent, opt-in ActivityPub server-to-server subset to the
existing canonical account, publisher, post and authority model. Its base was
committed Go continuation `2fcfbcfffea2777681a71e8bd4593652c4abfcae`.
Source boundaries and exact retained-file hashes are in
[ports/federation/PROVENANCE.md](../ports/federation/PROVENANCE.md).

## Runtime composition

`federationpg.New(db, config, keys, guard)` performs no I/O. Apply
`federationpg.Schema` explicitly after `authoritypg.Schema`, `accountpg.Schema`
and `postpg.Schema`. The adapter neither generates keys nor fetches remote
actors. `Config` requires one canonical HTTPS origin, an explicit list of remote
HTTPS origins, a positive 1–12 attempt limit, and a 1–60 second delivery timeout.
The key provider supplies an existing local RSA signer by canonical publisher
UUID and trusted remote actor/key/inbox bindings by exact URI. Its implementations
must not discover keys by following untrusted request URLs.

`federationhttp.Handler` has `Repository`, `Config`, `Keys`, and `Authority`
dependencies. Host integration routes `IsPublicRoute` directly to this handler
so HTTP signatures cover the original request target. Private management routes
identified by `IsPrivateRoute` use the existing host proof binding plus
transaction authority binding. All local mutations require the canonical
`publishers.update` permission and a currently joined publisher manager or owner.
A request-provided account identifier never creates authority. The transaction
guard must use the same SQL pool as the domain transaction; a missing guard
fails closed. Incoming federation traffic uses only its verified remote proof.

No scheduler, transport, enrollment, migration, credential grant, or external
communication starts during construction. Explicit worker calls are
`ReconcilePosts(ctx, limit)` followed by
`ProcessDeliveries(ctx, transport, limit)`. Both accept 1–100 items. Nil transport
or key provider rejects delivery without changing the queue. `Ready(ctx)` checks
adapter table presence. The embedding host owns scheduling and must decide
whether to configure the optional adapter at all.

## Implemented routes and state

Public routes:

- `GET /.well-known/webfinger?resource=acct:name@configured-domain`
- `GET /activitypub/actors/{username}`
- `POST /activitypub/actors/{username}/inbox`
- `GET /activitypub/actors/{username}/outbox`, with `offset`/`take` pages
- `GET /activitypub/actors/{username}/followers` and `/following` (counts only)
- `GET /activitypub/objects/{post-uuid}`

Authenticated owned management additions:

- `PATCH /api/activitypub/publishers/{publisher-uuid}/settings`, requiring both
  `enabled` and `manually_approves_followers` booleans
- `GET /api/activitypub/publishers/{publisher-uuid}/relationships`
- `POST` to that relationship route with `action` and `remote_actor`; actions
  are `follow`, `unfollow`, `accept`, `reject`, `block`, and `unblock`

Local actors are opted-in type-0 canonical publishers whose canonical owning
account is enabled and publisher is not deleted. Publisher IDs are distinct
from account IDs. Realm actors remain unsupported. The actor's stored canonical
name determines URLs regardless of lookup case. The emitted key belongs to that
actor. Profiles and plain text post content are escaped before HTML projection.
Only visibility 0, published, non-draft, non-deleted, due canonical posts appear
in public objects or outbox. Unlisted, friends, close-friends and private content
are excluded. Public followers/following disclose counts, not identities.

Inbox verification requires a pinned 2048–8192-bit RSA key whose owner matches
the activity actor. The RSA-SHA256 signature must cover `(request-target)`, host,
date, digest, and optionally content-type in the supported order. Date skew is
bounded to five minutes. Host and path must be the configured canonical target;
SHA-256 verifies the exact body bytes. Bodies are capped at 1 MiB. Duplicate JSON
keys, hidden recipients, deep nesting, mixed bearer authentication, and malformed
identifiers are rejected. No key, profile or object is fetched from the network.

Verified Follow establishes an incoming pending relationship or atomically
accepts and enqueues Accept according to the local publisher setting. Accept and
Reject must reference that exact remote actor's pending outgoing Follow. Undo
accepts only the original incoming Follow. Block cancels queued/inflight work,
removes cached incoming objects and denies further inbox processing for that
publisher/remote pair. Unblock restores the ability to make a new request, not
an accepted relationship. Same signed activity ID and digest is idempotent;
reuse with different content or actor conflicts. Replay records are recipient
scoped and commit with relationship/object changes and delivery jobs.

Create and Update admit only public Notes from an accepted outgoing follow,
with signed attribution and same-origin object identifiers. The receiver stores
only the defined Note projection in a per-recipient federation cache. This cache
is not a local publisher, authority account or canonical local post and is not
silently included in the local feed. Delete requires an already established
object owner, retains a tombstone and prevents resurrection. Unknown-object
Delete, private remote messages, attachments and unsupported activity types are
rejected. No raw incoming envelope is exposed by a public API.

## Delivery and canonical post changes

`ReconcilePosts` observes the existing canonical post table, records a durable
revision using canonical post audit IDs and timestamps, and enqueues Create/Update/Delete for accepted incoming followers in
one transaction. Public-to-private/deleted transitions produce a content-free
Tombstone. Repeated worker runs do not enqueue duplicate revisions. A transaction
is bounded to 1000 follower destinations; exceeding that bound fails without
partial fanout. There is no automatic historical backfill after a new follow.

Each delivery retains its immutable payload and activity ID, peer, publisher,
optional canonical post revision and audit sequence, attempt count, due time and lease. Workers
claim due rows with `SKIP LOCKED`; only the earliest unfinished row for a
publisher/remote pair can be claimed. The exact lease token guards completion.
Transient transport failure, 408, 429 and 5xx use bounded exponential retry;
redirects and other 3xx/4xx are terminal. Attempt exhaustion is terminal `dead`,
including an expired final lease. There is no automatic dead-letter resend.
After a crash, an expired lease can be reclaimed with the same activity ID.
Delivery therefore has at-least-once semantics, not an impossible exactly-once
network guarantee.

Before a send, the worker rechecks enabled publisher/owner, block state,
relationship, post visibility/revision and the current pinned destination.
Canonical account, publisher and post locks remain held through the bounded
transport call. This serializes successful local privacy/block/disable changes
against stale sends. An expired network context leaves the lease recoverable;
it never records an uncertain send as successful. Stale Follow/Accept/Reject
messages are cancelled against current relationship state.

The optional `NewHTTPTransport` sends only to configured HTTPS origins. It has
no ambient proxy, no redirect following, bounded response headers/body reads and
a TLS minimum. All DNS results must be public unicast addresses, excluding
private, loopback, link-local, shared, reserved, translation and documentation
ranges. The dial uses the already validated numeric address, retaining the
original hostname for TLS. Configuration does not automatically construct or
activate this transport.

## Schema and validation

Nine supplementary tables are added:
`owned_federation_publishers`, `owned_federation_peers`,
`owned_federation_relationships`, `owned_federation_blocks`,
`owned_federation_inbox`, `owned_federation_objects`,
`owned_federation_post_state`, `owned_federation_deliveries`,
`owned_federation_audit`. Foreign keys point to the existing canonical publisher,
account and post rows. Signing secrets are never stored in these tables.

Locally passed: full `go test ./...` (DB cases skipped), focused `go vet`, race-enabled pure-Go
signature/JSON/destination/HTTP tests. The seven PostgreSQL cases compile but
were skipped because this environment has no authorized disposable DB socket.
No local listener, real remote actor, remote inbox, or production service was
used. Guarded CI must require these exact cases rather than count their skips as
success:

- `TestPGFederationCanonicalPublicHTTP`
- `TestPGFederationInboxReplayAndTargetIsolation`
- `TestPGFederationRemoteObjectsAndFollowControl`
- `TestPGFederationDurableDeliveryAndPrivacy`
- `TestPGFederationFreshAuthorityAndAtomicRollback`
- `TestPGFederationSignedLoopbackDelivery`
- `TestPGFederationRestartPersistence` (before and after DB restart)

The loopback test opens its disposable TLS server only after the existing
`database(t)` marker/DSN guard passes; production transport has no loopback test
mode. Test keys are generated in memory or existing disposable fixture paths.
Source-compatible NodeInfo behavior and its bounded predicates are unchanged.

Not claimed: full ActivityPub conformance; C2S outbox writes; remote discovery;
realm/server actors; followers identity listing; inbound Like/Announce/Move;
shared inbox forwarding; private federation messaging; media federation; key
rotation UI; remotely applied moderation; production federation activation;
upstream Shared-schema or C# differential parity. Existing NodeInfo output is
not expanded to advertise these unsupported features.

The later [signed quote authorization continuation](federation-quotes-continuation.md) adds a bounded incoming QuoteRequest/Accept/consume/revoke/Delete flow and public stamp dereference while retaining the explicit remote-discovery and broader protocol gaps above.
