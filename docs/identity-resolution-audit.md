# Identity, permission, and global-filter resolution audit

Date: 2026-10-08. Target: `Solsynth/DysonNetwork` at
`4ee2d0cebb9da8284a92483fee6dce795bf6947c`.

## Decision

The eligible sources resolve the permission spelling and a meaningful documented
JWT/cache baseline. They do **not** establish production authentication,
authoritative permission evaluation, or shared global-filter parity. Keep the
production authority adapter absent and protected routes fail closed.

This is a source-informed evidence audit, not a clean-room or legal opinion.
No authentication implementation, production request, credential, deployment,
upstream write, excluded-repository read, or excluded protocol export was used.

## Scope and provenance

The audit read the existing `internal/auth` documents and
`inventory/license-audit.json`, retained licensed snapshots, the pinned
DysonNetwork root tree, and selected DysonNetwork-owned source/documents through
the existing GitHub connector. Every newly fetched source file used the pinned
commit. Repository-scoped search results were discovery hints; relevant findings
were checked against pinned source or the already retained pinned snapshots.

The recursive root-tree response was not truncated. It represents
`DysonNetwork.Shared` as a gitlink at
`83c36cb92d667d3800f152665660c17886f71f7c`, not owned implementation files.
That gitlink was **not expanded**. The root AGPL license does not establish a
license for a separately maintained dependency.

Boundaries preserved:

- No NeTo/Spec definitions, schemas, descriptors, generated bindings, or user
  exports of those materials were fetched, used, or proposed as an input.
- No new implementation/interface evidence was obtained from ElecPostal,
  PersonalityCore, DistributionCenter, MaidCafe, or other missing-license
  repositories. The limited existing audit does not establish reuse permission.
- SoSYS's earlier API 403 and canceled repository-page/clone retrievals were not
  retried or routed around. In particular, no additional Stargate retrieval was
  performed. Earlier Stargate statements remain previously reported evidence,
  not newly verified implementation.
- No production identity, permission, Redis, WebSocket, or HTTP endpoint was
  probed. No actual account, token, password, or private key was needed.

Eight relevant DysonNetwork-owned narrative documents are retained verbatim in
`internal/auth/source-docs/`, with `LICENSE.txt` and `manifest.json`. Each file's
Git blob SHA was recalculated from its bytes and matched the pinned connector
SHA; SHA-256 and byte lengths are also recorded. These are independent parent
repository documents, not exports from the excluded protocol repository.

## Newly recovered eligible document evidence

All source links in this table use the pinned target commit.

| Source | What it establishes | What it does not establish |
|---|---|---|
| [UNIFIED_JWT_AUTH.md](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/docs/UNIFIED_JWT_AUTH.md), blob `46146891b46a4475103b928352a1672d7903866f` | Documented RS256 `Bearer` user and `Bot` API-key lanes; required claims; issuer/audience/public-key configuration names; revocation and account-version checks | Current middleware implementation, clock skew, claim parsing edge cases, version comparison/missing/error behavior, session-epoch checks, or actual deployment configuration |
| [PERMISSIONS.md](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/docs/PERMISSIONS.md), blob `524bc9fd360bd3e358ffc108cbf60823bd734361` | `post.subscriptions.manage` at lines 171–172; all declared permission attributes must pass; first failed check gives 403; OAuth scope, superuser, group and punishment concepts | Complete evaluator algorithm, exact wire transport, current token/session binding, unavailable-service behavior, or exact denial body |
| [SHARED_CACHE_CONTRACT.md](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/docs/SHARED_CACHE_CONTRACT.md), blob `1e04837889dc474ee342da2863ac1420e99aad15` | `dyson:` structured-cache namespace; JSON hash envelope; Redis TTL governs expiry; missing/invalid structured entries become misses and session misses go to the auth service | Full cached-session payload, mandatory freshness/revocation checks on a hit, Redis outages, revoked-JTI/account-version marker defaults, or permission-cache validity |
| [PASS_TOKEN_ENDPOINTS.md](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/docs/PASS_TOKEN_ENDPOINTS.md), blob `1e5a795e5db37b57ab904f79fc97c63b5ce9dfce` | First-party/OAuth token endpoint distinction; access versus refresh token use; `AuthToken`/`RefreshToken` cookie names; refresh-body-to-cookie fallback | Normal-request cookie/header precedence, cookie flags, full current refresh-rotation/epoch implementation, or a read-only permission/introspection substitute |
| [SESSION_MANAGEMENT_API.md](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/docs/SESSION_MANAGEMENT_API.md), blob `d09a77a49bad1682b6e37928eb21b0188d52670e` | Documents a revoked-JTI marker and 30-day TTL used by described session deletion flows | A universal retention rule for every current token type or Redis failure/missing-key behavior |
| [AUTH_SESSION_REVOKED_EVENT.md](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/docs/AUTH_SESSION_REVOKED_EVENT.md), blob `4e5642bb448012ebaf59800fe9c737b5e7208403` | Explicit revocation event and gateway matching intent; passive expiry is not covered by the event | An HTTP authentication decision, durable event-consumption guarantees, or permission/cache freshness |
| [PUNISHMENT_API.md](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/docs/PUNISHMENT_API.md), blob `70c5d54947ec2a686bf796da55481161ff0b64c3` | Documented permission blocking, exact/prefix matching, active-expiry condition, null/default result on a block | Complete current precedence among superuser, OAuth scope, account state, grants, groups and punishments |
| [SHARED_API_ERRORS.md](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/docs/SHARED_API_ERRORS.md), blob `129e74f6870ac375b3f800e273d0c5e1aa9f1c5f` | Application-error JSON fields, omission of nulls, camel-case `traceId`, broad default status/code families | Exact middleware challenge/denial/dependency responses or the global validation filter's behavior |

These documents coexist at the pinned revision but describe migration-era
Padlock/Shared paths and configuration. Coexistence is not proof that all
described paths remain the current implementation. In particular, the local
JWT normal path in `UNIFIED_JWT_AUTH.md` and session-cache-miss authority path in
`SHARED_CACHE_CONTRACT.md` must be reconciled by a current normative contract;
neither should silently replace the other.

## Owned implementation and caller findings

### Trusted identity and service-specific pipeline

- [PostWatchController.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostWatchController.cs)
  lines 43–73 requires `[Authorize]` on GET and PUT and uses
  middleware-provided `DyAccount.Id`. PUT adds
  `PermissionKeys.PostSubscriptionsManage`. Neither operation accepts an
  account-ID override from the request. The retained file is
  `ports/sphere/upstream/PostWatchController.cs`.
- Sphere, Develop, Messager and Wallet owned
  `Startup/ApplicationConfiguration.cs` register authentication, authorization,
  then `RemotePermissionMiddleware` before controllers. Their registration
  calls prove pipeline participation, not the middleware's internal behavior.
- [Passport's middleware registration](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Startup/ApplicationConfiguration.cs)
  instead uses authentication, `UseDyAuthModelProjection`, remote permissions,
  then authorization. Its blob is
  `baf63804f5ad39749155629b8b8daa9432b260a0`. This order cannot be replaced by a
  supposed universal pipeline.
- [AccountCurrentController.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountCurrentController.cs)
  requires an `SnAccount` context projection, not just an arbitrary UUID. Board
  GET and PUT both declare `AccountsProfileBoardManage`. The owned controller
  does not define the projection's current freshness/failure contract.

Authentication handling can affect an otherwise public controller when an
invalid or stale credential is supplied. The inspected registration calls do
not prove whether a given failure becomes an anonymous request or an immediate
error. Public-controller behavior alone is not full inherited HTTP parity.

### Direct permission consumers

Licensed owned call sites narrow the in-process evaluator shape:

- [ProgressionAdminController.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Progression/ProgressionAdminController.cs)
  lines 130–142, blob `f23caf795d2789003ba14102568dc396af7565fa`:
  rejects missing current user, locally allows `IsSuperuser`, otherwise calls
  `HasPermissionAsync` with `Actor = currentUser.Id.ToString()` and
  `Key = "progression.admin"`, then consumes `response.HasPermission`.
- [TicketController.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Ticket/TicketController.cs)
  lines 86–93, blob `a01879f5d3f73b24905c6acc3165f94e33098a4e`, uses the same
  actor form and boolean projection for `tickets.admin`.
- [PostActionController.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostActionController.cs)
  lines 214–230 and 374–384 passes the trusted account's string ID and
  `posts.create.blog`, with its own superuser/domain checks.

The caller actor value is a bare account UUID; the permission-node storage
example `user:<id>` in `PERMISSIONS.md` must not be substituted into that caller
request. These are distinct contracts. These calls establish neither numbered
wire fields nor transport metadata, caller authentication, or HTTP error mapping.
They also do not show that a raw account permission result includes OAuth/API-key
restrictions. Replacing `RemotePermissionMiddleware` with this boolean alone
would discard unverified policy layers.

### Redis missing/unavailable behavior: what was actually found

`SHARED_CACHE_CONTRACT.md` lines 78–97 explicitly covers missing keys, unexpected
codec, unparseable payload, and unexpected type for structured cache reads. They
become **misses**, after which the session flow validates through the auth service.
It does not say that a miss is a successful authentication. An absent authority
adapter therefore cannot turn a miss into a usable identity.

The same passage does not define connection refusal, timeout, partial marker
reads, or semantics for the separate revoked-JTI/account-version keys. Do not
generalize its structured-cache rule to security markers. The `dyson:` shared
cache prefix and unprefixed marker examples likewise do not independently prove
the fully normalized current marker storage keys.

The owned [Passport AccountService.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountService.cs),
blob `270436f5665cf8a2539c32719ed55580139ddfb9`, lines 26–64, implements a
different, five-minute `account:{id}:hydrated` profile cache. A miss invokes the
remote account service, ensures a profile and caches the hydrated result. Its
cache read lies outside the `RpcException(NotFound)` catch; if the cache wrapper
throws, this method does not catch it. The wrapper implementation is not present,
so that observation does not establish whether Redis errors are thrown, converted
to misses, or otherwise handled. This profile read is not token validation.

Bounded repository-scoped searches for `auth:revoked:jti`, `auth:account_ver`,
`auth:session:`, `AuthCacheKeys`, `SessionEpoch`, `DysonTokenAuthHandler`, and
`HasFlagAsync` found the above documentation and unrelated consumers, but no
owned implementation that settles current auth marker failures or session epoch.
The pinned root tree independently places Shared behind an unexpanded gitlink.
This is a bounded negative finding, not a claim about all possible licensed
sources outside this repository.

### Global request and data filters

All five owned services call `AddPaginationValidationFilter()` in
`Startup/ServiceCollectionExtensions.cs`. Retained Develop lines 28 onward and
Messager lines 40 onward show this directly; pinned Passport line 63, Wallet
line 41, and Sphere's owned registration agree. The implementations belong to
Shared. Known controller clamps/defaults cannot establish the earlier global
filter's accepted ranges, target parameters, ordering, response body, or whether
the controller is reached for malformed/negative values.

All five owned `AppDatabase.cs` files invoke `ApplySoftDeleteFilters()` and
`ApplyAuditableAndSoftDelete()`. Retained Develop lines 41–44 and 71, and
Messager lines 144–149, are concrete examples. Their calls do not expose the set
of applicable entity types, filter composition, deletion conversion, or audit
timestamp behavior. An explicitly coded `DeletedAt == null` predicate or SQL
index predicate is usable evidence for that specific path; it is not proof of
the complete shared filter. A migration snapshot's columns likewise do not
specify these runtime hooks.

## What can genuinely be implemented with this evidence

1. Use the exact `post.subscriptions.manage` constant and documented static key
   registry in a contract inventory. Do not treat registry membership as a grant.
2. Implement the documented generic application-error JSON representation and
   source-backed controller errors. Do not relabel unknown middleware errors as
   generic 401/403 responses and claim parity.
3. Continue source-backed domain policies, explicit controller predicates and
   storage operations behind mandatory trusted-identity/permission adapters.
   Their synthetic tests can use explicit resolved identities and authority
   results; these fixtures must not become production trust sources.
4. Build isolated conformance tests for documented RS256/lane/claim requirements,
   structured cache envelopes and miss-to-authority behavior. Passing them
   would prove only those documented slices. It would not enable a production
   success path while freshness/authority semantics remain missing.
5. Preserve distinct service pipelines and mark inherited middleware/global
   filters as unresolved in route parity results. Anonymous/controller-only
   tests remain useful but have a narrower claim.

A conservative new policy that rejects more requests may be safe in isolation,
but it is not established upstream compatibility. No production adapter should
be enabled merely because its defaults are restrictive.

## Minimal remaining eligible contract

The smallest unblocker is **current independently authored, appropriately licensed
interface/behavior documentation or owned caller/implementation evidence**, with
synthetic cases. It must not be exported or reconstructed from the excluded
protocol repository. Private credentials and production data are unnecessary.

### A. Verified identity and freshness

- Identify the actually supported validation path for these service revisions:
  local JWT plus authoritative state, an existing authority endpoint, or a
  documented combination. State how the two retained auth/cache documents apply.
- Specify supported credential schemes and cookie/header precedence; duplicate
  or conflicting credentials; current legacy grace applicability; required
  claims and types; `jti`/`sid` consistency; issuer/audience/key rotation and clock
  skew; refresh-token rejection.
- Specify mandatory state checks for revoked/expired sessions, disabled/deleted
  accounts, account version and refresh/session epoch. For each current Redis
  key, distinguish absent, malformed, stale, failed and unavailable reads,
  normalized key/value format, comparison rule, and authoritative fallback.
- Define success identity/session/token-restriction projection and exact outcomes
  for invalid, expired, revoked, stale, wrong-lane, disabled-account and unavailable
  dependencies. Include anonymous-route behavior and Passport model projection.

### B. Authoritative permission decision

- Confirm the real supported operation and transport, with authenticated
  actor/session binding, caller trust, request/result semantics and dependency
  failure mapping. The key spelling itself is now settled.
- Define ordering and interaction of OAuth/API-key scopes, wildcard/full scopes,
  superuser, account grants, groups, activation/expiration and punishments.
  Clarify which stages are part of the authority result and which must run in
  middleware. Existing documentation is insufficient to safely resolve every
  overlap.
- For a supported HTTP interface, provide method/path and synthetic success,
  deny and dependency-error examples. No such equivalent was established here;
  do not invent one. A permission-key catalog is not a decision endpoint.
- A first production implementation can omit optional local result caching if
  the supported authority contract allows it. Omitting a performance cache must
  not omit authoritative revocation/version checks.

### C. Inherited filters for endpoints that use them

- Pagination: applicable arguments/locations, defaults/limits, rejection versus
  clamping, malformed/null/overflow behavior, filter order and exact error JSON.
- Data: applicable entity types and soft-delete predicates, includes/joins and
  explicit ignore behavior, audit/default timestamp rules and deletion-to-update
  semantics for the tables being ported.
- Supply synthetic request/response or query-result fixtures for boundaries.
  Tests built from guessed hooks cannot serve as the upstream oracle.

## Corrections needed in existing documents

This audit did not edit existing `internal/auth` documents. During recovery they
were replaced with a newer version; the current `CONTRACT.md` already states the
exact permission key and known cookie names. Do not restore their earlier
delegated-only/unresolved-spelling wording.

Recommended reconciliation:

- Link the retained `source-docs/manifest.json` and the precise source sections
  wherever the key was described as delegated evidence only or unavailable.
  `docs/policy-followup.md`, earlier checkpoint prose, and older provenance text
  may still contain that historical limitation.
- Replace blanket statements that issuer/audience/key configuration, cookie
  names, or permission concepts are wholly unknown with the known documented
  baseline plus the exact current-behavior gaps above.
- Add the explicit structured-cache miss-to-authority rule without assigning it
  to version/revocation markers or Redis outages.
- Retain the narrow HTTP conclusion: no sufficiently specified HTTP identity
  plus permission authority equivalent has been established. Do not phrase this
  as an absence of all lawful authentication evidence.
- Add service-specific pipeline/projection and pagination/soft-delete caveats to
  broader HTTP parity claims. Permission constants, docs, and controller tests
  do not by themselves complete protected endpoints.
- Keep the excluded-protocol-export rescission and no-production-probe boundary.

## Verification and stopping condition

Verified: eight retained document byte streams against their pinned Git blob
SHAs; SHA-256 and lengths recorded; retained license blob matched the pinned
root license; pinned owned callers and pipeline registrations inspected.
No Go or production tests were needed for this documentation-only change.

The remaining source boundary is concrete: current shared implementation is a
separate, unexpanded dependency, and the permitted owned code/documents inspected
do not resolve its security-critical choices. Continue independent eligible
backend work. Resume production identity/filter integration only when the above
eligible current contract is available; do not widen source access or infer the
missing semantics merely to remove a blocker.
