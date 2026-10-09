# Verification marks, administrative statistics and explicit rule testing

This slice extends immutable `3c537d4cfa91aa2963b929819672047d48f2c22d` plus the frozen moderation patch `432b47b0fbc63006b7ed405eb0503e8ae1069b92f2f7002018d4d4bb365a0944`. It preserves that delivery and adds a narrow dependency patch. No live providers, production moderation, real credentials, local database server or sockets were used.

## Source-backed surface

The retained authored AccountAdminController, PublisherAdminController and RealmAdminController prove POST/DELETE verification routes and the fields `type`, `title`, `description`, `verified_by`. Publisher and realm DTO bounds are 1024/8192/1024 UTF-16 units; the owned account route uses those same bounds. The authored account API documents numeric type 0. Solian's authored `account_name.dart` consumes the fields and renders numeric indices 0–6. The excluded Shared enum and generated SDK were not read. String aliases such as documented `creator` or `organization` cannot be mapped to numbers from these permitted sources, so the owned input accepts only numeric 0–6. This is an explicit compatibility gap, not reconstructed semantic enum parity.

The three authored stats controllers and `docs/ADMIN_STATS_API.md` supply the complete supported response shapes and service routing. The source AccountService implementation relies on a remote account projection, while the authored stats document says to exclude accounts without profiles. This implementation follows the documented profile boundary using canonical local profiles and durable presence last-seen rows, excluding disabled accounts. It does not claim unseen RPC/model behavior. Sphere counts nondeleted posts under nondeleted publishers, including private/moderated content in the global administrative totals. Wallet counts nondeleted wallets/transactions and retained canonical orders/subscriptions. No balances, identities, IP addresses or location data are returned. All rolling windows are inclusive 1/7/30-day intervals ending at the UTC `calculated_at` value; future records are excluded from windows.

AutomodController, AutomodService and the authored Sphere `Models/AutomodRule.cs` prove two rule types (link blocklist 1, keyword blocklist 2), three result actions (derank 1, hide 2, flag 3), defaults, atomic case-insensitive name upserts and explicit supplied-text testing. Source rule matching treats both types as case-insensitive literal or regex content matching; it does not fetch links. This slice implements that administration/testing lifecycle. It does not apply actions to posts or start background enforcement. Adding such enforcement requires an explicit canonical write/read integration and tests of current rule snapshots.

Exact revisions, blob hashes and primary links are in `admin-extensions-source-evidence.json`. Only authored licensed sources were used; no generated bindings, excluded sources or original fortune corpus were accessed.

## Routes and current authority

Every route requires a fresh bound proof, current canonical Superuser and the exact named permission. Global status cannot bypass explicit denial, session scope/expiry/revocation, punishments or ordinary privacy gates.

| Routes | Permission |
|---|---|
| POST/DELETE `/api/admin/accounts/{identifier}/verification` | `accounts.manage` |
| POST/DELETE `/api/admin/publishers/{name}/verification` | `publishers.moderate` |
| POST/DELETE `/api/admin/realms/{slug}/verification` | `realms.moderate` |
| Passport GET `/api/admin/stats` | `accounts.view` |
| Sphere GET `/api/admin/stats` | `posts.moderate` |
| Wallet GET `/api/admin/stats` | `wallets.transactions.manage` |
| GET/POST `/api/automod/rules`; GET/PUT/DELETE `/{id}`; POST `/bulk` | `automod.rules.manage` |
| POST `/api/automod/rules/{id}/test` | `automod.rules.test` |

Source routes without explicit local attributes do not establish public access. Their owned rule administration/test gates remain global. `verified_by` is a display label, never authority; audit rows independently retain the authenticated canonical actor. Verification grants no read access, publisher editing, realm membership or account privileges. Clear-account verification returns 204; publisher/realm operations return their canonical updated admin projection. Verification is nullable metadata on the existing profile/publisher/realm, not a new identity.

Canonical account, publisher, realm, post, collection, survey and sticker projections carry the optional mark through their current privacy checks. Private realms, disabled accounts and moderation remain outer gates. The existing publisher/realm admin lists now support `verified=true|false`. The next patch also corrects the moderation member list to the exact authored RealmAdminController contract: default/false `pending_only` selects joined members; true selects pending invitations. A regression creates both states and verifies disjoint results. The frozen moderation artifact is unchanged. Root/nested posts re-read current publisher metadata, so cleared marks do not survive in cached association data. Publisher/realm names and federation actor identity policy are unchanged.

## Transaction, cache and runtime bounds

The small moderation extension seam reuses its current global gate and sorted canonical account planning. Verification observes the canonical target, locks sorted accounts then realm/publisher, rechecks selector ancestry after waits, updates only verification metadata and records audit in one transaction. Account profile creation uses the canonical profile row when needed. It rejects setting a mark on a disabled account, while clearing remains available. No ordinary profile PATCH field can set verification.

Each statistics response comes from one aggregate SQL statement, so its counts share a database snapshot. Canonical global authority is locked before that statement and revalidated before commit. `count(*)` results are nonnullable 64-bit integers; there are no nullable sums or floating financial amounts.

Automod mutations lock the canonical actor, then one durable generation row, then affected rule rows. Every mutation rechecks current global authority after waits and before commit. Bulk requests contain 1–100 unique case-folded names and commit all rule/audit/generation changes together. Active rule capacity is 1000. Rules are soft-deleted; reads query current rows directly, so no stale rules cache is introduced. `enabled_only` (also source-style `enabledOnly`) filters disabled/expired rules. The explicit single-rule test can inspect a disabled rule, matching source behavior. The source request DTOs expose no expiry mutation, so this API does not invent one.

Regex uses Go's non-backtracking RE2 syntax. Backreferences/lookbehind and other unsupported .NET features are rejected. Before compilation, bounded syntax cost rejects repeat expansion; compiled instructions are capped at 1024, nesting at 128, text at 65536 bytes, and input-by-program work at 8,388,608. Context is checked around evaluation. Pattern/name/description limits follow authored UTF-16 bounds; weight/priority also have explicit owned limits. HTTP bodies are capped at 64 KiB. Invalid regex returns an error rather than a fabricated no-match. Literal Unicode case behavior follows Go's lowercase mapping and is not claimed to duplicate every .NET ordinal edge case. No test input is stored in the audit ledger.

## Exact host integration guide

- Import `internal/adminpg`, `ports/admin/verification`, `ports/admin/statistics`, and `ports/sphere/automod`.
- Construct `adminRepo, err := adminpg.New(db, guard, nil)` using the same canonical pool and authority guard, then call `Ready(ctx)`. Inject the fixture clock in tests.
- Verification: `verification.NewHandler(adminRepo, postIdentity{identity})`; dispatch when `verification.Route(canonicalPath)` succeeds, before the existing moderation handler's unsupported-verification branch.
- Automod: `automod.NewHandler(adminRepo, postIdentity{identity})`; dispatch only on Sphere/unqualified canonical automod paths using `automod.Handles`.
- Service stats: construct separate `statistics.NewHandler(adminRepo, postIdentity{identity}, statistics.Passport|Sphere|Wallet)` handlers for `/passport/admin/stats`, `/sphere/admin/stats`, `/wallet/admin/stats`, preserving existing service normalization to `/api/admin/stats`.
- Combined aliases: `statistics.NewHandler(adminRepo, postIdentity{identity}, "")` accepts only `/api/admin/passport/stats`, `/api/admin/sphere/stats`, `/api/admin/wallet/stats`, identified by `statistics.QualifiedRoute`. Unqualified combined `/api/admin/stats` has no implicit default. No header selects service or authority.
- Apply existing identity and transaction-proof middleware exactly as for the moderation handler. Handlers' actor strings confer no database authority by themselves.
- Apply updated `accountpg.Schema` and `socialpg.Schema` for nullable `verification` JSONB columns on `owned_accounts_profiles`, `owned_publishers`, `owned_realms`; then `adminpg.Schema`. It adds only `owned_admin_extension_audit`, `owned_automod_rules`, `owned_automod_state`. All existing identity/moderation/statistics tables are reused. No startup migration or background worker is added.
- Register all ten new integration cases in the before phase, and `TestPGAdminExtensionsRestartPersistence` in both phases. The prior moderation host/view dependency remains required.

No host, CLI or workflow files are changed by this patch. Coordinate the shared account repository scanner edits with the separate actor-alias patch: retain its selector/name-reservation helper and append verification columns/scans without replacing the whole file.

## Verification and remaining work

The delivery manifest records full tests, vet, formatting, focused race and source-evidence results. Ten PostgreSQL cases compile and explicitly skip without the guarded parent runner. They cover metadata lifecycle and private/nested projections, current global/target rechecks, rollback, exact aggregate windows/ledger counts, permission/session denial, rule lifecycle, bounded explicit testing, bulk rollback/concurrency, revocation and restart persistence. All fixtures are disposable synthetic state with frozen-clock-derived timestamps; no payment provider executes.

Remaining work: semantic verification-name mappings, verification for unrelated app types, custom realm permissions, background automod enforcement/deranking, distributed rule-cache adapters, Ring/geography admin statistics and unseen full legacy model/RPC parity. None is reported as implemented or successful.

## Composed host candidate

The combined host installs the explicit extension schema, constructs the shared canonical repository with the configured clock and includes it in readiness. Service-local stats preserve Passport/Sphere/Wallet dispatch, while combined `/api/admin/stats` has no implicit service and returns not found. Qualified combined aliases dispatch deterministically. Verification routes override the prior moderation unsupported branch, and automod is limited to Sphere/combined routing. One composed PostgreSQL fixture verifies each service response shape, non-global denial, set/clear propagation through the public canonical account profile, explicit rule match and deletion. The fixture uses the array-valued authored test response and typed JSON requests. All eleven added cases await guarded actual PostgreSQL execution.
