# Collection subscriptions and navigation

This local owned implementation adds the five authored collection routes from
DysonNetwork `4ee2d0cebb9da8284a92483fee6dce795bf6947c`. Exact source bytes,
license and Git blob/SHA-256 hashes are pinned in
`collection-subscription-navigation-evidence.json` and literal test expectations.
No excluded protocol bindings, Shared implementation, Socommon or canceled
repository material was retrieved.

The controller's subscribe/unsubscribe/status contract is account-self:
subscribe returns 201 and a subscription Location once, then the same row with
200; unsubscribe returns 204 even when absent; missing status returns
`COLLECTION_SUBSCRIPTION_NOT_FOUND`. The source permission is
`post.collections.posts.manage` on both POSTs. GET status authenticates without
that mutation permission. Handles stay publisher names, including UUID-shaped
names; collection paths never silently accept a publisher ID. HTTP query/body
injection is rejected before the repository is invoked.

Subscriptions use the existing canonical owned taxonomy subscription table.
An explicit FK and partial unique index bind active rows to collection IDs.
Unsubscribe ends a historical row, resubscribe creates a new ID, and both owner
and admin collection deletion end live subscriptions. Rename and retained handle aliases resolve the same
collection identity. Canonical permanent name reservations reject reuse by a
different publisher, whose same-slug collection has independent subscription
state. The established collection and publisher slug-reservation policy
is unchanged. Audit failure rolls back the subscription mutation.

Navigation computes lag/lead after all current post, publisher, moderation,
social and realm visibility filtering, ordered by published-or-created date,
creation time and ID descending. Its anchor must itself be a listed visible
collection post. Viewer-hidden private or unlisted, drafted, deleted and nonmember anchors
produce the same directional 404 as absent anchors. Count/page ordering for the
existing contents route uses that same authored order. Original controller
manual reorder rejection is retained.

The new flow observes the collection candidates and the response's complete
relation dependency set (one-level parents, chained children, and recursive reply
counts). It acquires sorted canonical account locks, including nested publisher,
realm and media owners, then ordered realm and publisher locks, the collection,
and ordered post and media locks. Deleted nested sources remain lockable and are
redacted by the current projection. Dependency ancestry and media assignments are
compared after waits and hydration; new identities fail with conflict instead of
being acquired out of order. This conservative read lock set includes collection
candidates, so very large collections can increase contention. It
never acquires a second pool connection inside the transaction. The original
proof, current scopes, enabled actors, canonical resolved identity and current
visibility are rechecked after waits and before commit. Selection changes during
a wait fail with the existing conflict response instead of exposing an unlocked
replacement. Full response hydration reuses the canonical timeline, embed,
media and interaction chain. Hidden nested identifiers and metadata follow the
same independent visibility decisions as post detail.

Owned differences are explicit: stronger active-row uniqueness; historical
subscription tombstones with fresh re-subscription IDs; transactional auditing;
strict request binding; fixed individual-publisher collection boundary; current
privacy after waits; and conflict on a changed navigation selection. The
subscription wire projection reuses the established five-field taxonomy DTO;
unknown excluded base-model serialization is not invented. The historical
service's separate manual-order method does not enable the rejected HTTP route.
Collection artwork and organization-owned collection CRUD remain existing
unsupported work, despite older broad inventory wording.

The retained `PublisherSubscriptionService.NotifySubscriberPost` includes
collection subscribers in new-post recipients. The separate local publication
hook is now documented in `collection-publication-notifications.md`; its nine
additional PostgreSQL declarations and restart case still require coordinator
CI. The manager-added publisher-subscription notification has a different
purpose. No external notification delivery is enabled by either collection slice.

Validation includes HTTP wire/permission tests, immutable source hashes, and
nine composed PostgreSQL cases for ordering, privacy, nested media, realm
revocation, incarnation/name reuse, rollback, contention, lock waits, one pool
connection and saved-original-proof restart replay of the full nested/media
projection. The media-wait cases include revoked private-file assignment and
changed relation identity; cancellation is followed by a fresh read to prove
transaction cleanup. Nested privacy changes block on the nested post before the
media phase, respecting the same lock order as writers. Those database cases are
registered for the private coordinator; ordinary local package success is
compile/skip evidence only. The restart after phase restores the saved verifier
and original credentials and does not recreate identities, grants or content.
