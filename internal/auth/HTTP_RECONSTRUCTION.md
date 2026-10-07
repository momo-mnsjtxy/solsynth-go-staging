# Focused HTTP alternative check

Checked 2026-10-07 using only previously obtained licensed source evidence and
the existing local contract inventory. No network retrieval, live authentication
request, denied-source workaround or auth implementation was performed in this
pass.

## Result

No already-observed HTTP authority endpoint supplies both verified account
identity and the PostSubscriptionsManage authorization decision with sufficient
request/response/error/revocation semantics. Missing gRPC descriptors cannot
currently be avoided by switching to a verified HTTP equivalent.

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
  identity/session binding, or PostSubscriptionsManage's exact wire key
- A permissions catalog, even if one exists, would not itself prove that this
  principal/session is allowed to perform the operation
- Blade's WebSocket upgrade authentication does not return a reusable HTTP
  permission decision and is not an introspection substitute
- OAuth/OIDC standards do not establish which optional endpoints this deployment
  implements or how native fleet tokens map to those endpoints

The earlier contract report remains authoritative about unresolved issuer,
audience, key discovery, cookies/API keys, permission/error and revocation rules.

## Smallest additional document request

Provide either option A or option B, plus the shared rules. Interface documents
and synthetic fixtures suffice; implementation source and production credentials
are unnecessary.

### A. HTTP authority contract, if supported

1. Exact method/path for validating a native access token and obtaining its
   current account/session identity; accepted header/cookie scheme and minimal
   success/error JSON schema
2. Exact method/path for evaluating PostSubscriptionsManage for that same
   authenticated account/session; its exact permission wire value, request
   fields and allowed/denied/dependency-error response schema

One documented endpoint can cover both if that is the actual supported contract.
Do not invent a new endpoint solely to fit this checklist.

### B. Existing gRPC authority contract

Provide only the original .proto declarations or a FileDescriptorSet (including
imports) for:

- The RPC actually invoked by the shared token authenticator. Its helper name
  AuthenticateRequest does not establish the RPC's service/method name
- Its credential/request-metadata message and authentication response message,
  including the referenced account/session identity and status/expiry enums
- The RPC actually invoked by the permission middleware, with request identity/
  session/token-scope binding, permission field, response decision and enums
- The exact value of PermissionKeys.PostSubscriptionsManage

Descriptors must include package and fully-qualified service/method names,
field numbers/types, oneof/optional presence and referenced types. Redacted
synthetic request/response/error examples should identify semantic field meaning;
descriptors alone do not explain revocation or authorization policy.

### Shared rules needed for either option

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

## Short question for the user

能提供认证/权限接口的文档或 .proto 吗？只需要“校验访问令牌并返回当前账号/会话”和“检查 PostSubscriptionsManage”的请求、响应、错误定义，以及这个权限的准确字符串值；如果已有 HTTP 接口，给对应方法、路径和 JSON 示例也可以。还需要确认撤销/过期会话和令牌权限范围如何影响结果。不需要真实令牌、密码或生产数据。

## Implementation decision

Keep the existing production authority adapter absent and fail closed. The
successful PostgreSQL tests establish persistence behavior, not identity or
authorization parity. Resume the adapter once one of the two actual authority
contracts is available; no additional full-repository access is necessary.
