# Realm profile, quota and moderation-log continuation

Base: `444076623a010135a721122730612210be85e670`. This bounded continuation restores the remaining authored nick/bio profile routes and realm-removal log list, plus a usable quota read under explicit owned policies. It uses the existing enabled realm-member module and its copied boost/perk configuration. It adds no provider calls, media fetches, payments, implicit quotas, or runtime DDL.

## Evidence and client shape

The retained complete AGPL DysonNetwork RealmController at `4ee2d0cebb9da8284a92483fee6dce795bf6947c` establishes GET `quota` (42–53), the nullable nick/bio request (103–107), PATCH/DELETE `members/me/profile` (395–436), and GET `posts/moderation-logs` (1241–1262). The retained complete authored RealmService establishes the realm-only query, count, removed-post reason, descending moderation date, and active member loading. The retained Passport AppDatabase and licensed EF model snapshot establish nick/bio limits and the log identity/time columns. The existing Solian realm_detail.dart at `ffd0838e5f04cb40bea311a39118ebeedad210c1` supplies the actual profile editor and explicit DELETE caller (3050–3167): account pictures remain account profile data.

New pinned, complete authored evidence lives in `ports/passport/realmmembers/source`, with Git blob, SHA-256 and length manifests and both root licenses:

- RealmQuotaService.cs: response total/used/remaining/level/perk level and owned realm id/slug/name records. Its imported ResourceQuotaCalculator is **not** inspected or reconstructed.
- Solian realm_quota_info.dart: all six response fields are required; level/perk are integers, records is an array.
- Solian realm_form_content.dart: GET `/passport/realms/quota`; the visible quota card consumes used/total. Realm picture/background fields belong to realm CRUD, not the self-profile request.
- Solian build.yaml: `field_rename: snake`, so the Dart property `perkLevel` corresponds to wire key `perk_level`.

The existing source-integrity checker includes all new pinned bytes; mutation tests cover the service, client model and serialization configuration. No canceled SoSYS/Socommon material, protocol bindings, imported model definitions, or unlicensed Suki sources were read. Shared model names in authored service/EF evidence are references, not reconstructed imported implementations.

## Routes

Both `/api` and `/passport` accept:

- PATCH `/realms/{ref}/members/me/profile`: a complete nullable replacement containing only nick and bio. Omitted fields clear their realm overrides, matching the authored request object assignment. Nick is at most 1024 UTF-16 units, bio 4096; NUL, invalid field types, duplicate/unknown/case-aliased keys are rejected. Media IDs and caller-selected target accounts are rejected.
- DELETE the same path: clears both overrides and returns the canonical member object (200). It still requires current self membership and `realms.members.manage`, but no paid perk, matching the source. Nonempty DELETE bodies are rejected.
- GET `/realms/{ref}/posts/moderation-logs?offset=0&take=20`: authenticated current `post.moderate` realm permission; 1–200 take, maximum offset 1,000,000, strict single query values. Returns the authored array plus `X-Total`. Stable order is moderated time descending, then ID.
- GET `/realms/quota`: authenticated self-only projection, no selectors/query arguments. Returns total, used, remaining, level, perk_level and records; `X-Realm-Quota-Policy: owned.configured.grant` identifies the explicit owned policy.

The self-profile requires the global `realms.members.manage` scope and live account perk level at least two for PATCH. It deliberately does not require realm moderator rank, member-management realm permission or realm boost level. Ordinary joined members can edit only themselves. Perks come from the existing explicit subscription map; caller claims, manually stored realm boost levels and paid-but-queued/testing/malformed subscriptions cannot grant eligibility. DELETE remains available after a perk expires.

Nick/bio are separate from canonical account fields. Existing canonical social member lists, self reads, label-filtered reads and membership responses receive the same optional enrichment. Profile rows bind to both canonical member ID and joined_at; a departed/rejoined membership cannot revive an old override. Losing a perk does not erase an existing profile. Account nick and profile remain unchanged.

## Explicit quota policy and remaining parity limit

The upstream quota total depends on an imported tier calculator using account level and perk. That formula remains unverified. This route instead reports the exact existing owned realm-creation grant: a begun, unexpired `owned_realm_quota` row supplies total; absent/future/expired grants supply zero. Used and records contain only this account's nondeleted canonical realms, matching owned CreateRealm's usage count. Existing owned creation has no reserved-slot ledger to add. Remaining is max(0,total-used). Reads never reserve or consume slots and never insert grants. At most 10,000 owned realm records are projected; larger sets fail closed rather than return a misleading partial count.

Level uses the canonical account progression XP ledger and the existing explicitly configured global threshold row, with the same one-based level projection as progression Summary. It is not realm-scoped progression and does not use a guessed legacy curve. Missing threshold policy returns the stable 503 `REALM_QUOTA_POLICY_UNCONFIGURED`, not a fabricated zero or authentication denial. Perk uses the existing mandatory explicit map; an explicitly empty map selects zero, never an inferred subscription identifier. Changing level/perk does not silently alter the existing quota grant. This is a documented owned policy, not complete upstream tier-formula equivalence.

Quota projection locks/authenticates the account, locks the grant and configured threshold rows, and revalidates the original proof before returning. Effective grant time uses fresh database wall time. Subscription/definition row contention returns whole-transaction retry. After all subscription/definition queries, both ends of every candidate's active interval are checked against the maximum of fresh `clock_timestamp()`, the original caller time, and a fresh commerce policy-clock observation. This includes queued paid cycles that begin during a wait and excludes cycles that expire during it. A policy clock advanced during the wait takes effect; moving that clock backward cannot resurrect a cycle already expired by database time.

## Privacy and transactional boundaries

All mutations and protected reads use original opaque proof through the canonical ReadCommitted TransactionGuard. Accounts are locked in sorted order before realms; active owner, current membership, role generation, scope, session and punishment state are checked after waits. The self-profile final check repeats perk and authority after profile/audit/enrichment work. Failures roll back the entire profile, membership timestamp and audit transaction. Secondary subscription/definition locks are NOWAIT.

Log reads query only `owned_realm_post_moderation` for the resolved realm. They return id, realm_id, post_id, moderator_account_id, reason, moderated_at, created_at, updated_at and deleted_at. There is no join to post bodies, account profiles, global automatic-rule evidence or review metadata. Since this immutable owned removal ledger has one creation/moderation event, created_at and updated_at equal moderated_at and deleted_at is null. Existing and future canonical removals receive a stable UUID through the explicit post-realm migration. This is not a second moderation authority or log insertion API. Canonical owner activity and realm visibility still apply; a superuser flag alone cannot bypass missing membership/realm permission.

## Installation and verification

Apply the existing `internal/postpg/realm_schema.sql` with its additive stable log-ID migration, and `internal/realmmemberpg/schema.sql` with profile/audit tables. Their current harness positions already satisfy dependencies. Realm-member readiness now checks the profile/audit, log identity and canonical progression tables; it deliberately does not require a level policy row, allowing the quota endpoint's specific unconfigured response. Construction performs no migrations or seeding. Existing host realm-member/boost flags configure this continuation; disabled routes fail closed.

`docs/realm-profile-routes-integration.json` lists mandatory PostgreSQL before/after cases. Group/compiled-test registration is intentionally a coordinator integration step against the final combined tree. Local all-package tests, focused race checks, vet, source-integrity mutation checks, PostgreSQL-16 grammar and static SQL bind arity run without sockets. Integration package success without PG_TEST_DSN means compilation/skips only. Real PostgreSQL execution, constraints, waits, transaction rollback and process restart remain mandatory coordinator CI.

The existing perk-wait case covers both subscription and definition table waits for profile and quota reads, across cycle start, cycle expiry and policy-clock advance. Expiry subcases also replay with an old policy clock to reject resurrection. Fixture mutations occur before the later table lock; the blocker never writes authority grants or punishments while the request holds an earlier account lock.

The restart marker `realm-profile-log-record.json` is private disposable test state, never a deliverable. Before creates a paid canonical entitlement, profile and actual moderated post using the original bounded proof. After reconstructs only adapters using original proof and public verification keys, advances by real elapsed time, and checks persisted member/log identities, timestamps and unchanged audits. No after-phase seeding, replacement credentials or policy writes occur.
