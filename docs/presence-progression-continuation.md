# Canonical presence and progression continuation

This change provides guarded PostgreSQL account status, privacy, session leases,
daily check-in, earned experience/badges, achievement/quest state and a durable
canonical-event projector. It introduces no account identity, account directory,
client XP endpoint, default permission grants, live credentials or automatic
runtime migrations.

## Host integration

Install `internal/presencepg/schema.sql` after canonical authority/accounts/social.
Install `internal/progressionpg/schema.sql` after presence. The latter contains
both owned DDL and the 58 pinned licensed catalog definitions; no separate catalog
install is required. The projector also requires the existing post, interaction,
chat, realm and account audit schemas. Both packages expose `Schema` strings for
explicit migration callers. Constructors perform no I/O.

Construct `presencepg.NewWithAuthority(db, guard)` and
`progressionpg.NewWithAuthority(db, guard)` with the same canonical database pool
and `authoritypg.TransactionGuard`. Their plain `New(db)` constructors deny
private operations. Mount `presence.Service` and `progression.Service` using
`Repository`, `Authority: verifier`, and `Bind: guard.Bind`; route their
`RouteMatches` families before the generic account handler. Their public status
and badge reads accept optional authentication; an invalid or empty supplied
Authorization header is never downgraded to anonymous access. Proxy identity
headers and URL token aliases are rejected.

Call `progressionRepository.ProcessDue(ctx, limit)` from the host's existing
worker (`1 <= limit <= 1000`). It processes at most that many committed canonical
source records and returns the number newly receipted. It does not start a
scheduler. The source anti-join uses durable `(kind, account, source)` receipts,
not a timestamp/sequence high-watermark that could miss a late transaction
commit. Each source is reread under the canonical account row lock. Missing
sources and disabled accounts receive explicit `gone`/`disabled` outcomes;
unexpected failures roll back the receipt for retry. Disabled historical events
are not replayed automatically on re-enablement.

Optional direct integration hooks are `RecordCanonicalEvent(ctx, actor, kind,
sourceID)` and `ApplyCanonicalEvent(ctx, tx, actor, kind, sourceID)`. Both require
an original opaque authority proof; the latter joins a caller-owned transaction
from the same pool. Callers must lock the canonical actor before domain objects,
as required by the existing authority transaction contract. Neither accepts XP,
occurrence time, metadata, an arbitrary account event, or a reward amount.

Presence transport callbacks are `Heartbeat(ctx, actor, idle)` and
`Disconnect(ctx, actor)`. Context must be bound to the transport's verified
original proof. Session ID, epoch, account version and timestamps come from the
fresh guard and database. Refresh after access-token rotation using a new proof.
One lease represents one canonical session; a host sharing a session across
several simultaneous sockets should disconnect it only when its final socket
closes. Unclean disconnects expire automatically.

Completions are persisted in `owned_progression_outbox` in the same transaction
as their progress, XP, badge and grant. Optional
`DispatchCompletions(ctx, limit, sink)` accepts a
`DeliverProgression(ctx, accountID, eventID, payload)` sink. Stable event IDs allow
transport deduplication; delivery is at least once across a crash between delivery
and SQL commit. No provider is called when there is no configured sink. Packets
contain earned XP/badges only: unavailable source-point payouts are zeroed in the
packet, while the original configured reward remains visible in grant history
with `source_points_granted_at: null`.

## Routes and behavior

Source-evidenced route families:

- `GET /api/accounts/{name}/statuses`
- `GET/POST/PATCH/DELETE /api/accounts/me/statuses`
- `GET /api/accounts/{name}/badges`, `GET /api/accounts/me/badges`
- `POST /api/accounts/me/badges/{earnedBadgeID}/active`
- `GET/POST /api/accounts/me/check-in`
- `GET /api/accounts/me/leveling` with bounded `offset`/`take` and `X-Total`
- `GET /api/accounts/me/progression/achievements`, `achievements/stats`,
  `quests`, and `grants`
- `GET/POST /api/admin/progression/achievements` and `quests`
- `PUT /api/admin/progression/{achievements|quests}/{identifier}`
- `POST /api/admin/progression/{achievements|quests}/{identifier}/enable`

Explicit owned extensions:

- `GET/PUT /api/accounts/me/presence/preferences`
- `POST /api/accounts/me/presence/heartbeat` (`is_idle` only)
- `DELETE /api/accounts/me/presence`
- `GET /api/accounts/me/check-in/history`
- `GET /api/accounts/me/progression/level`
- `PUT /api/admin/progression/levels` (`thresholds` array)

Private reads and writes join fresh authority validation to the same
ReadCommitted transaction. Creating/updating statuses requires
`accounts.statuses.create`/`accounts.statuses.update`; activation requires
`progression.badges.manage`. Admin definition writes require both
`progression.admin` and the relevant `progression.achievements.manage` or
`progression.quests.manage`. Level policy changes require `progression.admin`
and `progression.levels.manage`. These are explicit owned permission strings,
not values imported from excluded enum/constant definitions.

Online, last-seen and custom-status visibility each accept `public`, `friends`
or `private`; defaults are public/friends/public. Friends use mutual current
canonical relationships. Either-direction blocks hide the resource. Invisible
status suppresses online/idle/last-seen to other viewers. Device and session IDs
are never included in public projections. Leases last 90 seconds, have a
120-second schema cap, and cannot outlive their canonical session. Reads also
filter revoked sessions, stale epochs, stale account versions, disabled accounts
and active disable-account punishments. Idle means every live session is idle.
Last-seen and one activity event per observed local date are durable.

Daily check-in uses server UTC dates, an account lock and a unique account/date
constraint. Concurrent/repeated requests return the same result and award the
licensed 100 XP exactly once in the same SQL transaction. There is no client
backdating. `reward_points` and `fortune_report` remain null, and `tips` is an
empty array. Nonempty CAPTCHA tokens/backdated requests fail explicitly because
those providers are not configured. The owned anti-abuse policy is one verified
canonical account check-in per UTC date; it does not claim the upstream random
CAPTCHA policy. Metadata levels 0–4 preserve the source's 10/20/40/20/10 draw
weights without importing fortune prose or claiming exact excluded enum values.

Achievement counters, local-date streaks, daily counts and daily/weekly/monthly
quest periods are durable. ISO week-year and daylight-saving boundaries use
calendar arithmetic. Older events cannot reset a newer daily/streak state.
Series representatives/stages, counts, hidden statistics, search and percentage
follow the retained root service. The night-owl seed additionally enforces the
local midnight–04:00 predicate stated in its source description; the source's
generic trigger omitted that condition. Per-source idempotency prevents
reactions/bookmarks or re-friending the same entity from farming repeated XP.

There is no guessed level formula. XP works without a level policy; the owned
summary returns null level/progress and `level_policy_configured: false` until
an operator installs strictly increasing thresholds beginning with zero. The
level policy endpoint is explicitly owned rather than original wire parity.
Perk bonus multipliers are fixed to one until a trusted entitlement provider is
integrated. Badge activation requires an actually earned, unexpired badge for
the same canonical account and atomically deactivates its former badge.

## Canonical sources

Automatically projected categories are `post.create`, `post.react`,
`post.bookmark`, `chat.use`, `chatroom.join`, `realm.join`, `publisher.create`,
`relationship.friend_established`, and `account.active`. Post creation requires
its canonical create audit and a published live post. Chat use requires the
owned authored-message marker, canonical sender membership and live room.
Friendship requires the current mutual relationship. Account activity comes
from the durable authenticated heartbeat event, not a client event stream.

The retained catalog also describes source categories with no trusted provider
in this continuation: `account.avatar`, `account.connection_link`,
`account.profile_complete`, `account.push_enable`, `auth.factor_enable`,
`post.boost`, `post.create_topical`, `post.featured`,
`relationship.friend_accept`, and `stellar.support_month`. Their definitions
remain visible but no fabricated events are emitted. Downvote metadata-dependent
seeds do not advance without verified metadata. Original source-point rewards
remain pending; no balance, wallet transaction or provider acknowledgement is
invented. Rich activity/catalog/calendar/artwork operations, birthday bonuses,
admin direct badge grants/revocation and exact legacy device DTOs are outside
this status/progression continuation.

## Source and license boundary

`ports/passport/progression/source-evidence.json` records exact reviewed Git
objects from DysonNetwork `4ee2d0cebb9da8284a92483fee6dce795bf6947c` and Solian
`ffd0838e5f04cb40bea311a39118ebeedad210c1`. Licensed root Progression controllers,
contracts, service, seed catalog/service, ExperienceService and AccountService
badge methods informed the implementation. The already retained AccountEvent,
AccountCurrent, AccountPublic and Account controllers supply route evidence.
Authored Solian badge/check-in/progression callers and retained watch DTOs
provide field evidence. Nested trigger/schedule strings, status integer
encodings, level policy and added privacy fields are explicitly owned.

The full root AccountEventService was reviewed through a pinned source read; it
is not redistributed in this change because it includes unrelated fortune
fallback text. Only its source URL/hash is retained. No SoSYS/NeTo/Spec submodule,
unlicensed generated model/protocol definition or fortune corpus was opened or
imported. Existing licensed `badgediscovery.Catalog` is the sole badge-type
allowlist. Runtime code contains no imported or generated fortune content.

## Validation

Executed in this isolated workspace:

- `go test -race ./ports/passport/presence ./internal/presencepg
  ./ports/passport/progression ./internal/progressionpg` passed.
- `go test -run '^$' ./tests/integration` compiled all integration tests.
- Focused pure tests cover DST/ISO/leap boundaries, streak duplicate/out-of-order
  handling, complete catalog validation, metadata predicates, bounded definitions,
  forged/empty credentials and presence input/privacy bounds.

Required disposable PostgreSQL cases (prepared and compiled, not run locally):

- `TestPGPresencePrivacyLeasesAndAuthority`
- `TestPGPresenceFriendsBlocksInvisibleAndExpiry`
- `TestPGProgressionConcurrentCheckInAndIsolation`
- `TestPGProgressionCanonicalProjectorRollbackAndEarnedBadges`
- `TestPGProgressionChatSocialProjection`
- `TestPGPresenceProgressionRevocationWhileBlocked`
- `TestPGPresenceProgressionRestartPersistence` in both before/after phases

They use the existing marked disposable database guard, explicit synthetic
accounts/policies and real opaque proofs. They cover account/session isolation,
wrong-pool/unbound/unguarded denial, multi-session expiry/revocation, invisible and
friend/block privacy, concurrent one-time rewards, canonical-only sources,
transaction rollback, concurrent projector receipts, earned/expired badge
activation, automatic chat/social projection, re-friending deduplication,
deterministic lock-wait revocation, and persisted restart state. They open no
HTTP listeners. Local PostgreSQL execution was intentionally left to the parent
CI harness; compiled tests are not claimed as a PostgreSQL runtime pass.

Unified-host integration additionally dispatches earned completion records into the existing durable account inbox with the stable progression event ID as its idempotency key. The configured notification worker materializes inbox/replay events; external delivery remains disabled. A composed test checks canonical post creation, earned projection, duplicate-safe queueing and inbox processing.
