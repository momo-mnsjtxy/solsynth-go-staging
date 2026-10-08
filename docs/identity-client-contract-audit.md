# Licensed client and gateway identity contract audit

Date: 2026-10-08. This extends `identity-resolution-audit.md`; it does not
replace that document's pinned DysonNetwork evidence.

## Result

The broader eligible-client pass establishes concrete HTTP account/session
routes, authored response projections, platform-specific refresh/error behavior,
and owned gateway routing and WebSocket lifecycle behavior. The earlier evidence
was incomplete, rather than proof that these contracts were unavailable.

An HTTP client for the documented/currently authored account lookup can now be
implemented and tested against synthetic responses without protocol definitions.
That is narrower than a production replacement for shared authentication and
permission middleware. Global permission-decision transport, authoritative token
freshness policy and inherited filters are still **not established by the
inspected eligible evidence**. No production authentication success path was
implemented or enabled in this audit.

## Source boundary and coverage

### Solian

- Repository: `Solsynth/Solian`, pinned
  `ffd0838e5f04cb40bea311a39118ebeedad210c1`.
- License: root GNU AGPL v3 `LICENSE.txt`, verified before new source retention;
  Git blob `bae94e189e62df1b8d5bd11435d954b25bee9d7b`, 34,522 bytes,
  SHA-256 `4df3c306dddaaf4baffdff5ca820cc679ac8cd6dc263c6a74517783e42fa7a3b`.
- Retained 59 exact authored source/test/config files, 1,105,861 bytes, plus
  license in `inventory/identity-client-evidence/solian/`.
- `manifest.json` records every retained upstream file's path, pinned URL, Git
  blob SHA, SHA-256 and byte count. All match the retrieved bytes.
- The complete pinned tree was untruncated. All 23 authored Dart files under
  `lib/auth/` were retained; the three generated companion files were unnecessary
  and were not read. Core networking, API-error parsing, WebSockets, account
  bootstrapping, device/session/authorized-app UI, relevant native Swift/Kotlin
  networking and auth, and adjacent source tests were retained and inspected.
- `coverage.json` records the auth-subtree census and 28 repository-scoped
  indexed-search summaries. Searches are discovery aids, not an exhaustive
  proof of absence. Broad searches can hit result limits. This is not a claim
  that every Solar Network client or every Solian feature was audited.

Solian's authored `pubspec.yaml` delegates many typed API/model operations to
`solar_network_sdk` under the separate `socommon` gitlink. The parent confirmed
Socommon lies inside the unresolved earlier canceled per-repository retrieval
scope. It was **not accessed**: no metadata, source or mirror lookup. Its license
and permission boundary are unresolved, not proven absent or unavailable. The
Solian parent license does not license that dependency. A typed SDK method name
alone does not establish its HTTP path, serialization or error handling.

### Blade

`Solsynth/Blade` at `dbded8c29f1cba55dd67086c786ac4e7432096ca` has independently
verified root AGPL license evidence. All 46 authored Go source/test files in
its complete pinned tree and relevant owned docs/config are covered by
`identity-blade-contract-audit.md`. Its original 58-file inspection and publication-safe 53-file retained subset (five deployment configs omitted) and
verification records are in `inventory/identity-client-evidence/blade/`.

No Stargate retrieval, SoSYS 403/canceled-access retry, excluded NeTo/Spec
protocol/schema/descriptor/generated-binding read, dependency expansion,
production request, credential use, remote write or deployment occurred. Source
tests were inspected, not executed. No source repository's availability or
license is inferred from a canceled access path.

## 1. Concrete callable paths found in authored clients

Paths below are relative to the user's configured gateway base URL. They are
static request contracts used by the pinned client, not a verified live
deployment or proof of every server response.

| Operation | Direct authored evidence | Interpretation |
|---|---|---|
| `GET /stargate/accounts/me` with Bearer | Watch `Services/NetworkService.swift:652–691`; Dart `auth/web_auth/web_auth_server.dart:125–128`; `accounts/account_pod.dart` delegates to SDK and records this source path | Concrete current-account lookup; authored Swift `SnAccount` parsing is retained. Not a global permission decision. |
| `GET /stargate/sessions?offset=...&take=...&includeChildren=false` | Watch `NetworkService.swift:804–838` | Reads root session list; default take 50/offset 0 in this client, requires 2xx, then decodes `[SnAuthSession]`. The source comment mentions `X-Total`, but this method does not consume it. |
| `DELETE /stargate/sessions/current`, `/other`, `/{id}` | Watch `NetworkService.swift:841–857` and `deleteChecked:426–442` | Explicit revocation operations; helper accepts 2xx. These mutate state and are not token-validation substitutes. |
| `POST /stargate/auth/token`, JSON refresh grant | Dart `core/network.dart:1175–1245`; iOS `Services/DataExchange.swift` | Sends `grant_type=refresh_token` and stored refresh token. Reads `token`, `refresh_token`, `expires_in`, `refresh_expires_in`; may preserve the previous refresh token if omitted. |
| Same first-party token path, JSON authorization-code grant | Dart `auth/login_content.dart:171–191` | Sends `grant_type=authorization_code`, `code`; stores returned pair and runs post-login flow. This is issuance, not introspection. |
| `POST /stargate/auth/open/device/code` | Watch `StandaloneAuthService.swift:241–285` | Form request with client ID, scope, device ID/name. Authored code consumes device/user code, verification URI, optional complete URI, expiry and polling interval. |
| `POST /stargate/auth/open/token` | Watch `StandaloneAuthService.swift:310–374` | Form device-code or refresh grant, with client ID. Reads `access_token`, optional refresh token and expiry. This is the OAuth lane, distinct from first-party JSON token exchange. |
| `GET /stargate/auth/open/device/code/{userCode}`; `POST .../{userCode}/approve` or `/decline` | Dart `auth/authorize_screen.dart:80–140`, using `stargateApiClientProvider` | Device-flow review/decision. It grants or rejects a new session and cannot be repurposed as a read-only per-request authority. |
| `GET`/`POST /stargate/auth/open/authorize` | Dart `auth/authorize_screen.dart:93–150` | Reads client/consent metadata; POST uses form fields including `authorize` and supplied OAuth parameters. No global permission check is established. |
| `POST /stargate/auth/login/session` | Dart `auth/web_auth/web_auth_server.dart:299–318` | Creates a child session after a separate Develop app-challenge validation. Returns token-pair fields. Mutating credential issuance, not an authentication shortcut. |
| `GET /stargate/accounts/{username}/punishments/overview` | Dart `auth/login_content.dart:194–224` | Used when login receives 423; UI fetches punishment information best-effort. Does not specify disabled-account behavior on ordinary protected APIs. |

Other authored auth calls include challenge creation/read/factor operations,
QR generation/status, recovery, and external-provider login. The entire authored
auth subtree is retained so those calls are reviewable. Some response objects
still rely on the unaccessed SDK. The pending-challenge listener documents a
`GET /stargate/auth/challenge/pending` catch-up path, but its actual request is
through `client.auth.getPendingChallenges()`; keep that provenance distinction.

Useful source links:

- [Watch direct HTTP client](https://github.com/Solsynth/Solian/blob/ffd0838e5f04cb40bea311a39118ebeedad210c1/ios/SolianWatchApp/Services/NetworkService.swift)
- [Watch OAuth client](https://github.com/Solsynth/Solian/blob/ffd0838e5f04cb40bea311a39118ebeedad210c1/ios/SolianWatchApp/Services/StandaloneAuthService.swift)
- [Flutter networking](https://github.com/Solsynth/Solian/blob/ffd0838e5f04cb40bea311a39118ebeedad210c1/lib/core/network.dart)
- [Flutter consent flow](https://github.com/Solsynth/Solian/blob/ffd0838e5f04cb40bea311a39118ebeedad210c1/lib/auth/authorize_screen.dart)

### Account/session projection now independently established

Watch [Models.swift](https://github.com/Solsynth/Solian/blob/ffd0838e5f04cb40bea311a39118ebeedad210c1/ios/SolianWatchApp/Models/Models.swift)
is authored Swift `Codable` source, not generated protocol bindings.
`SnAccount:668–730` requires string `id`, `name`, `nick`; other fields include
language/region, optional `is_superuser`, profile, subscription, badges, contacts,
and timestamps. Defaults and tolerant decoding are client choices: language and
region become empty, missing lists become empty, some invalid optional objects
are suppressed, and some missing timestamps become the client's current time.
Those choices must not be mistaken for exact server output requirements.

`SnAuthSession:1066–1125` consumes ID, label, last-granted time, IP/user agent,
location, type, created time, `is_current`, category and trusted flag. The authored
comment names types 0 login, 1 OAuth, 2 OIDC, 3 API key. It explicitly does not
decode endpoint audiences/scopes. It is a display projection, not a complete
freshness or authorization object.

The current-account Watch method decodes its body without its own explicit
2xx guard; its transport wrapper handles retry separately. This is evidence of
the caller's parser, not a reason for a new identity adapter to accept error
bodies as authenticated identities.

## 2. Observed credentials and precedence

### Flutter request construction

Both Dio providers in `core/network.dart:691–915` read the stored token through
`getValidAuthToken`. When a nonempty token is returned they set
`Authorization: Bearer ...`, overwriting the same keyed request option. If
retrieval fails or gives no token they continue without that replacement. This
is client request-header precedence, not server credential precedence.

The providers do not author a Cookie header, cookie-name selector, or cookie
precedence policy. Absence of explicit client cookie handling does not prove
the browser/platform will send no cookies or the server ignores them.

`plugins/apis/network_api.dart` removes plugin-supplied Authorization, Cookie
and Host headers before delegating Solar requests to the host's authenticated
client. These plugin permission gates are local plugin capabilities; they are
not Solar account grants.

### Web and native WebSocket requests

[Flutter WebSocket source](https://github.com/Solsynth/Solian/blob/ffd0838e5f04cb40bea311a39118ebeedad210c1/lib/core/websocket.dart)
lines 134–164 constructs `/ws?namespace=dev.solsynth.solian`. Web builds add the
access token as `tk` query; native builds send a Bearer header instead. The
namespace comes from `core/config.dart:31–45`.

Watch `NetworkService.swift:1856–1906` sends a Bearer header to `/ws` with
`deviceAlt=watch`, and no explicit namespace. Blade uses its configured default
namespace when none is given. Its committed development config's default is
`dev.solsynth.solian`. Blade-authored code does not read `deviceAlt`; any effect
through the separate token/metadata helpers remains unestablished.

### Cookie/browser and older-route limits

Watch `InAppWebView.swift` uses `ASWebAuthenticationSession`; ordinary browsing
defaults to an ephemeral cookie jar, while sign-in can request a persistent web
session. This does not reveal auth-cookie names or header/query/cookie ordering.
The retained DysonNetwork token document establishes names separately.

The pinned Solian tree contains inconsistent generations of client route code:
native OIDC loads `/stargate/auth/login/{provider}` with an optional Bearer
header, whereas `oidc.web.dart` builds `/auth/login/{provider}` and optional
`tk`. The small Android native `ApiClient.kt` still uses a different hardcoded
host/path and an older stored-token key for chat sends. These are historical
compatibility clues, not evidence that the gateway currently supports all those
aliases. No legacy route was probed or invented in the Go backend.

### Blade forwarding versus authentication

Blade's owned HTTP reverse proxy rewrites `/{service}/{path}` to `/api/{path}`
and preserves incoming query data. It does not author credential translation or
trusted account/session headers. Built-in `/ws` authentication is a separate
registered path, not proof that normal HTTP proxying authenticates the caller.

Blade README documents WebSocket extraction order as `tk`, then Authorization
(Bearer/AtField/AkField), then `AuthToken`; it also names the authority call and
IP metadata. Owned callers delegate those operations to a separate dependency.
Retain these as independently licensed documentation, not an implementation
proof of corner cases or a policy to apply to every HTTP service.

## 3. Expiry, rotation, revocation and error behavior

### Flutter

- Decodes JWT `exp` and `iat` only for scheduling. The code does not verify
  signature or read `token_use`, account version, or a session-epoch claim.
  The 30-second expiry margin and five-minute issuance interval are client
  scheduling choices, not server-validation clock skew.
- Before refreshing an expired/near-expired pair it reloads shared preferences
  so a pair rotated by another engine wins. The authored comment at
  `network.dart:929–934` states that a second rotation advances the server session
  epoch and causes outright rejection, distinct from `TOKEN_EXPIRED`.
- `test/core/token_external_rotation_test.dart` uses synthetic JWT-shaped
  strings and an out-of-band store update to check that choice. It does not
  implement or test the server's epoch key, comparison, or Redis policy.
- The automatic request retry requires HTTP 401 plus parsed
  `code == TOKEN_EXPIRED`. A deduplicated forced refresh must yield a different
  stored access token; the failed request is then retried once. On failure the
  original request error is propagated. Other 401 responses do not enter that
  interceptor's refresh path.
- `_refreshTokenPairInternal` catches transport/parsing failures and returns the
  previous pair. It does not establish which upstream failures mean irrevocable
  logout. GET transport retry excludes 401.
- `accounts/account_pod.dart` stops bootstrap retries on a remaining 401 but
  does not automatically log the user out. Manual account-screen logout fires
  the SDK revocation request without awaiting completion, then clears local
  data/token state and closes the socket. Local logout is not proof of server
  revocation success.
- A WebSocket handshake error interpreted as HTTP 401 is terminal until explicit
  reconnection/sign-in, with no automatic logout. Other disconnects use the
  reconnect scheduler. The Flutter listener does not directly consume the
  broker's `auth.session.revoked` event.

### Watch and iOS differ

- Watch `AuthorizedSession` refreshes near expiry, and retries once after **any**
  HTTP 401; it does not require `TOKEN_EXPIRED`. It rewrites only requests already
  carrying Authorization and matching its stored server host and port.
- Watch refreshes are coalesced. Missing refresh credentials or OAuth
  `invalid_grant` are terminal; transient networking/keychain failures retain
  the stored session. `AppState.swift` returns to sign-in for terminal results.
- OAuth token errors parsed at HTTP 400 include `authorization_pending`,
  `slow_down`, `access_denied`, `expired_token`, and `invalid_grant`. Device polling
  waits at least one second, increases delay by five for `slow_down`, and is
  bounded by the client polling window. This is not normal API auth-error mapping.
- iOS `DataExchange.swift` has another first-party refresh implementation with
  a 30-second margin and previous-pair fallback. Widget networking has no authored
  refresh retry of its own. Platform implementations must not be collapsed into
  a claim that every Solian caller handles 401 identically.

### Blade owned lifecycle

Blade derives expiry from its returned session object. Ordinary connections can
reauthenticate at that expiry; empty/API-key token paths suppress that monitor.
Owned code does not prove the dependency's cache freshness or JWT/session checks.

Its authored NATS JSON revocation consumer matches nonempty session ID first;
account plus device/client is fallback only when the event has no session ID.
It targets the gateway's default namespace, not all namespaces. It disconnects
matching sockets but does not itself author cache invalidation or establish that
a subsequent connection will be denied. These static lifecycle facts support
consumer tests; authority production/delivery/freshness remain separate.

## 4. What permission evidence means

Solian's OAuth metadata parser accepts `scopes` arrays or a whitespace-separated
`scope` string. Consent UI labels recognized scope names and shows unrecognized
ones literally. `*` receives a full-access warning. Watch requests `*` for its
public `solian` client. These are consent/request/display facts; none is an
authoritative allow decision for `post.subscriptions.manage`.

Authorized-app UI calls typed SDK list/update/delete methods and displays its
authored `AuthorizedApp` projection. Punishment UI likewise calls the SDK. Their
underlying exact method contracts were not expanded through Socommon.

The repository-scoped `/permissions` result leads to Passport **realm-role**
configuration UI. Its per-realm chat/post/moderation permissions are a distinct
domain, not a replacement for the global account permission evaluator. Local
plugin permissions and `isSuperuser` UI display are also not authority evidence.

No inspected eligible Solian caller exposes a global permission-decision HTTP
operation for an arbitrary authenticated account/session. Exact searches for
`/stargate/permissions`, `/.well-known/permissions`, `getPermissions`, and
`post.subscriptions.manage` returned no Solian matches. This is a bounded
research result, not proof that no supported endpoint exists. Blade's configured
well-known permission route is catalog forwarding, not per-principal evaluation.

## 5. Implementable work and remaining acceptance conditions

### Grounded now

1. A non-production HTTP client for the exact account/session operations above,
   with synthetic success/error parsing tests and a configured trusted base URL.
   Authored Swift projections remove the earlier claim that account JSON is
   wholly unseen. Keep tolerated client fields separate from mandatory server
   output claims.
2. First-party versus OAuth token-response parsers, explicit one-retry client
   policy, error-envelope parsing, and shared-store rotation tests. Do not mint
   or refresh real credentials during verification.
3. Owned Blade route rewriting/query preservation and WebSocket lifecycle/event
   projection tests, using synthetic authority results and broker messages.
4. Route inventories that record controller checks and shared-authority/global
   filter dependencies separately. These are useful progress without enabling
   unverified production authorization.

### Still partial, not declared unavailable

- The current-account endpoint is an authoritative-service lookup in client
  architecture. A general middleware replacement must still establish that all
  intended token lanes/scopes can call it, that it yields the required current
  session/restriction projection, and how its failures map to each protected
  service. A successful account read alone does not authorize a protected write.
- Account JSON, session-display JSON, and decoded JWT scheduling claims do not
  settle account-version/session-epoch checks, revoked/disabled account policy,
  Redis missing/malformed/unavailable behavior, or cache invalidation.
- Permission key spelling is already grounded in pinned DysonNetwork docs.
  The remaining need is an eligible current decision contract: supported
  operation/transport, actor/session binding, scopes and superuser/punishment
  precedence, result/error mapping and deployment trust.
- Browser and gateway credential precedence is partly documented/observed.
  Empty/conflicting/multiple credentials, legacy applicability and per-service
  cookie behavior still require current normative evidence.
- Shared pagination, soft-delete and audit hooks remain outside this client's
  implementation. Client defaults and parser tolerance cannot determine them.
- Actual selected deployment configuration, health, TLS trust and broker policy
  were not inspected. Source defaults and examples are not verified production
  configuration.

### Explicitly unresearched or permission-bounded

Socommon/SDK and the previously canceled/denied source scope remain unaccessed;
there is no license-absence conclusion. Other possible licensed clients beyond
this Solian/Blade pass were not exhaustively searched. NeTo/Spec protocol exports
remain expressly excluded, regardless of a proposed transfer format. The next
step is eligible independent contract evidence or a separately specified,
authorized adapter boundary; no source restriction should be bypassed.

## Verification

All Solian retained upstream files and root license match pinned Git blob hashes;
SHA-256/lengths are recorded. Blade's separate manifest/verification records
provide the same checks. Generated coverage summaries are audit metadata rather
than upstream source. Source tests were not run: they import dependencies beyond
this retained evidence and cannot independently validate the authority. No live
login, refresh, permission, revocation or backend request was made.
