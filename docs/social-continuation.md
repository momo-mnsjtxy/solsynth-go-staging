# Canonical relationships and realms

This independently authored family persists social state against
`owned_authority_accounts` and resolves individual publishers through
`owned_publishers`. It does not use a second account directory, synthetic
identity headers, excluded shared models, or legacy database imports.

## Implemented behavior

- Relationship lists, pending request lists, request, recipient-only accept or
  decline, sender-only cancellation, forget, bilateral blocking, directed
  close-friend selection, and private aliases.
- `/stargate/relationships` and `/api/relationships` are equivalent authored
  client route aliases. `/friends`, `/friends/accept`, `/friends/decline`,
  `/block`, `/close-friend`, `/alias`, and PATCH status preserve the evidenced
  methods and request keys. Responses use `account_id`, `related_id`, `status`,
  `alias`, account projections and timestamps. Lists include `X-Total`.
- Realm create, joined list, private/public detail, update, soft delete, member
  list, current membership, incoming invites, invite, accept, decline,
  community join, leave, removal and role changes preserve the retained
  `/api/realms` controller routes. The embedding router supplies `/passport`.
- Active membership supplies realm-wallet and realm-chat authorization. The
  provided wallet policy grants view to members and editing only to owners.
  The host must use the transaction-aware membership provider when committing
  wallet changes: `RealmRoleForWriteInTransaction` holds the same realm row
  lock as membership mutations until commit and requires read-committed
  isolation. The standalone wallet bridge methods are preliminary reads.
- Post providers supply friend/close-friend predicates and bilateral blocking
  in the caller's existing transaction or fixed parameterized SQL. Friends
  require reciprocal accepted edges. Close friends additionally require the
  publisher owner's directed close-friend edge. Pending requests confer no
  access. Friends-only organization publishers are denied by this provider.
- Deleting a realm revokes membership and soft-deletes its canonical
  organization publishers and publisher memberships in the same transaction.

## State and authority rules

Install `internal/socialpg/schema.sql` explicitly after authority and account
schemas, followed atomically by `internal/socialpg/permissions_schema.sql`. Constructors never apply schemas or seed authority, quotas or roles.
Realm creation requires a current `owned_realm_quota` row. Promoted invitations
and role changes require `owned_realms.boost_level >= 2`; no payment processing
or automatic entitlement issuance is implied. Slugs remain reserved after
soft deletion.

Built-in roles are normal 0, moderator 50, and owner 100; defined custom roles
use levels 1–99 except built-in 50. The creator receives one accepted owner
membership. Canonical ownership comes from the realm account ID and is protected
through these APIs. Inviters need current `can_manage_members` permission and
cannot grant their own role, a greater role, or permissions they do not hold. Role changes/removals require strictly greater authority
than both the current and new target roles. An invitation is accepted only by
its target, while active and pending, after checking the inviter still has
sufficient authority and neither side has blocked the other. Rejoining a
community resets previous privileges to normal. Private realm details and
member lists are hidden from outsiders; pending members appear only in their
own invitation list, not the public member list.

Relationship state uses separate pending requests and directed accepted/block
edges. Accept atomically creates two friend edges and removes the request.
Blocking atomically removes both friendship edges and requests, preserving a
block independently held by the other party. Forget cannot remove the other
party's block. PATCH from blocked to friend starts a new request; it cannot
silently recreate consent. Close-friend selection is directional.

Every writer locks affected canonical account rows in sorted ID order before
locking domain rows. This serializes reciprocal friend requests, accept/block
races, quota consumption and membership changes. Audit failures roll back the
entire operation. Realm rows serialize membership and realm deletion. Host
composition must call `NewWithAuthority(db, guard)` and bind the original
verified proof using the authority guard. The guard rechecks live permission,
account and session state in the same read-committed transaction after account
locks are held, before domain state is inspected or changed. Plain `New` is
for explicit repository tests/provider composition without an HTTP mutation
boundary. A nil guard in guarded mode fails closed.

Read lists, their totals and hydrated account data use one repeatable-read
snapshot. No caches can retain a friendship or member role after revocation.
Repository provider methods do not treat account IDs or membership as identity
proof; embedding services must verify the actor and retain the transaction's
required locks. Permission names are drawn from the retained licensed registry.
PATCH status additionally requires its underlying block/friend/close-friend
permission, preventing a generic update permission from bypassing those gates.

## Later permission continuation

Custom realm role/permission lifecycle and canonical enforcement are added by [the realm permission continuation](realm-permissions-continuation.md). It supplies the role and override rules described above; timed realm mute expiry remains unsupported because no retained realm contract establishes it.

## Bounded gaps

Mute/unmute, expiring blocks, degradation, presence, relationship sync,
labels, boosts/payments, file attachments, experience,
moderation logs and quota projection return explicit unsupported responses
where routed. Creation/update reject unsupported picture/background IDs.
Undefined custom roles and stale assignment generations deny permissions.
Organization-publisher creation is supplied by the later [realm publishing continuation](realm-publishing-continuation.md). Automatic publisher role synchronization remains unsupported. Realm membership and publisher membership remain separate permissions; realm deletion revokes associated publishers.

These are owned behavior choices rather than claims of a full legacy model
port. In particular, private realm metadata is guarded, pending memberships do
not appear in public member lists, and privilege reactivation is prevented.

## Evidence

Retained licensed DysonNetwork source at
`4ee2d0cebb9da8284a92483fee6dce795bf6947c` supplies the full Passport
`Realm/RealmController.cs` routes/request shapes and permission names through
`internal/auth/source-docs/PERMISSIONS.md`. Their existing manifests and license
artifacts are reused without copying the retained source again.

Pinned AGPL Solian authored client reads at
`ffd0838e5f04cb40bea311a39118ebeedad210c1` supplied:

- [relationship.dart](https://github.com/Solsynth/Solian/blob/ffd0838e5f04cb40bea311a39118ebeedad210c1/lib/accounts/screens/relationship.dart),
  git blob `37eeb9df6432b9ea15223c45f1aff8a197ccf222`: lists at lines 30,
  83–124; pending/friend/close/block status meanings at 176–219; concrete
  mutation routes and payloads at 566–691 and 771–791.
- [realm_detail.dart](https://github.com/Solsynth/Solian/blob/ffd0838e5f04cb40bea311a39118ebeedad210c1/lib/realms/screens/realm_detail.dart),
  git blob `7e924fa9d58a68909cd3f0c18f6a003cf7f7b994`: role thresholds at
  122–125 and 247–251; normal-member invitation fields at 2568–2569.

Only authored, pinned client files were consulted. The client SDK import was
not expanded. No canceled SoSYS, Socommon or Stargate implementation source,
NeTo/Spec schema, generated definition, credential or production endpoint was
used. No additional full upstream source is retained by this change.

## Verification and integration

Local race-enabled unit tests verify route/action mappings, exact permission
gates, PATCH privilege boundaries, rejected identity headers, malformed bodies,
unsupported expiry semantics and role/slug bounds. The PostgreSQL integration
suite compiles locally but deliberately skips execution without its guarded
disposable CI database. No local database/listener was started.

Add these required names to the CI before phase:
`TestPGSocialRelationships`, `TestPGSocialRealms`,
`TestPGSocialTransactionsAndHTTP`, `TestPGSocialRealmWriteRevocationOrdering`,
`TestPGSocialRestartPersistence`.
Add `TestPGSocialRestartPersistence` to the after phase. That test's after
phase performs only reads; the harness must actually stop and restart
PostgreSQL between phases. Other integration cases cover account isolation,
duplicate request races, accepted/block races, private realm reads, invitation
revocation, strict role hierarchy, privilege-resetting rejoin, canonical wallet
roles, realm/publisher deletion, transaction rollback and stale permission
rejection after acquiring an account lock. Actual PostgreSQL pass/fail belongs
to the staging CI result, not the local compile result.
