# Canonical realm permissions and source boundary

This owned continuation adds the retained eight-boolean realm permission surface to the existing social, chat, and post framework. It is based on continuation `525ef9e`; it does not replace a newer parent checkout or change the frozen MLS artifact.

## Verified source contract

The retained, licensed DysonNetwork `Realm/RealmController.cs` at `4ee2d0cebb9da8284a92483fee6dce795bf6947c` defines role-permission list/upsert, user-override read/upsert, and numeric member-role assignment. Its public requests contain the eight fields `can_chat`, `can_post`, `can_comment`, `can_upload_media`, `can_moderate_posts`, `can_moderate_chat`, `can_manage_members`, and `can_manage_realm`. Global writes require the existing `realms.permissions.manage` node. The permission endpoints additionally require current realm-management authority.

The complete authored Solian `lib/realms/screens/realm_detail.dart` at `ffd0838e5f04cb40bea311a39118ebeedad210c1` supplies the snake_case maps (around 724–737 and 964–992), the DELETE role path (around 400–422), numeric assignment (around 3025–3036), and the exact all-null camelCase override-clear map (around 1019–1031). No SDK/generated binding was read or inferred. Existing whole-file and license manifests remain authoritative and unchanged.

Supported paths, under the existing `/api/realms/{realm}` and `/passport/realms/{realm}` aliases:

- GET `permissions/roles`: role permission projections
- POST `permissions/roles`: `role_level` and all eight non-null boolean fields
- DELETE `permissions/roles/{level}`: remove a custom role definition
- GET `permissions/users/{accountId}`: current nullable override, or JSON null
- POST `permissions/users`: `account_id` and nullable boolean overrides
- PATCH `members/{accountId}/role`: the evidenced bare numeric body
- GET `permissions/me`: an explicit owned effective-permission projection; the member `me` response also carries `effective_permissions`

The nullable user request replaces the override set. Omitted or null fields inherit the role. The authored all-eight-null camelCase clear request is accepted only as that complete shape with `account_id`; mixed aliases, partial camelCase shapes, unknown fields, duplicate keys, and non-null camelCase grants fail. Effective permission helpers recognize only the eight owned field identifiers; unknown strings and wildcard requests deny.

The retained realm controller, realm admin controller, and authored realm detail screen contain no realm mute/unmute route, timed mute DTO, mute-expiry field, or evidenced negative mute role. The retained `RelationshipExpiryJob.cs` concerns relationships. No realm-wide timed mute API or numeric mute alias is invented here. Existing chat member timeout expiry remains the separately implemented chat contract. Realm mute expiry therefore remains a specific source-contract gap.

## Owned policy choices and protection against escalation

The complete authored `RealmService.cs` at the same DysonNetwork pin was read and its Git blob verified (`f7aed2430a446667f88da621794f6ed960a49f8c`). `HasPermission` maps the eight action names, checks nullable user overrides, and falls back to `GetRolePermission` and `GetDefaultPermission`. The default algorithm is authored in this service: named Normal gets chat/post/comment/media, named Moderator additionally gets post/chat moderation and member management, and named Moderator explicitly has `realm.manage=false`. Named Owner returns true. The service imports numeric role constants and model declarations from excluded `DysonNetwork.Shared.Models`; those declarations were not read. Defaults are not delegated to an unseen resolver.

Assigning these named profiles to owned levels 0, 50, and 100 remains an explicit compatibility-sensitive owned choice, corroborated by the authored caller's 50/100 capability thresholds. It is not a claim that excluded Shared constant definitions were verified. In particular, owned role 50 defaults to `CanManageRealm=false`. The caller's new-role editor defaults all eight draft toggles to true; those draft defaults are not evidence of legacy effective permissions. The role-creation UI's wider numeric range is not copied: assignment in the retained controller forbids owner-or-higher values. New custom levels are 1–99, excluding built-in 50.

Existing-flow adjustments from the earlier Go rank-only implementation are explicit: realm updates and organization-publisher creation now require `can_manage_realm`; role/override management and management reads also require it. A default role-50 member must use the canonical owner or receive an explicit owner-authorized grant before performing those actions. Existing organization-creation coverage now verifies the default denial and then an explicit grant. Member invitations/removal and chat/post moderation retain their named default moderator grants. Clients that gate management affordances solely by rank should consume the owned `effective_permissions` projection for custom roles and overrides.

Other deliberate resolver differences are bounded: the upstream service checks user overrides before active membership, while this implementation always requires current canonical membership first; named upstream Owner returns true for any requested string, while the owned canonical owner receives only the eight known grants. Built-in role rows are explicitly seeded and projected here, with generation values and descending rank order, rather than claiming the upstream stored-row-only ascending projection. Nullable override clearing removes the owned row; upstream writes nullable fields to an existing row. These are owned contract choices, not copied Shared behavior.

Only `owned_realms.account_id` identifies the canonical owner. An active owner retains all known permissions regardless of override rows and cannot be demoted, removed, or locked out through these APIs. A non-owner numeric 100 row grants no owner authority. The wallet/merchant bridge and realm payout-owner check retain canonical ownership requirements; realm management is not a financial or publisher-membership grant.

Delegated managers need the effective named permission and a strictly higher rank than the target role/member. They cannot modify themselves, peers, the owner, or their own role definition. A new grant must be a subset of their current effective permissions. Assigning an already-defined lower numeric role cannot bypass that subset check. Existing explicit user overrides remain a separate grant source across ordinary role changes; clear those overrides to revoke them as well.

Built-in role definitions can be edited by an appropriately authorized higher manager but cannot be deleted. The owner definition is immutable. Custom-role deletion atomically denies that role, clears affected user overrides, and records audit. Recreation increments its generation. Existing assignments retain the previous generation and stay denied until an explicit reassignment; reusing the same number does not restore old grants.

User overrides bind both the canonical member row and its joining timestamp. Leaving, removal, decline, invitation replacement, and community rejoin clear overrides. A deleted member cascades its override row. Ownership transfer clears both the old and new owners' prior overrides and updates their role-generation bindings. Neither membership reuse nor role-number reuse revives stale grants.

## Canonical enforcement

The permission matrix is used by the real domain paths, not only administration CRUD:

- Realm updates, invitations, acceptance, member management, role assignment, and organization-publisher creation check current named permissions and hierarchy.
- Chat sends and call admission include `can_chat`; realm room creation/management and moderation use `can_moderate_chat`; realm chat attachment admission requires `can_upload_media`.
- Realm-associated and realm-publisher post creation/editing require `can_post`. New replies and edits to existing realm replies check `can_comment` against the current referenced realm, including replies whose own post has no explicit realm ID.
- Attachment mutations check `can_upload_media` for the post, its organization publisher, and any realm reply target. Moving a personal post with existing attachments into a realm cannot bypass the gate by omitting the attachment field. Existing attachments remain readable under the ordinary visibility rules, but mutations that preserve realm attachments require current upload permission.
- Scoped post moderation uses `can_moderate_posts`; global administration still requires its separate global authority. A scoped moderator does not gain publisher editing.
- Effective-permission and management reads recheck current canonical membership and permission state. Ordinary realm/chat/post visibility remains governed by membership and publication policy: denying a write permission is not a retroactive history-access revocation.

Permissions are combined as role grants, then nullable allow/deny overrides. Missing/deleted roles or assignment-generation mismatches deny all known grants before overrides are considered. There are no permission caches. Account disable punishments use the current database clock. Role and override writers lock canonical accounts before the realm; domain writers retain their established realm lock through mutation. Read-committed writers see role changes committed before obtaining a waited-for realm lock. Social transaction authority is rechecked before commit. Audit failure rolls the entire change back.

The eight-bit storage mask and generation columns are owned implementation details. HTTP responses expose the authored booleans, stable row IDs, timestamps, role level, and an additional generation value. No shared enum, legacy permission inheritance algorithm, or external RPC compatibility is claimed.

## Installation and verification

Install `internal/socialpg/permissions_schema.sql` explicitly and atomically after the canonical social and authority schemas. It widens the owned role constraint to the supported numeric range, creates the role/override tables, backfills built-in profiles, and installs an ordinary invoker-rights permission function. Constructors and startup do not migrate data. The application readiness probe checks the required relations and function. `socialpg.Schema` includes this additive migration for existing disposable fixtures; the guarded PostgreSQL runner installs it once as a separate single-transaction migration.

Offline verification covers the Go suite, focused race tests, request/unknown-key tests, preserved source hashes, PostgreSQL-16 grammar, and runner schema/negative controls. Seven PostgreSQL scenarios are compiled and registered in the required-test gate. They cover lifecycle and role generations, escalation/canonical owner/membership incarnation, queued revocation and audit rollback, chat/post/reply enforcement, media admission and scoped moderation, exact HTTP/null-clear maps, and a real before/after restart contract. The restart test uses the parent's `fixtureRestartPhase`, `saveRestartFixture`, and `loadRestartFixture` helpers. Its before phase saves the generated account/realm IDs, role and override records, and member incarnation/generation; its after phase only reads persisted state and compares it with that saved prestate. The private harness must stop and restart PostgreSQL between phases.

Actual PostgreSQL transaction/catalog outcomes and a running authored client have not been verified locally. No socket retry, live provider, production service, new credential, or external publication was used. The parent private runner remains responsible for the database evidence before rollout.

Recovery verification re-ran the complete offline `scripts/verify.sh` aggregate against the preserved author tree: source/license and mutation controls, guard/wiring controls, PostgreSQL syntax parsing, formatting, build, vet, full Go tests, and the full race suite passed. The historical-base gate contains 523 before-phase and 68 after-phase tests; these counts are not the final composed parent count. The existing parent restart helper is a compile dependency and must not replace a newer parent helper. Actual PostgreSQL tests were skipped locally.
