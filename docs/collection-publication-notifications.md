# Collection publication notifications

This local owned implementation closes the documented canonical publication to
collection-subscriber notification hook. The lawful contract is the retained
AGPL DysonNetwork revision `4ee2d0cebb9da8284a92483fee6dce795bf6947c`:
`PublisherSubscriptionService.NotifySubscriberPost` selects collection
subscriptions, excludes replies, and handles Public/QuietPublic publication;
`PostService` calls it for immediate creation and unpublished-to-published edits.
The existing independently authored scheduler supplies the guarded due transition.
Exact source hashes are recorded in the companion evidence JSON and immutable
source tests. No excluded protocol, NeTo, Socommon, generated or canceled source
was consulted or copied.

The canonical post transaction records the publication identity and exact
subscription IDs. A recipient subscribed to several associated collections gets
one durable queue item with all captured incarnations. Subscribe/unsubscribe and
post writers serialize through canonical publisher locks; the hook acquires no
recipient account lock or recipient foreign-key lock after domain locks. An
ordinary edit or adding an already-published post to a collection is silent.
Drafts, future intents, replies, private/friends posts, and the separate
CloseFriends notification path do not enqueue this audience. An enqueue failure
rolls back post, audit and schedule changes. Final composition and blog checks
run after enqueue so a durable-write wait cannot preserve an expired source grant. A duplicate transition does not refill
its original audience with later subscribers. The bounded snapshot rejects more
than 4,096 subscription rows atomically with the existing conflict response.

The finite local worker uses one connection and a read-committed transaction per
recipient. It observes the full current direct-source/collection/media ancestry,
then locks sorted accounts, realms, publishers, collections, posts and files.
The original recipient and all current source/file/realm owners are included in
the account phase. Changed dependencies fail with conflict rather than expanding
the lock set late. The exact subscription incarnation, collection membership,
post publication identity, pinned publisher ownership, current post/realm/source
visibility, moderation, enabled/unpunished owners and recipient, block/mute, and
media availability/assignment are checked after waits and again after the inbox
projection. Unsubscribe/resubscribe cannot revive an old event. Processing and
receipt commit together; retries and racing workers do not duplicate the inbox
or account stream. Notification preferences reuse the existing local sink.

Notification payloads use a fixed owned English summary, topic `posts.new`, and
only `post_id`, `action_uri` and `notification_type` metadata. Titles, post bodies,
collection names, attachment IDs, private URLs and nested content are never
copied into durable inbox/replay payloads. The queue contains identifiers only.
No external delivery row is created and no provider API is called.

Inbox lists, total/unread counts and replay use the same current-source SQL
predicate. The reserved source key fails closed without its configured policy.
Read queries bind the actor as text before UUID comparison so the shared
visibility policy keeps its empty-actor NULLIF expressions type-safe.
Lists recheck after the viewed-update wait; replay rechecks the complete page
again after all other event projection waits. Filtered stream records still
advance the cursor, and the existing transport revalidator checks again before
emission. These reads authorize against a current statement snapshot; they do
not retain source locks across a network write. Bytes already delivered cannot
be recalled. No new public route is introduced.

Install `internal/interactionpg/collection_publication_schema.sql` explicitly
after the canonical interaction schema. The backend readiness check requires
all three relations. Constructors do not install schema or run work. The
application's notification worker includes this local projector and exposes a
bounded trusted `ProcessCollectionPublicationNotifications` method for tests and
maintenance. The canonical scheduler rechecks its recorded session and current
publication policy after the new durable write.

Rollback: stop this local projection and revert the host observer/read-policy
wiring together. Retain the identity-only tables and existing inbox/stream rows;
never reinterpret `collection-post:` rows as ordinary unscoped notifications.
The updated notification repository hides that reserved prefix when its domain
policy is absent. Dropping tables is not required and is not performed.

Owned differences are explicit: local inbox/replay only, a fixed content-free
summary instead of localized source snippets, current read-time revocation,
pinned publisher ownership, stronger current direct-source/media policy,
bidirectional mute suppression, historical incarnation binding and a fanout cap.
Other publisher/category/tag/CloseFriends audiences, localization, external
push/email, legacy migrations, mixed C#/Go writers and production deployment
remain outside this slice. This is not whole-platform parity.

Ten composed PostgreSQL declarations cover lifecycle/deduplication, drafts and
scheduled publication, current recipient/owner/source/media policy, inbox and
replay revocation, rollback/namespace/concurrency, domain waits/cancellation,
realm revocation, original proof after waits/fanout bounds, composition expiry
after a final enqueue wait for immediate and draft publication, and saved-original-
proof restart with a one-connection pool. They are compiled/skipped locally;
actual PostgreSQL and restart execution must run in the coordinator's authorized
private disposable CI. Offline source, SQL grammar, build/vet/unit/race evidence
is recorded separately and is not database runtime evidence.
