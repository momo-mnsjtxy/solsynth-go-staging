# PostWatch authentication adapter: proposal and unresolved contract

Current correction (delegated recovery report, 2026-10-07): the permission key is
`post.subscriptions.manage`. Historical statements below that the spelling is
unresolved are superseded; its source proof is in the recovery work, which this
checkout has not yet recovered. Current-version identity checks, authoritative
permission evaluation, Redis missing/failure/version/epoch behavior, credential
precedence and exact errors remain unresolved. Do not enable access from the key
alone. No production authentication is implemented.

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

No cookie-based HTTP authentication names, precedence or settings are established
by the inspected client. The absence of cookies in this client does not prove
that server/browser integrations do not support them.

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

## Exact unresolved requirements

| Requirement | Known | Missing input |
|---|---|---|
| PostSubscriptionsManage | Symbolic C# permission name on PUT | Authoritative exact wire string and permission-evaluation contract |
| Current account | Middleware-provided DyAccount.Id UUID | Auth RPC/protocol, required input metadata, trusted endpoint configuration |
| JWT verification | README says RS256 and fleet RSA keys | Issuer, audience, key lookup/rotation, allowed claims/clock skew, session epoch/account-version validation |
| Key discovery | None established | Whether a JWKS/discovery route applies to these access tokens; exact verified URL/configuration |
| Introspection | Blade invokes shared gRPC authenticator | Service full name, RPC method, numbered message fields, response and failure semantics |
| Cookies/API keys | Shared token extractor is used elsewhere | Supported schemes, names, precedence, applicability to PostWatch |
| Permission errors | Attribute present | Exact denied/error response code/status/detail and dependency-failure behavior |
| Authentication errors | Explicit controller unauthenticated case 401 UNAUTHORIZED; client expects 401 TOKEN_EXPIRED | Distinction among expired, revoked, invalid, disabled-account and unavailable-auth cases |
| Refresh | Client POST and README rotation/grace | Complete authoritative refresh contract; should remain handled by identity service |
| Cache/revocation | README names session cache and event; Blade consumer projection observed | TTL/version checks, reliable invalidation and stale-result behavior for HTTP requests |
| RPC transport trust | Blade has configurable TLS settings | Selected deployment endpoint, trusted CA/server identity and caller authentication |

Organization-scoped licensed source searches found PostSubscriptionsManage only
as a symbolic use in PostWatchController; no exact wire-value declaration was
found. A search for a plausible dotted spelling found no evidence. No guessed
string is approved.

## Proposed implementable design

Implement only after the missing authoritative contract is supplied:

1. An Authenticator verifies supported request credentials through the existing
   authoritative identity service and returns an immutable verified account ID,
   session identity and relevant token restrictions
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

## Minimal input needed to proceed

- Independently derived licensed-client contracts or separately licensed public
  HTTP/interface documentation with synthetic request/response/error examples;
  no definitions exported from the excluded unlicensed protocol repository
- Exact PostSubscriptionsManage wire key and permission semantics
- Token type/validation and revocation requirements relevant to PostWatch
- Verified deployment trust configuration, supplied without private credentials

Without these, adding a production authentication adapter would invent security
semantics. Existing local development authentication must remain visibly local
and must not be described as real Stargate integration.

## License and access boundaries

The inspected DysonNetwork, Solian and Blade repository license files identify
GNU AGPL v3. No separate Shared/NeTo/SoSYS implementation, protocol definitions,
descriptor sets or generated bindings have been imported in this scope.
No cancelled Stargate clone was retried, no further Stargate content was fetched,
and no denied SoSYS repository page was retrieved.
