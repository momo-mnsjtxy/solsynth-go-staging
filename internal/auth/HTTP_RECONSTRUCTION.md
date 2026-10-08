# Focused HTTP alternative check

Checked 2026-10-07 using only previously obtained licensed source evidence and
the existing local contract inventory. No network retrieval, live authentication
request, denied-source workaround or auth implementation was performed in this
pass.

Scope correction, 2026-10-07: the earlier protocol-export request is rescinded.
The user excluded the unlicensed protocol repository in its entirety, including
definitions, schemas, descriptors and generated bindings. User-provided exports
do not make that source eligible. Only independently derived licensed-client
contracts or separately licensed public documentation may be used.

## Result

No already-observed HTTP authority endpoint supplies both verified account
identity and the PostSubscriptionsManage authorization decision with sufficient
request/response/error/revocation semantics. This is a narrow HTTP conclusion, not a claim that no lawful authentication
contract exists: owned docs now establish a local-JWT/Redis baseline.

### Observed identity candidate

Previously read Stargate source at
01dfcdd7e633ca7abc82306684ae4b8dd37dd2f0 registers GET /api/accounts/me with
RequireAuth. Its handler obtains CurrentUser, loads the account with profile by
that user's UUID, enriches its own-account projection and responds with the
account object.

Known handler outcomes:

- Missing CurrentUser: 401 UNAUTHORIZED, message Authentication is required.
- Missing stored account: 404 PADLOCK_ACCOUNT_NOT_FOUND
- Required profile load/enrichment failure: 500 INTERNAL_ERROR
- Success: 200 hydrated account; not a documented permission-decision response

[Previously read route and handler](https://github.com/Solsynth/Stargate/blob/01dfcdd7e633ca7abc82306684ae4b8dd37dd2f0/internal/httpserver/profilectl/profile.go)

Blade's previously read generic service-prefix proxy explains the candidate
gateway form /stargate/accounts/me. Whether that prefix is enabled on a selected
deployment must be verified from its supported configuration; no deployment URL
is assumed.

[Previously read Blade routing](https://github.com/Solsynth/Blade/blob/dbded8c29f1cba55dd67086c786ac4e7432096ca/internal/proxy/proxy.go)

The route is useful evidence of an authoritative identity lookup, but its
middleware's accepted credentials, revocation/version evaluation and complete
serialized response are not specified in the obtained source. Even a successful
identity read would not prove permission to change PostWatch preferences.

### Observed token endpoint

Solian sends bearer access tokens and refreshes via POST /stargate/auth/token:
JSON grant_type=refresh_token plus refresh_token. The response fields consumed
are token, refresh_token, expires_in and refresh_expires_in. The client recognizes
401 TOKEN_EXPIRED and retries once after refresh.

[Previously read licensed client](https://github.com/Solsynth/Solian/blob/ffd0838e5f04cb40bea311a39118ebeedad210c1/lib/core/network.dart)

This is a token-rotation endpoint, not an observed access-token introspection or
permission endpoint. Calling it merely to authenticate PostWatch would mutate
session state and change the existing workflow. It is not an acceptable substitute.

### Absent contracts

- No obtained source establishes an HTTP introspection or userinfo route with
  full access-token validation semantics
- No obtained source establishes an HTTP permission-evaluation route, its input
  identity/session binding; its exact wire key is now documented as
  post.subscriptions.manage
- Owned PERMISSIONS.md establishes GET /.well-known/permissions as a public key
  catalog; it does not prove that this principal/session may perform the operation
- Blade's WebSocket upgrade authentication does not return a reusable HTTP
  permission decision and is not an introspection substitute
- OAuth/OIDC standards do not establish which optional endpoints this deployment
  implements or how native fleet tokens map to those endpoints

The updated CONTRACT.md resolves the documented JWT lanes/claims/configuration,
cookie names, exact permission key, API errors and cache structure. Remaining
gaps are current-generation consistency, precise Redis missing/outage/version
semantics and authoritative permission-evaluator inputs/results. See its pinned
owned-document links and dated reassessment.

## Smallest additional document request

Provide independently derived licensed-client contract evidence or separately
licensed public interface documentation, plus the shared rules. Do not export or
request definitions, descriptor sets, generated bindings or schemas from the
excluded unlicensed protocol repository. Production credentials are unnecessary.

### Documented HTTP authority contract, if supported

1. Exact method/path for validating a native access token and obtaining its
   current account/session identity; accepted header/cookie scheme and minimal
   success/error JSON schema
2. Exact method/path for evaluating PostSubscriptionsManage for that same
   authenticated account/session; request fields and allowed/denied/dependency-error
   response schema. The permission wire value is already post.subscriptions.manage

One documented endpoint can cover both if that is the actual supported contract.
Do not invent a new endpoint solely to fit this checklist.

### Independent licensed evidence, if HTTP documentation is unavailable

Use separately licensed client call sites or public documentation that independently
establish the actual authority operation, credential metadata, returned account/
session identity, permission-decision input and
failure behavior. The helper name AuthenticateRequest does not establish a
network RPC method or wire encoding. If eligible sources do not provide enough
information, leave the adapter absent; do not fill the gap by copying or exporting
the unlicensed protocol repository.

### Additional behavioral rules

- Does each authority call reject revoked/expired sessions, disabled/deleted
  accounts and superseded session/account versions? Identify expected outcomes
- Does permission evaluation include token scopes/session restrictions as well
  as account grants? Identify any required evaluator input
- Map invalid, expired and revoked credentials; denied permission; and unavailable
  authority to their existing HTTP or gRPC status/body semantics
- State the required server identity/transport trust and service-caller
  authentication mechanism, without supplying secret values

No local authorization cache is needed initially. If the contract explicitly
requires caching, its validity/version/invalidation rules must also be supplied.

## Smallest current clarification

The owned docs already provide the permission key and a JWT/Redis validation
baseline. The remaining request is current deployment/version behavior: confirm
which validation flow applies; Redis raw key/value/missing-state/epoch semantics;
and an independently documented permission-evaluator input/decision contract.
Synthetic expiry/revocation/denial fixtures would resolve stale examples. No real
credentials or excluded protocol exports are needed.

## Implementation decision

Keep the existing production authority adapter absent and fail closed. The
successful PostgreSQL tests establish persistence behavior, not identity or
authorization parity. Resume only when eligible independent licensed contracts
establish the necessary behavior; additional protocol-repository access or exports
are not an approved route.
