# PostWatch authentication adapter: proposal and unresolved contract

Status: research only. No authentication implementation, credentials, RPC wire
guesses, live-account probes or deployment changes accompany this document.
Production parity is not established.

Scope correction, 2026-10-07: the user identified the protocol repository as
unlicensed and excluded its definitions. Earlier suggestions to request exported
.proto files or descriptor sets are rescinded. Do not obtain, copy or use that
repository's schemas, descriptors or generated bindings, including user exports.
Continue only with independently derived licensed-client call-site contracts or
separately licensed public interface documentation. Missing authority stays closed.

## Verified licensed call-site requirements

### Trusted identity and permission boundary

At DysonNetwork revision 4ee2d0cebb9da8284a92483fee6dce795bf6947c,
PostWatch GET and PUT have Authorize. Both consume a DyAccount set by trusted
middleware in HttpContext.Items["CurrentUser"], then parse currentUser.Id as UUID.
They do not derive identity from request body, query, arbitrary account headers
or an unsigned JWT payload.

PUT additionally declares AskPermission(PermissionKeys.PostSubscriptionsManage).
The owned PERMISSIONS.md registry verifies its wire value: post.subscriptions.manage.
GET has no corresponding permission attribute in this controller.

Source:
[PostWatchController](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Post/PostWatchController.cs)

Sphere registers AddDysonAuth, then UseAuthentication, UseAuthorization and
RemotePermissionMiddleware. Implementations live in its separate Shared
dependency, which has not been retrieved or assumed licensed.

Sources:
[Program](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Program.cs),
[middleware registration](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Sphere/Startup/ApplicationConfiguration.cs)

### Gateway is not an identity assertion

Blade revision dbded8c29f1cba55dd67086c786ac4e7432096ca implements its ordinary
HTTP gateway as a reverse proxy. It routes a service-prefixed URL to /api and
forwards the request; this code does not authenticate PostWatch requests or
establish a trusted account identity. A request arriving through Blade therefore
does not justify trusting arbitrary X-Account-ID-style headers.

[Blade proxy](https://github.com/Solsynth/Blade/blob/dbded8c29f1cba55dd67086c786ac4e7432096ca/internal/proxy/proxy.go)

Blade's separate WebSocket route constructs a shared gRPC TokenAuthenticator,
wraps it in a cached authenticator and calls AuthenticateRequest. The result
contains Account and Session. This supports an authoritative-authenticator
architecture, but does not reveal the shared gRPC message descriptors, cache
invalidation details or HTTP token-extraction behavior.

Sources:
[Blade startup](https://github.com/Solsynth/Blade/blob/dbded8c29f1cba55dd67086c786ac4e7432096ca/cmd/main.go),
[WebSocket auth call site](https://github.com/Solsynth/Blade/blob/dbded8c29f1cba55dd67086c786ac4e7432096ca/internal/wsgateway/http.go)

### Known client token flow

Licensed Solian revision ffd0838e5f04cb40bea311a39118ebeedad210c1:

- Adds Authorization: Bearer followed by its current access token
- Recognizes HTTP 401 with JSON code TOKEN_EXPIRED as the refresh signal
- Deduplicates refresh calls and retries the failed request at most once
- POSTs JSON to /stargate/auth/token with grant_type=refresh_token and
  refresh_token when available
- Reads token, refresh_token, expires_in and refresh_expires_in from the result
- Uses exp/iat decoded locally for scheduling only; this is not server-side
  proof of token authenticity

[Solian networking](https://github.com/Solsynth/Solian/blob/ffd0838e5f04cb40bea311a39118ebeedad210c1/lib/core/network.dart)

The client does not establish cookie precedence. The subsequently inspected owned
PASS_TOKEN_ENDPOINTS.md documents AuthToken and RefreshToken cookies, refresh
body-to-cookie fallback, and cookie deletion on logout. Current extraction
precedence and cookie security flags remain unverified.

### Known session/revocation behavior

The Stargate README already read before the access cancellation, at revision
01dfcdd7e633ca7abc82306684ae4b8dd37dd2f0, documents RS256 JWTs using existing fleet
RSA keys, refresh rotation incrementing session epoch and revoking prior tokens,
and immediately previous refresh-token grace of default 60 seconds. It identifies
interoperable session cache keys and auth.session.revoked events. These are
documentation claims, not a substitute for exact validation/cache contracts.

[Previously read Stargate README](https://github.com/Solsynth/Stargate/blob/01dfcdd7e633ca7abc82306684ae4b8dd37dd2f0/README.md)

Blade's own revocation consumer listens on auth.session.revoked and reads
event_id, event_type, session_id, account_id, client_id and device_id. It
disconnects matching WebSocket connections, choosing device_id then client_id.
This verifies a consumer projection, not the entire event-delivery or HTTP cache
revocation contract.

[Blade revocation consumer](https://github.com/Solsynth/Blade/blob/dbded8c29f1cba55dd67086c786ac4e7432096ca/internal/wsgateway/revocation_nats.go)

## Owned-document reassessment, 2026-10-07

Earlier claims that no lawful auth contract or exact permission value existed
were too broad and are superseded by these owned AGPL docs at the same pinned
revision. No links into the excluded Shared/protocol repository were followed.

- [PERMISSIONS.md](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/docs/PERMISSIONS.md): exact post.subscriptions.manage key; OAuth prefix wildcard/full scopes, groups, timed nodes, punishment-based denial precedence; public GET /.well-known/permissions is a key catalog, not an account authorization decision
- [UNIFIED_JWT_AUTH.md](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/docs/UNIFIED_JWT_AUTH.md): RS256 Bearer user and Bot API-key lanes; required iss/aud/sub/jti/sid/exp/iat/nbf/token_use/ver claims; signature, issuer, audience, expiry/not-before and Redis revocation/version checks; configured public key path rather than an established JWKS dependency
- [PASS_TOKEN_ENDPOINTS.md](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/docs/PASS_TOKEN_ENDPOINTS.md): access/refresh separation, refresh-cookie fallback, session extension only during refresh, cookies and rejection of token_use=refresh as ordinary API credentials
- [SHARED_API_ERRORS.md](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/docs/SHARED_API_ERRORS.md): code/message, optional status/detail/traceId/errors/meta, omitted nulls, camelCase traceId, standard 401 UNAUTHORIZED and 403 FORBIDDEN
- [SHARED_CACHE_CONTRACT.md](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/docs/SHARED_CACHE_CONTRACT.md): dyson-prefixed hash data/codec/type/cached_at envelope; TTL-based expiry; malformed cache treated as miss; group/reverse-index removal and leased owner-token locks
- [AUTH_SESSION_REVOKED_EVENT.md](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/docs/AUTH_SESSION_REVOKED_EVENT.md): auth.session.revoked subject, auth_session_events stream, explicit revocation fields and session-scoped disconnects; excludes passive expiry
- [SESSION_MANAGEMENT_API.md](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/docs/SESSION_MANAGEMENT_API.md): documented revocation JTI marker and 30-day TTL; session/device routes, filtering and headers

### Currency and remaining gaps

1. JWT/token docs last changed March 26, before the Stargate replacement, and
   still name Padlock plus a March 20 legacy-token deadline. The current client
   uses /stargate/auth/token. Do not equate old deployment defaults with current
   fleet settings merely because the docs remain in the tree.
2. The May 16 shared-cache guide describes authoritative validation on a session
   cache miss; the March JWT guide describes local validation. Establish which
   current flow applies to PostWatch and how session epochs interact with ver.
3. Redis logical revocation/account-version names are documented, but current
   raw versus dyson-normalized keys, marker/version encoding, absent-version
   behavior and authority/cache-outage behavior are not fully established.
4. Permission semantics and the exact key are documented. The current evaluator's
   authoritative data source/transport, time boundaries and precise scope,
   superuser and punishment ordering still need eligible evidence where prose
   is incomplete or ambiguous. Do not replace account grants with token scope alone.
5. Public key path, issuer and audience configuration keys are documented; their
   selected deployment values, key rotation and clock tolerance are not. A JWKS
   endpoint is not required unless the actual supported deployment says so.
6. Error envelope is resolved; exact current invalid/expired/revoked/disabled/
   dependency-failure code mapping and credential extraction precedence remain.
7. Session examples contain older camelCase shapes unlike current snake_case
   startup configuration; treat those examples as historical, not full fixtures.

[MODEL_CONFIGURATION_REWORK.md](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/docs/MODEL_CONFIGURATION_REWORK.md)
concerns AI model/kernel selection, not EF model filtering or timestamps. It does
not resolve ApplySoftDeleteFilters or ApplyAuditableAndSoftDelete. The board
candidate's row/audit default gate therefore remains justified.

## Proposed implementable design

Implement only after the missing authoritative contract is supplied:

1. An Authenticator follows the verified current generation: either the documented
   local RS256 plus authoritative Redis state checks, or a documented authority
   adapter. It returns immutable verified account/session identity and restrictions
2. An Authorizer evaluates the verified account/session against the confirmed
   PostSubscriptionsManage capability for PUT; GET only requires identity unless
   the verified inherited middleware contract adds requirements
3. Transport middleware places the result in request context. The PostWatch
   handler receives no user-controlled identity override
4. Invalid credentials, missing authoritative adapters and auth-service failures
   never grant access. Preserve the documented distinction between authentication
   failure, authorization denial and dependency unavailability once established
5. Keep refresh in Stargate/client flow; do not mint tokens or reinterpret refresh
   tokens as access tokens. Do not add a JWT-only shortcut that ignores revoked
   sessions or account/session versions
6. Begin with no local auth-result cache. Add caching only with a verified
   revocation/version contract; fresh authorization must remain authoritative
7. Test using synthetic credential fixtures and fake authority responses, then
   isolated integration services. Do not probe production accounts

The adapter can depend on narrow local interfaces without importing NeTo
implementation. A test-only fake authority is useful but must not become a
production default. A C# host retaining its existing authoritative middleware
could be a future staged boundary, but any identity handoff to Go would itself
need a documented, authenticated channel and explicit deployment approval.

## Minimal next evidence needed

- Current-version confirmation of the documented validation flow, selected public
  key/issuer/audience configuration and credential precedence, without secrets
- Current Redis key normalization, values and missing/outage/version/epoch semantics
- Eligible documented permission-evaluator input/data/decision/error contract for
  the already-known post.subscriptions.manage key, including scope restrictions
- Synthetic current success/denial/revoked/expired fixtures to resolve doc drift

No excluded protocol definitions, descriptor sets, generated bindings or exports
are requested. Existing baseline docs support independent reconstruction, but
current production integration remains unverified. Standard errors and the exact
permission value are no longer missing evidence.

## License and access boundaries

The inspected DysonNetwork, Solian and Blade repository license files identify
GNU AGPL v3. No separate Shared/NeTo/SoSYS implementation, protocol definitions,
descriptor sets or generated bindings have been imported in this scope.
No cancelled Stargate clone was retried, no further Stargate content was fetched,
and no denied SoSYS repository page was retrieved.
