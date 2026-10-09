# Durable scheduled post publication

This continuation is based on runtime `c8f2fe79c6f8822534c24ad3926fcfaad4ded917`.
It completes future `published_at` admission and a bounded due transition over
canonical posts. Existing immediate publication, drafts, post/media ownership,
publisher editors, realm permissions and moderation repositories are reused.

## Source and genuinely missing flow

The complete authored `PostService.cs`, `PostActionController.cs` and
`PostAdminController.cs` were read at the licensed DysonNetwork revision
`4ee2d0cebb9da8284a92483fee6dce795bf6947c`. The complete scheduled-job registration
was also read. The new retained PostService is exactly 145,621 bytes, Git blob
`dbacff11ded248680bc34d9fe422cc7e38c49068`; its SHA-256 is pinned in the existing
post source-integrity inventory and the adjacent scheduling-evidence manifest.
Existing controller snapshots and AGPL license remain in their prior locations.

The action controller declares nullable drafted/published instants, rejects both
being non-null, supports create/PATCH and an explicit draft publish operation.
PostService.PostAsync (1644 onward) accepts a future publication timestamp,
sets an omitted timestamp to the current instant, and rejects explicit past
creation. UpdatePostAsync (1903 onward) switches between draft and publication,
permits future timestamps and normalizes a draft's past publish request to now.
The service/query extension hides future publication from non-editors until the
clock passes. The service launches notification, link-preview, indexing,
ActivityPub and realtime work only if publication is already due during the
create/update call. Neither that complete service nor the complete job registry
contains a delayed publication worker. This gap is not attributed to unseen
Shared code, and no excluded definitions were followed.

Retained authored Solian initial compose state was read, and both complete
publish payload branches in compose_shared were inspected. They preserve draft
save through non-null drafted_at plus null published_at, followed by the
explicit `/sphere/posts/{id}/publish` call. The retained current settings form
has no scheduling control. Future publication is evidenced by the original
server contract; this does not claim an already working client scheduling UI.

The prior Go post validator rejected every future published_at. Existing admin
statistics and moderation cover current post state, while livestream maintenance
reconciles stream provider runs and earned payments. Existing automod has rule
administration and explicit rule testing, with no post-publication evaluator.
None supplies this missing delayed post transition. This slice does not invent
an automod approval, external classifier result or successful provider delivery.

## API and owned storage choices

The canonical composed host accepts future published_at on the existing POST
and PATCH post routes and aliases. Omitting/nulling the time fields keeps prior
behavior; setting drafted_at cancels a schedule, a replacement future time
replaces its intent, and deleting the post cancels it atomically. Ordinary
content/media edits retain the pending deadline and update its revision. The
existing explicit publish operation can publish a scheduled draft immediately,
with fresh authority, cancelling its old delayed intent. Immediate publication
through either publish or PATCH rechecks current moderation, related-post and
retained-media gates after all media waits; it does not reuse the old schedule's
captured policy stamp. Existing non-future request behavior, including the owned
past-timestamp policy, is preserved.
Standalone constructors keep rejecting future timestamps until explicitly
configured with WithScheduling and canonical dependencies.

A pending operation is stored as a canonical draft with published_at null.
This stronger owned boundary means all existing feeds, nested post readers,
private file grants, progression and federation projection keep withholding it
even after its deadline, until the due transition commits. It avoids treating a
clock crossing as authorization. Primary editor responses retain the authored
future published_at / null drafted_at shape. An additional editor-only `schedule`
object reports id, due_at, state and a bounded failure reason. It is an owned
extension, never credential/proof data. Pending items appear in the existing
draft listing because they remain draft storage internally; that is an explicit
listing difference. Blocked items project as drafts. Anonymous/non-editor post
reads never receive schedule history. Nested internal projections remain drafts.

The schedule table records actor, target publisher/post, session identity,
authorization epoch, account version, relevant policy snapshot, due time,
revision and terminal state. It never stores bearer/refresh tokens. Every
admission requires the original signed proof and posts.create, in addition to
the existing mutation permission and current publisher/realm checks. The owned
recorded-admission lane supports ordinary type-0 sessions; delegated/OAuth future
scheduling returns unsupported rather than guessing ongoing delegated consent.

## Authority continuity and cancellation

Ordinary access-token expiry and token refresh do not revoke the schedule.
Execution reuses StoredSessionEligible, which checks the canonical recorded
session and parent chain, stable authorization epoch, account version, current
scopes/grants/punishments and posts.create. Explicit logout/session revocation,
parent revocation, scope changes, session expiry and account invalidation stop
publication. The schedule therefore remains tied to its originating session's
continued authorization, not a short-lived access token. A normal refresh may
extend session lifetime according to the existing authority rules.

Relevant grant records, publisher membership identity/timestamps, realm member
and role/override records, publisher owner state and moderation history are also
bound. Replacing those records invalidates old intent even when effective rights
are later restored. This is deliberately conservative: a relevant policy-record
edit can require explicit rescheduling. Ordinary content edits do not silently
refresh the old actor's captured authority or moderation history. A new explicit
future timestamp records fresh intent under the editor making that request.
Transient revoked state that leaves no surviving canonical generation, timestamp
or audit change is not reconstructed as an unseen historical event; current
eligibility is always checked. Once a worker observes a denial it records a
terminal blocked result. Grant restoration, unlock or repeated due processing
cannot revive it; an authorized editor must reschedule or explicitly publish.

## Atomic due processing and current gates

ProcessScheduled selects a bounded set of due candidates, then rechecks each
under canonical actor, sorted realm/publisher, post and sorted file locks, using
the existing post prelocker. Publication additionally includes existing forward
and chain targets in the prelock plan. It acquires retained-file and schedule
locks before final gate evaluation, then rechecks the captured revision, current
publisher editor/realm posting or commenting authority, source ancestry, post/publisher moderation, current related-post visibility, ready
retained media, source owner state and applicable realm upload permission.
Draft, unavailable, locked or shadowbanned relation targets stop publication.
Private/friends/realm visibility is preserved; the worker never promotes it.

The database clock decides due eligibility, including equality at the deadline,
after waits. Only a successful transition clears the canonical draft, stores
the requested publication time, appends the canonical publish audit and records
one durable publication event. Every write shares the transaction. Event/audit
failure rolls back the post and schedule; concurrent workers and stale selected
revisions cannot duplicate publication or overwrite a replacement/cancellation.
The original-proof check now also repeats before ordinary post mutations commit,
so a file or domain wait cannot extend a request credential's lifetime.

The host ProcessDue includes this bounded worker before its existing downstream
reconciliation. ProcessScheduledPosts provides a focused local maintenance hook.
Construction starts no scheduler and makes no external call. Existing federation
reconciliation can queue public content only after committed publication and
continues enforcing its current visibility/moderation policy. The durable
schedule events are local transition receipts, not a claim that notifications,
search indexing, realtime packets or remote delivery occurred. New push/index
providers and live automod execution remain separate work.

## Installation, tests and evidence limits

Explicitly install internal/postpg/scheduling_schema.sql after canonical posts,
authority sessions/continuity, publishers and the composed realm/media/timeline/
moderation dependencies, before any composed Backend.Ready call. Startup only
checks dependencies. No production migration, provider operation or local
PostgreSQL socket was attempted.

CI files were deliberately left unchanged while the parent freezes its pilot.
`docs/post-scheduling-integration.json` gives the exact new schema and eleven
before-phase declarations; the saved-restart case also belongs in the after
phase. The parent must merge those into its current runner/groups/guards before
running actual PostgreSQL. The unchanged base's integration declaration guard is
not a pass for this unregistered candidate.

The tests compile locally and cover lifecycle/HTTP projection, no early post or
file access, private publication, cancellation/replacement/delete, token expiry,
refresh/logout, current grants/account/publisher/realm revocation, concurrency,
rollback, due-time boundaries, wait races, retained media and related-post
privacy, deferred federation state, and saved published/pending/blocked rows and
bytes read after an actual runner restart. Ordinary Go execution skips them
without PG_TEST_DSN. Full local build/vet/test/race and source/SQL checks are
recorded in the delivery manifest. Independent review added explicit publish
and PATCH gate failures and repair controls, publisher/realm owner changes during
file waits, and stored-session expiry during the final schedule-row wait. The
access-expiry case uses real signed-token expiry with a real issuer/verifier clock;
it does not advance a frozen verifier ahead of the database. Only a targeted
fixture deadline is changed and restart timestamps retain database precision.
Catalog/type/locking/restart behavior still requires the parent's authorized
private PostgreSQL run.

No prohibited NeTo/Spec/generated definitions, excluded Shared implementations,
canceled SoSYS/Socommon or unlicensed Suki source was read. Existing full model,
provider, automod application, bulk publication and frontend UI gaps are not
represented as complete by this bounded flow.
