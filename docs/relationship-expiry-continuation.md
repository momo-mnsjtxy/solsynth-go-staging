# Current-time relationship expiry

This owned continuation starts from `10eb55ea95885b72cec348dbf716a3ed6e242c7a`. It adds the authored timed block/mute and pending-request lifecycle to canonical relationships. Current reads observe expiration without waiting for a job; bounded cleanup persists the resulting state and an atomic local lifecycle event.

## Complete eligible evidence

`ports/passport/social/expiry-evidence/manifest.json` pins complete authored RelationshipService.cs, RelationshipExpiryJob.cs, FriendsController.cs and Solian's relationship.dart, together with both repository licenses. DysonNetwork is pinned to `4ee2d0cebb9da8284a92483fee6dce795bf6947c`; Solian is pinned to `ffd0838e5f04cb40bea311a39118ebeedad210c1`. Exact Git blobs, full-byte SHA-256 values and source URLs are checked offline, including mutation controls. The SDK import/generated Dart part and excluded Shared definitions were not expanded.

The complete service creates new pending requests with seven-day expiration. BlockAccount and MuteAccount accept an optional duration and degradation; ProcessExpiredRelationshipsAsync degrades an expired row when a target status exists, otherwise removes it. Read helpers filter current expiration, and the Quartz job invokes that cleanup. The Friends controller establishes overview reads, not mutation DTOs. Mutation route/body evidence here comes from the complete authored client; this slice does not claim an inspected exact mutation-controller implementation.

The caller identifies muted status as -50 and blocked status as -100. Its block/mute sheet sends exactly `30m`, `1h`, `24h`, `7d`, or `30d` in `expires_in`; timed blocks additionally send `degrade_to: -50`. It uses POST `/stargate/relationships/{account}/mute` and `/unmute`, while permanent operations omit the duration. No generated enum number or realm-wide mute API was inferred.

## Bounded input and state behavior

Both existing `/api/relationships` and `/stargate/relationships` aliases support the sourced operations. Block and mute requests accept only the five observed duration strings. Null/omitted expiry remains permanent. Only a timed block can request degradation, and the only supported target is muted -50. Other durations, status transitions, or expiry fields on unsupported operations fail explicitly. Duplicate keys, unknown/camelCase keys, malformed values and unsupported degradation cannot silently alter a timer.

Newly created friend requests expire after seven UTC days. The explicit additive migration leaves previously stored rows with no expiry unchanged, preserving their prior owned behavior; it does not retroactively assign deadlines. Pending requests never grant friendship, and expired requests cannot be accepted. A replacement request receives a fresh identity and deadline after atomic cleanup of old intent.

A mute replaces the caller's directed relationship state. Re-muting a currently active mute conflicts, matching the service. The caller's 'update mute duration' branch actually calls unmute for an existing mute; that client behavior is documented rather than silently interpreted as a different server contract. Muting also removes the sender's own pending request, preventing the recipient from later accepting stale intent over the sender's newer mute. It does not remove the other person's independent block or mute.

As in the authored service, explicit unmute restores only the caller's directed Friend state and clears its timer. The owned endpoint additionally requires both `relationships.mute.manage` and `relationships.friends.manage`. Private friendship-based access still requires reciprocal accepted edges, so unmute or expiry cannot manufacture the other person's consent. Generic status PATCH cannot bypass the dedicated mute authority.

Timed mute with no degradation is removed at expiry. Timed block with no degradation is removed; a timed block with `degrade_to: -50` becomes a permanent directed mute. Expiry never restores a friendship. Existing no-expiry block/close-friend/alias/request operations retain their supported behavior. Blocking still removes pending intent and bilateral friendship while preserving the other person's independent negative state.

## One current read policy

`owned_social_effective_relationships` projects current status using the PostgreSQL clock and omits expired nondegrading rows. It projects an expired block's supported mute degradation immediately. `owned_social_effective_requests` hides expired pending requests. The only remaining raw relationship/request reads are mutation and maintenance paths that need stored state; all domain visibility reads use the effective projection.

This covers canonical social providers, friend/private post and timeline reads, chat/call admission through existing chat policy, presence, profile media, location and calendar visibility, account discovery, sticker/livestream filters, and existing progression/rewind readers. Relationship-list count and page are materialized from one projection in one SQL statement, preventing deadline crossing from making their totals disagree. No permission cache can prolong an expired block.

Mute is a distinct directed relationship state, not a second chat ban. Existing membership/history rules still apply. This slice does not add legacy push-message fanout or change chat invalidation events into push notifications. The existing notification audience filter inherits the current chat visibility policy.

Remote ActivityPub actors have their own explicitly provisioned identities and federation block table. No unverified account-to-remote-actor mapping is introduced: local relationship expiry neither alters those blocks nor expands public-only federation object visibility. A guarded case verifies that private canonical posts remain unavailable through federation after a local block expires.

## Atomic cleanup and races

Each relationship/request policy gets a monotonic sequence-backed revision. Policy replacement gets a new revision, including after row deletion/recreation. Cleanup selects bounded candidates, locks both canonical accounts in sorted order, obtains a fresh database time after any wait, and mutates only the selected identity/revision while it is still due. A renewed or permanent block therefore survives a stale worker observation.

Ordinary social writers retain their existing account locks and process due state for that pair in the same transaction before applying the requested action. A queued accept sees the current request deadline after its account-lock wait. Expiration cannot race a block/update into restoring an older policy.

`owned_social_expiry_events` records owner, counterpart, source identity/revision, previous status, optional resulting mute status, effective deadline and processing time. The state transition and event insert commit together; event failure rolls both back. A unique source/revision key and guarded writes make cleanup/restart replay idempotent. These are local durable lifecycle records, not instructions to notify another person or call an external provider.

`Repository.ProcessExpiredRelationships(ctx, limit)` accepts 1–1000 candidates. The existing Backend.ProcessDue lifecycle invokes it; constructors start no job or migration. The current-time projection remains authoritative even while cleanup is delayed or rolled back. A batch error preserves already committed per-candidate work and reports the error with the count.

Install `internal/socialpg/expiry_schema.sql` explicitly and atomically after the canonical social schema. It adds nullable deadlines, the sourced muted status, bounded degradation, policy revisions, effective views and the event table. Existing `socialpg.Schema` includes the addition for disposable fixtures; the guarded runner installs it separately. Backend readiness checks the added storage and projections.

## Verification

Seven new PostgreSQL declarations cover current-time degradation and cleanup, seven-day/new versus legacy-null requests, actor isolation and event rollback, concurrent cleanup, real account-lock waits across policy renewal and deadline changes, chat/presence/profile visibility, unchanged federation audience and restart persistence. The before phase saves exact policy/event prestate with the parent's exclusive marker helper. The after phase only reads it and verifies permanent, future-timed and already-degraded state; it never creates fixtures or reapplies schemas.

Offline verification passed source integrity/mutation controls, PostgreSQL-16 grammar, runner/result/phase controls, formatting, build, vet, all Go tests and full race tests. The isolated base plus this slice registers 567 before-phase and 74 after-phase cases. Actual PostgreSQL transaction, catalog, locking and restart execution remains for authorized private CI; local integration runs compile and skip.

Not claimed: arbitrary duration parsing, arbitrary degradation targets, full excluded model serialization, realm-wide mute routes, remote-actor block equivalence, relationship delta/sync APIs, or new external push delivery. No production system, live provider, credential change, publication or parent-checkout edit was performed.
