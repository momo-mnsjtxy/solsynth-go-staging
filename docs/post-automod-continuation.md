# Automatic local post-rule enforcement

This bounded addition targets local `657eb35c8c6d5fb21baa97f21508ffd3476a0d86`.
It closes the missing configured-rule runtime path. It does not implement an
external classifier, global content blocking, a moderation notification service,
or unseen legacy provider contracts. No external model/provider call, production
moderation, database socket or deployment was performed.

## Source and default effects

The complete authored AutomodService, AutomodController, AutomodRule,
AutomodConfiguration, PostService, post controllers and TimelineService at
DysonNetwork `4ee2d0cebb9da8284a92483fee6dce795bf6947c` were inspected.
`post-automod-source-evidence.json` identifies each retained source, exact Git
blob, SHA-256 and license. New evidence snapshots retain the AGPL license;
existing exact snapshots are referenced rather than duplicated. No excluded
Shared definitions, protocol/schema exports or generated bindings were used.

The authored default is deliberately preserved:

- Derank sums each matching rule's configured weight.
- Hide adds 100 to the penalty and removes the post from ranked candidates.
- Flag has no enforcement side effect. A matched Flag is a recorded fact, not
  a notification, quarantine, visibility change or mandatory review queue.
- Latest bypasses automatic rule effects. Detail, search and ordinary post
  listing keep their existing visibility and manual moderation gates.

Every active rule is checked; priority orders evidence and does not short-circuit
later rules. The owned evidence outcome uses Hide, Flag, Derank, clear precedence
while retaining all reasons. Title, description and body are space-joined as in
the authored service. The owned post model has no mention-URL field, so that
source input remains unavailable. Link rules are case-insensitive text matching:
there is no URL fetch, percent decoding, path collapse, IDNA conversion or port
normalization. Tests record those explicit URL-equivalence boundaries.

## Runtime and freshness

The composed backend enables local-rule evaluation for canonical post
create/edit/publish writes and ranked feeds. Standalone repositories opt in with
`WithAutomod(reviewEnabled)`; construction creates no schema and starts no job.
Successful post writes store their content hash, rule generation, complete active
rule fingerprint, bounded reasons, penalty, flags and decision revision in the
same transaction as content/associations/audit. Draft and private state is never
promoted by the evaluator. `classifier: unavailable` explicitly distinguishes
local text matching from external classification approval.

Historical decisions do not authorize ranked visibility. Every ranked feed reads
the current active rules in the same repeatable-read transaction as its candidate
posts, then evaluates the Go matcher again. This includes old posts without a
decision and immediately reflects rule insert/edit/delete/disable/expiry. There
is no rules cache, asynchronous freshness gap or SQL reinterpretation of RE2.
The existing chronological page/cursor algorithm is retained; Hide may leave a
short or empty ranked page while its cursor still advances over scanned rows.

Current publisher editors receive a whitelist summary of the historical outcome,
penalty, flags, revision and computed `stale` marker. Recursive ordinary post
projections exclude global rule identities, matching excerpts, rule fingerprints,
content hashes, reviewer identity and private review reasons. Public/noneditor
post reads and timeline/search/event projections omit automatic-rule evidence.
Canonical stored decisions and private decision events retain full evidence;
projection never rewrites them. The marker is informational; review and ranked
reads independently recompute current state. No automatic bulk backfill rewrites
old evidence merely because a rule changed.

Full administrative evidence additionally requires current canonical Superuser
identity, `posts.read` and the exact `automod.rules.manage` permission. Explicit
denial wins, and a broad `admin.*` grant is no substitute. Administrative reads
without rule-management permission retain their safe manual post projection.
Evidence-bearing list/detail/extension replies recheck both permissions together
at the final response boundary, including grant expiry during a list. Failures
return no partially accumulated evidence.

## Atomicity and bounds

Canonical actor, realm, publisher, post and association/media locks precede the
shared generation lock. The evaluator reads rules only after acquiring that
generation row. Rule CRUD takes the actor then exclusive generation/rule locks
and never takes post/media locks, avoiding a generation-to-post inversion.
Post writes reobserve current editor/owner/realm gates after the generation wait
and revalidate their original bound authority before commit. Deletion records a
terminal evidence state without running the matcher, so evaluation bounds cannot
prevent authorized removal of oversized existing content.

The existing safe RE2 compiler, instruction/input limits and unsupported-feature
rejections are reused. There are at most 1,000 rules, 65,536 input bytes whenever
an active rule is present, 1,024 regex instructions, and 8 Mi work units across
one complete ruleset evaluation. A ranked page stops without output after its
aggregate work exceeds 64 Mi units; at most one additional bounded post has been
evaluated at that point. No partial feed or partial post commit is returned.
Reasons retain at most 256 UTF-8 bytes of matched text, and persisted JSON is
bounded to 2 MiB. No active rules means large otherwise-valid posts still work.

Unsupported .NET regex features and Unicode casing differences are not claimed
as exact engine parity. Bounds and RE2 are explicit owned safety decisions.
Missing rule storage, invalid legacy rule data, or exceeded runtime bounds fail
explicitly; they are never converted to successful classification.

## Optional owned administration

`--enable-automod-review` (application `PostAutomodReview`) explicitly enables
`POST /api/admin/posts/{id}/automod-review` and its Sphere alias. Default is off.
The request contains `expected_revision`, `action` (`dismiss` or `recheck`) and a
nonblank bounded `reason`. Current canonical Superuser identity and both exact
`posts.moderate` and `automod.rules.manage` permissions are required together.
The original proof and current grants are rechecked after account/domain/rule
waits and immediately before changed or replayed reviews return. No permission
name or anonymous administrative bypass is added.

Dismissal suppresses local ranked Hide/Derank effects for exactly the reviewed
content hash, active-rule fingerprint and generation. A changed body or rule
generation, including delete/replace, invalidates it. Rule expiry invalidates the
active-rule fingerprint even without a generation bump. An exact repeated
still-current dismissal returns the same revision without duplicate events.
Stale reviews conflict. Recheck records fresh evidence and removes a dismissal.
Recheck retries must use the current returned revision. Manual post locks,
shadowbans, draft/private visibility and realm decisions remain independent.
Turning the option off stops honoring existing dismissals in ranked feeds.

Every changed decision and optional review has a durable event; review also
writes the canonical moderation audit. Any failure rolls back content, evidence,
revision, associations and audit together. The review extension and persisted
evidence are owned additions, not claims of an upstream Flag workflow.

## Scheduling composition

The separate scheduling overlay is tested in an isolated composed copy. Due work
calls `AutomodInTransaction(ctx, tx, actor, post, "scheduled_publish")` after
post/media/schedule locks and before publication. Current publication gates and
stored-session eligibility run again after its possible generation wait. Hide
alone does not deny publication in the source-compatible mode; ranked reads
apply the current Hide result. An unavailable evaluator rolls back due work
instead of publishing with fictional approval.

For explicit publication, preserve the scheduling review correction's retained
media lock phase; evaluate after that phase and before the final publication
checks. The delivery includes merged reference files and an exact standalone
scheduling overlay. Do not independently place a generation lock before a later
retained-media lock acquisition.

## Migration and verification

Apply updated `postpg.Schema` after existing authority/account schema. It adds
`automod_revision`, `automod_decision` and `owned_post_automod_events`. Existing
social/post-realm/moderation/admin schema order is retained; rule tables and the
generation row remain in `adminpg.Schema`. Host readiness checks the new evidence
columns and event relation. No DDL is executed at startup. Rolling back the code
can leave additive evidence intact; do not delete event history or claim legacy
schema import support. Mixed legacy/Go rule writers do not share this protocol.

`post-automod-integration.json` lists exact before/after PG declarations and the
two scheduling overlay cases. The restart assertion reads original saved
evidence and event bytes before doing current-rule reads; it does not recreate a
post, rule or decision in the after phase. Unit and compile checks do not certify
PostgreSQL catalog, locks, rollback or restart execution. Actual PostgreSQL 16
execution remains with the coordinator's private CI. No CI layout/group files
were changed in this artifact.
