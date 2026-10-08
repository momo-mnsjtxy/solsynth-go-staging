# Owned OAuth authorization-code and OIDC sign-in

Base: `2e6e25f0a9ba31659119b5981338c08edde7bd43`. This delivery implements
authorization-code consent, S256 PKCE, canonical delegated sessions, signed OIDC
ID tokens, scoped userinfo, refresh rotation and durable revocation. It uses the
existing authority's issuer/key material, canonical accounts/sessions, developer
apps, app credentials and app consents. It creates no separate account identity
or signing authority. No provider is contacted or dynamically trusted.

## Evidence and compatibility

`ports/authority/oauth/source-evidence.json` pins seven retained authored Solian
files to `ffd0838e5f04cb40bea311a39118ebeedad210c1`, with original blobs, verified
SHA-256 values and retained root-license evidence. The authorization screen
establishes GET and form POST `/auth/open/authorize`, the client/redirect/scope/
state/response-type/PKCE parameters, explicit `authorize=true|false`, and the
`redirect_uri` response. The authored metadata parser establishes client name,
slug, description, homepage and scope fields. The authored app model supplies
registration options; Watch supplies the form refresh-token transport and
`access_token`, `refresh_token`, `expires_in` response vocabulary.

The server lifecycle is independently authored. No excluded Stargate server,
SoSYS, NeTo/Spec, generated SDK or generated protocol definitions were consulted.
The authorized-apps UI calls excluded SDK methods, so their route paths are not
guessed; this slice has a separately documented consent-revocation endpoint.
Full legacy OAuth, native provider login, Watch device-code and all shared DTO
parity are not asserted.

Normative design references:

- [RFC 6749](https://www.rfc-editor.org/rfc/rfc6749.html): code/client/redirect
  binding, one-use codes, form token requests and errors.
- [RFC 7636](https://www.rfc-editor.org/rfc/rfc7636.html): S256 challenge and
  verifier grammar.
- [RFC 9700](https://www.rfc-editor.org/rfc/rfc9700.html): exact registered
  redirects, PKCE, and refresh-family invalidation after replay.
- [OIDC Core, errata set 2](https://openid.net/specs/openid-connect-core-1_0.html):
  nonce binding, ID-token authentication claims and scoped userinfo.

## Executable scope and identity boundary

Only `openid`, `profile`, `email`, and `offline_access` execute here. `openid` is
required. Requested scopes must all be known, registered and explicitly approved;
no wildcard or implicit resource permission is granted. `offline_access` also
requires the app's offline flag and registered refresh grant. An absent offline
scope produces no refresh token. Profile claims are canonical name, preferred
username and locale; email and its verification status come from canonical signin
credentials and are omitted when no credential record exists.

Access tokens use the existing RS256 signing authority and configured API audience
with the distinct `token_use=oauth-v1`. Only `Verifier.AuthenticateOAuth` accepts
that lane. Ordinary bearer verification rejects these tokens, as does the
first-party refresh endpoint. ID tokens use the registered client slug as their
audience and carry issuer, subject, session ID, issue/expiry/authentication times,
and the original nonce when supplied. They cannot be used as access tokens.

Resource API scopes are deliberately rejected pending a reviewed host route-to-
scope mapping. Several ordinary account reads require authentication without a
named permission; accepting delegated tokens there would exceed this consent.
Do not put `AuthenticateOAuth` into the ordinary account middleware. Token
verification still checks the exact configured issuer, audience, signature key,
session/category, account version, session epoch and current database binding.
Unknown issuers, algorithms, keys, scopes, session categories and request fields
fail closed.

`IssueSession` now accepts only Type-0 first-party sessions. No existing runtime
caller in the frozen source used Type-1; one old synthetic unit fixture did and
now asserts denial. Positive delegated issuance, rotation, ID-token verification,
proof ownership and parent cascade have separate passing unit coverage. The
trusted `IssueOAuthTransaction` seam is the only new Type-1 creation path.

## Registration, consent and code lifecycle

The client identifier is the exact canonical app slug. The app, project,
publisher and owner must be live, and app status must be production. Registration
must explicitly enable authorization_code and PKCE. Only authorization_code and
refresh_token grant names are accepted. Redirects match a registered string
exactly, including path, case, query and port. HTTPS is required except for
explicitly registered HTTP loopback IP redirects. No wildcard or dynamic-port
matching occurs. Fragment, credential-bearing and prepopulated OAuth-response
parameter redirects are rejected.

A first-party bearer session requests a preview with the complete authorization
request. The server retains a five-minute request fingerprint, state hash,
original account/session, app epoch and owner identity/version. Repeating the
same live preview is idempotent; changing any bound request field, changing the
parent session, or reusing a completed/expired state fails. State is required,
16–256 printable ASCII characters. Optional nonce has the same bound. PKCE uses
only canonical S256 challenges and 43–128-character verifiers.

The subsequent form POST must explicitly approve or decline that preview. A
decline returns a registered redirect with `access_denied`, original state and
issuer. Approval writes consent and a one-minute, one-use, random 256-bit code in
the same transaction. Only a domain-separated SHA-256 code hash is stored. The
JSON redirect contains the code, original state and exact issuer. Invalid client
or redirect requests never receive an automatic redirect. The client must also
verify the returned state and issuer before exchanging the code.

Token exchange rechecks the current client, exact redirect, PKCE, original parent
session, app/owner epochs, consent version/scopes and expiry. Code consumption,
canonical Type-1 child session, grant, refresh hash and audit commit together.
Any failure rolls back issuance and code consumption. The grant lifetime is
bounded by consent, original parent session, canonical session policy, and the
confidential credential's expiry. Refresh cannot extend that absolute lifetime.

Public clients send no secret. Confidential clients authenticate with an existing
canonical app secret explicitly scoped `oauth.client.authenticate`, using Basic
or form authentication, never both. That purpose is independent of user scopes;
the marker does not confer account or resource authority. Existing app-secret
creation/rotation/revocation APIs retain their normal guarded owner policy. This
slice does not issue a production credential or contact a third-party provider.

## Revocation and concurrency

The canonical app schema gains `oauth_epoch`; security-relevant status, slug and
OAuth registration changes increment it. Display-name/description/link edits
preserve it. Existing app deletion increments it. Canonical app consent gains a
monotonic `version`; scope changes, expiry renewal and revocation increment it.
Codes and grants bind both values and the owner account's identity/version, so
disable/re-enable or revoke/re-consent cannot revive an old grant.

All issuance/refresh transactions first resolve and sort the complete user and
client-owner account set, lock it, then lock publisher, project, app, consent and
grant state. The exact original verifier proof is revalidated in the same SQL
transaction for user decisions. Code/refresh replay handling commits its family
revocation before returning invalid_grant; it never rolls that revocation back
merely because the operation returns a denial.

Every delegated verification checks canonical grant, app, owner, consent, original
parent and credential state in the same authority transaction/snapshot. Read-only
verifiers use their coherent repeatable-read snapshot. Guarded writes already own
the user row and use a NOWAIT boundary for secondary owner/app/consent/grant locks;
contention returns a retryable unavailable result instead of waiting in an inverted
account order. Revoking app or consent authority does not require locking all users
after app locks. Canonical account disable, account version changes, parent expiry,
parent session revocation, app suspension/deletion, consent removal and app-secret
rotation/revocation all invalidate delegated access.

Each refresh succeeds once, rotates its hash and advances the canonical session
epoch. Any reuse revokes the whole family, including a concurrently issued winner.
There is deliberately no refresh replay grace. Clients should serialize refreshes;
an uncertain response may require renewed authorization. Original refresh hashes
remain available for replay detection for the family's lifetime. Canonical
first-party refresh lookup never contains those hashes.

ID tokens are bounded signed authentication assertions; an external client doing
offline signature validation cannot observe instantaneous server revocation.
Current delegated access and userinfo always recheck canonical state. This slice
does not implement token introspection, front/back-channel logout or an external
revocation notification protocol.

## HTTP and host wiring

The handler accepts both the `/stargate` prefix and direct paths. Public metadata
routes and client-authenticated token routes must be dispatched before ordinary
user-bearer middleware. `oauth.RouteMatches` identifies its exact routes.

| Route | Method and contract |
| --- | --- |
| `/auth/open/authorize` | GET complete query preview; POST form with explicit authorize decision |
| `/auth/open/token` | POST form authorization_code or refresh_token |
| `/auth/open/userinfo` | GET delegated bearer; current scoped canonical claims |
| `/auth/open/revoke` | POST form token/client authentication; revoke a refresh family |
| `/auth/open/consent` | DELETE form client_id under original first-party bearer |
| `/auth/open/jwks` | GET configured public verification keys |
| `/.well-known/openid-configuration` | GET configured discovery metadata |

The source consent endpoint is a JSON/form backend for the existing frontend,
not a rendered browser page. Set `Service.AuthorizationEndpoint` to an explicitly
deployed HTTPS consent UI that invokes it. Discovery returns 503 without this
configuration, while API consent/token operations remain usable. No UI URL is
invented or fetched. The UI must authenticate the user, display this preview,
collect the actual decision and return the response redirect to the user agent.

Construct using the existing pool, store, issuer, exact verifier/guard pair and
app repository:

```go
repo, err := oauthpg.NewWithAuthority(db, authorityStore, issuer, guard, appRepo)
handler := (&oauth.Service{
    Repository: repo, Verifier: verifier, Metadata: issuer, Bind: guard.Bind,
    AuthorizationEndpoint: configuredConsentUI,
}).Handler()
```

Apply versioned `apppg.Schema`, canonical signin credentials, then `oauthpg.Schema`.
Add `repo.Ready(ctx)` to readiness. Add `repo.ProcessDue(ctx, 100)` to bounded host
maintenance. Its cap is 100; it removes expired request/code and grant/refresh
roots after a 24-hour evidence window, acquiring canonical account locks with
SKIP LOCKED. It preserves canonical sessions and audit records. Active families
retain their refresh history; limits are 32 active previews, 128 live grants per
account and 1024 rotations per family. Reaching the rotation bound requires new
authorization.

No host/router/CLI file is changed here. The integration owner must merge these
hooks while preserving existing transport and maintenance composition. Confidential
client authentication and the consent UI require explicit operator configuration;
there is no default client registration or seeded quota/grant/secret.

## Schema and limits

Five new tables: `owned_oauth_requests`, `owned_oauth_codes`, `owned_oauth_grants`,
`owned_oauth_refresh`, `owned_oauth_audit`. New indexes cover expiry, account/app
grants, revoked grants and refresh-family history. Foreign-key cascades apply only
to owned temporary code/refresh records, never to accounts or canonical sessions.
Two canonical columns are added through the existing app migration: app
`oauth_epoch` and consent `version`, both positive and initially 1.

Transport bodies are capped at 16 KiB. Duplicate parameters, unknown fields,
query credentials, spoofable identity headers, cookies, mixed client authentication,
unsupported response types/grants and weak PKCE methods are rejected. Responses
use no-store, no-cache, no-referrer and sanitized errors. Logs and audits do not
store bearer tokens, authorization codes, code verifiers or client secrets.

## Verification and remaining work

Race tests pass for authority, OAuth transport/validation, app transport, app PG
adapter and authority PG adapter. Focused vet and whole-repository build pass.
The following exact PG cases compile; actual execution remains required in the
guarded parent runner. No local database process/socket or production service was
started. All accounts, client registrations and keys in tests are synthetic.

- `TestPGOAuthAuthorizationCodePKCEConsentAndUserInfo`
- `TestPGOAuthRedirectStateNonceAndParentBindings`
- `TestPGOAuthConcurrentCodeAndRefreshReuse`
- `TestPGOAuthAppOwnerConsentAndSecretRevocation`
- `TestPGOAuthParentRevocationAndTokenExpiry`
- `TestPGOAuthAtomicRollbackAndCanonicalProof`
- `TestPGOAuthOwnerDisableDuringLockWait`
- `TestPGOAuthOpposingOwnersAndRetryableGuardBoundary`
- `TestPGOAuthScopeMinimizationAndBoundedCleanup`
- `TestPGOAuthRestartPersistence`

Run the restart test in both before/after phases. It stores synthetic signing/hash/
seal keys and synthetic expiring token fixtures only under the already guarded
disposable root with mode 0600; it verifies real persisted canonical state and
rotates the refresh token after restart. Other cases cover valid confidential and
public flow, exact bindings, authority proof ownership, rollback without orphan
sessions, negative scope/PKCE cases, opposing owners, revocation and expiry.

Remaining compatibility gaps: resource API delegation, Watch device flow, external
provider login, implicit/password grants, dynamic client registration, full legacy
authorized-app listing/scope-edit paths, rendered consent UI, logout protocols and
token introspection. The revoke endpoint currently accepts refresh-token families;
access-token revocation is available through consent or canonical session removal.

## Integrated optional host configuration

The composed backend routes the exact OAuth family before ordinary bearer middleware and shares the canonical issuer, verifier, app repository and transaction guard. `--enable-oauth` requires explicit issuer key/hash/seal inputs and `--oauth-consent-url` identifying a deployed HTTPS consent UI. This flag does not enable first-party registration or factor enrollment; those still require `--enable-signin`. Expired authorization records use the existing bounded lifecycle maintenance. Ordinary backend bearer routes continue rejecting delegated tokens. The composed test covers preview, consent, token exchange, scoped userinfo, ordinary-route denial and parent-session revocation.
