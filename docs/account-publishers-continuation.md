# Owned account and publisher continuation

This is a bounded, independently owned Go account/profile and publisher implementation. It uses canonical authority account rows and PostgreSQL persistence. It is not a claim of legacy Stargate/Shared wire or database compatibility, and it does not implement the complete Passport or Sphere service.

## Source and ownership boundary

The retained eligible sources are pinned in their existing manifests, and the six SHA-256 values below were checked against the local bytes on 2026-10-08. No new repository download was needed.

| Evidence | Pinned revision | SHA-256 |
| --- | --- | --- |
| `inventory/operation-sources/DysonNetwork.Sphere/Publisher/PublisherController.cs` | DysonNetwork `4ee2d0cebb9da8284a92483fee6dce795bf6947c` | `27f586249fd911cbe552e788f3e19016a5e206b28ea386e0aa1f8bf826358ed9` |
| `inventory/operation-sources/DysonNetwork.Sphere/Publisher/PublisherPublicController.cs` | same | `229ec0f8504e58b5210b8c9390a7f714c95d8f873be49f859948b9d025029eac` |
| `inventory/operation-sources/DysonNetwork.Passport/Account/AccountPublicController.cs` | same | `1d017b4285058eb99ecdd6f1d65fcb5f601676e9ff2fd4b032af07d87f10149d` |
| `internal/auth/source-docs/PERMISSIONS.md` | same | `153a84141705315d88b2460388892192de274ef70d7e80ef846d67559d0ea874` |
| `inventory/product-client-evidence/Solian__lib__accounts__screens__profile.dart` | Solian `ffd0838e5f04cb40bea311a39118ebeedad210c1` | `6430b1d562fd340f3daff5bc064f81b6571fc31cde44f8b741c982bff0a31813` |
| `inventory/identity-client-evidence/solian/lib/accounts/account_pod.dart` | same | `14342d21dd2d59485c3e861a4ce3ae5a2881e29fcb729a0003716d3e8617ca40` |

Manifest license evidence identifies the repository-owned sources as AGPL-3.0. Existing notices and manifests remain in place. The implementation has AGPL-3.0-only SPDX notices. References in those C# files to unavailable Shared types are not definitions of those types. No NeTo/Spec schemas, descriptors, generated bindings, SoSYS/Socommon, or Stargate source were used or reproduced.

The client evidence identifies `/stargate/accounts/me` and `/stargate/accounts/{reference}` lookups. The reduced public account DTO, optional owned profile, `/api/accounts` aliases, PATCH semantics, role numbers, SQL tables, and errors are explicitly new contracts. Publisher route names, account-ID member addressing, numeric PATCH role body, and named permissions follow the retained controller/document declarations where known. Their full legacy shared-model serialization remains unresolved.

## Implemented behavior

| Route | Identity and policy | Result |
| --- | --- | --- |
| `GET /stargate/accounts/{reference}` and `GET /api/accounts/{reference}` | Anonymous allowed; any supplied proof must validate | Active canonical account by UUID or case-insensitive name; profile omitted until it exists |
| Same routes ending in `/me` | Fresh authenticated account/session, no fabricated read grant | Own restricted public projection |
| `PATCH /api/accounts/me` | Fresh proof plus the documented `accounts.manage` grant | Own nick/language/region and bounded public profile fields; atomic profile/account update and audit |
| `GET /api/publishers` and `/api/publishers/invites` | Authentication only | Accepted memberships or own pending invitations |
| `GET /api/publishers/of/{accountID}` | Anonymous allowed | That active account's accepted publishers |
| `GET /api/publishers/{reference}` | Anonymous allowed | Active publisher with safe account projection |
| `GET /api/publishers/{reference}/members` | Anonymous allowed | Accepted active members, validated offset/take, consistent `X-Total` and page snapshot |
| `GET /api/publishers/{reference}/members/me` | Authentication only | Own active membership, including pending invitation |
| `POST /api/publishers/individual` | `publishers.create` plus current stored quota | Independent publisher UUID, owner membership and audit in one transaction |
| `PATCH /api/publishers/{reference}` | `publishers.update` plus accepted Manager or Owner | Nick/bio update and audit |
| `DELETE /api/publishers/{reference}` | `publishers.delete` plus accepted Owner | Soft deletion, membership revocation, default-selection cleanup and audit |
| `POST /api/publishers/invites/{reference}` | `publishers.invites.manage` plus Manager or Owner and sufficient role | Pending invitation for an existing active account |
| `POST /api/publishers/invites/{reference}/accept` or `/decline` | `publishers.invites.manage`; target always authenticated actor | Accept or decline own pending invitation |
| `DELETE /api/publishers/{reference}/members/{accountID}` | `publishers.members.manage` plus Manager or Owner and sufficient role | Revoke non-owner member and remove its default selection |
| Same route ending in `/me` | Authentication only | Leave own accepted non-owner membership |
| `PATCH /api/publishers/{reference}/members/{accountID}/role` | `publishers.members.manage` plus sufficient current role | Change an accepted non-owner to a supported non-owner role |

The original role enum is not available as permitted source. Owned role values are Viewer=0, Editor=50, Manager=100, Owner=200. Arbitrary integers and assigning Owner are rejected. This implementation deliberately protects all owner memberships from removal, leaving, or demotion; it does not copy the legacy controller's weaker self-removal check. Inviting requires at least Manager. Soft deletion reserves names indefinitely, preventing immediate identity reuse.

Account lookup never creates an account or profile. UUID references cannot be shadowed by another account's name. Updates derive the account ID from verified proof rather than JSON, URL, or proxy headers. Public responses never include contacts, credentials, scopes, grants, superuser status, punishments, birthday, or private authority metadata. Nested publisher/member accounts use the same projection. Profile links accept bounded HTTP(S) URLs without embedded credentials; no URL is fetched.

Name updates reject with `NAME_CHANGE_CARD_REQUIRED`; the paid name-change-card route returns `DEPENDENCY_UNSUPPORTED`. Picture/background, wallet, default-tag/category, organization/realm, rating/statistics, federation, features, subscriptions, quota-response wire shape, and other unimplemented provider paths cannot report successful writes. Unknown JSON fields, root null, malformed/multiple values, oversized bodies, invalid pagination, query credentials, and unsupported asserted-identity headers fail before domain mutation. HEAD returns 405 without domain reads or writes. Unknown generic account paths may return 404; recognized unimplemented publisher paths return 501.

## Persistence and integration

Apply `internal/authoritypg/schema.sql` first, then `internal/accountpg/schema.sql`, then schemas with publisher foreign keys such as the post lifecycle/application projection. Constructors and commands never apply migrations, import legacy state, mint accounts, seed grants, or create quota/entitlement rows. A separately approved migration must reconcile case-insensitive account-name collisions before the new unique index can be applied. Existing databases with a different definition need an explicit migration; `CREATE TABLE IF NOT EXISTS` is not a schema upgrade mechanism.

Owned tables: `owned_accounts_profiles`, `owned_publishers`, `owned_publisher_members`, `owned_publishing_settings`, `owned_publisher_quota`, `owned_publisher_entitlements`, and `owned_accounts_audit`. Account foreign keys point to `owned_authority_accounts`. Publisher and profile IDs are independently generated; neither is assumed equal to its account ID. Profile/publisher/member mutations lock the actor account first, then the publisher where relevant, and include their audit entry in the same transaction. Audit failure rolls back the mutation. Publisher creation serializes its current quota row and live publisher count, so parallel creates cannot exceed the configured total.

Quota and entitlements are explicit policy inputs with start/expiry bounds. Missing/expired policy denies rather than inventing a plan. `PublisherEntitlement` returns stored protected-tag total, level and perk level for an active publisher. `Resolve` verifies accepted membership and active owner/publisher state; it uses a valid persisted default or the only eligible publisher and rejects ambiguous choices.

`RequirePostEditor`, `ResolveForPost`, and `ListPostPublisherIDs` in `transaction_provider.go` join a caller-owned SQL transaction. The caller must lock the authority account first. Editor checks acquire the publisher row lock, shared by every membership mutation, and must remain in the post write transaction through commit. List results alone are observations and never write authorization. Disabled publisher owners and pending/revoked members cannot qualify.

Canonical proof integration requires `AuthenticateRequest`, `AuthenticatePrincipal`, and `AuthorizePrincipal`. Authentication-only routes recheck proof/account/session freshness without manufacturing a permission or weakening nonempty authorization requirements. The standalone command uses the canonical public-key verifier; the aggregate host may forward only its private verified proof context.

**Authority transaction boundary:** current authority/session/grant validation occurs immediately before the domain operation, while domain transactions independently lock the account and recheck its disabled flag. There is no transaction-bound canonical authority verifier here. Concurrent grant/session revocation between the validation snapshot and domain mutation is not serialized with that mutation. No claim of atomic cross-authority authorization is made. This remains a release gate requiring a shared transaction protocol and concurrency verification across domains.

## Command and verification

`cmd/account-service` exits without starting a listener by default. Starting it requires `--serve`, a literal loopback `--listen` address, `--postgres-dsn-env` naming an already injected DSN, `--public-key-file`, `--key-id`, `--issuer`, and `--audience`. It accepts one bounded RSA public-key PEM (PKIX or PKCS#1), rejects private-key PEM, uses `authority.NewVerifier`, checks the configured database, and supports bounded graceful shutdown. It never handles a private signing key, provisions credentials, or creates fixtures. No service listener was started in this continuation.

On 2026-10-08, `go test -race -count=1 ./ports/passport/accounts ./internal/accountpg ./cmd/account-service ./tests/integration`, focused `go vet` on the same packages, and `go build -buildvcs=false ./cmd/account-service` passed using the staged Go 1.25 toolchain and writable caches. The account HTTP package contains ten unit tests, and the command contains three. PostgreSQL cases skipped because no DSN was supplied. The initial command build's VCS stamp failed because this staged directory lacks git metadata; the explicit no-stamp build then passed. Unit tests cover safe projection, strict actor binding, authentication-only versus exact-grant routes, denied writes, every implemented publisher mutation route, pagination/count propagation, unsupported dependencies, invalid supplied authorization, root-null handling, and public-key-only opt-in command configuration.

Database cases in `tests/integration/accounts_test.go`:

- `TestPGAccountsPublisherLifecycle`: canonical lookup, profile persistence, quota policy, distinct publisher identity, membership/invite/decline/reinvite transitions, owner protection, real member counts, entitlement policy, defaults, soft deletion and disabled accounts.
- `TestPGAccountsAtomicRollbackAndQuotaRace`: eight concurrent creates compete for one quota slot; an audit-trigger failure rolls back both account and profile updates.
- `TestPGAccountsProcessPersistence`: a fresh test process rereads the committed profile.
- `TestPGAccountsProfileConcurrencyAndCanonicalLookup`: concurrent disjoint profile patches preserve both changes; canonical UUID lookup cannot select a username shadow.
- `TestPGAccountsRestartPersistence`: before phase creates profile/publisher/owner/audit; after phase only reads those persisted values following a real database restart.

The required integration runner must include all five in its before-pass assertion, include `TestPGAccountsRestartPersistence` in its after-restart selection/assertion, and reject skipped database cases. The existing guarded harness requires a marked disposable synthetic cluster. This worker did not start PostgreSQL or access any DSN/credential. Local tests without `PG_TEST_DSN` compile and skip PostgreSQL cases, so their package success is **not** database, rollback, race, restart, or deployed-service evidence. Those gates remain pending until the combined authorized integration environment runs them without skips.

Full client compatibility, unimplemented providers, legacy migration/import, cross-authority atomicity, real PostgreSQL execution/restart, actual HTTP-process verification, and publication/exact-commit CI are not complete in this bounded handoff.
