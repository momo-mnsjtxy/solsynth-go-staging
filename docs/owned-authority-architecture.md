# Independently owned Go authority

Date: 2026-10-08. Base: `ac50657a6fa0fc16921ae44ca0ddedc46689f496`.

## Scope and status

This is a functional, independently authored account/session/permission authority
for the new Go backend. It is not an adapter that accepts unsigned claims, and
it does not preserve an unavailable legacy gRPC or Redis implementation. New
PostgreSQL tables own its identities, session state, grants and punishments.
Successful synthetic issuance/authentication/rotation/revocation paths exist.
It is not production activation, legacy credential migration, a complete
identity-provider product, or a claim of exact old authority parity.

The parent owns deployment, manifests, CI, migrations and service integration.
No existing cloud developer certificate, private key, account or credential was
read, trusted, exported or created. Tests generate ephemeral synthetic keys.
No localhost/network listener or real PostgreSQL process was started in this
restricted local executor. The PostgreSQL/process tests are written for the
parent's already-guarded disposable CI harness; local skip is not a database pass.

## Evidence boundary

Client-visible requirements are grounded in the complete retained, licensed
Solian auth subtree and authored native client/models/tests, not generated SDK
bindings. The files and pinned provenance are indexed in
`inventory/identity-client-evidence/solian/manifest.json` and explained in
`docs/identity-client-contract-audit.md`:

- Solian `ffd0838e5f04cb40bea311a39118ebeedad210c1`:
  `ios/SolianWatchApp/Services/NetworkService.swift` establishes
  `GET /stargate/accounts/me`, session listing with `offset`, `take`,
  `includeChildren`, and current/other/ID deletion.
- Authored `ios/SolianWatchApp/Models/Models.swift` establishes consumed
  `SnAccount` and `SnAuthSession` fields. The minimal account response here
  includes `id`, `name`, `nick`, language/region, `is_superuser`, and creation
  time. Session responses include the consumed ID/label/type/timestamps,
  `is_current`, category and trusted fields. Optional profile/contact/badge/
  location/IP/user-agent data is not fabricated. Full legacy hydration is a gap.
- `lib/core/network.dart` and `ios/Solian/Services/DataExchange.swift` describe
  first-party JSON token refresh with `token`, `refresh_token`, `expires_in`,
  `refresh_expires_in`. `lib/auth/login_content.dart` exchanges a completed
  authorization code at the same path. Only an already-trusted login-completion
  hook can create such a code in this implementation.
- `ios/SolianWatchApp/Services/StandaloneAuthService.swift` uses a separate
  OAuth form lane and `access_token` naming. This implementation supports
  refresh of explicitly provisioned OAuth sessions, bound to their `client_id`.
  It does not claim device-code authorization, consent or provider completion.
- `test/core/token_external_rotation_test.dart` and the surrounding networking
  implementation distinguish an external session-epoch change from actual
  expiry. Synthetic JWT-shaped client test values are not server token proof.
  `test/core/auth_callback_token_test.dart` checks callback metadata; that is not
  an issuer/signature contract.

Retained DysonNetwork-owned narrative documents at
`4ee2d0cebb9da8284a92483fee6dce795bf6947c`, under `internal/auth/source-docs/`,
provide RS256/user-lane claims, account versions, permission/scope/group/
punishment concepts, token endpoint separation and shared error fields. These
are source-informed facts, with the migration-era drift already recorded in
`docs/identity-resolution-audit.md`; their old Redis/gRPC mechanisms are not
reconstructed. The earlier retained documentation summary mentions immediately
previous refresh-token grace of 60 seconds. It does not prove the precise old
server algorithm.

`docs/public-api-documentation-audit.md` independently records published factual
findings from the official permission, OAuth and login guides. Published
`/padlock` examples are not treated as proven `/stargate` route aliases.
No Suki repository Markdown, NeTo/Spec schemas/descriptors/generated bindings,
Socommon, SoSYS, canceled Stargate retrieval, or alternate mirrors were used.
No production endpoint was called. Existing audit documents remain historical
evidence; this approved new authority intentionally supersedes their earlier
"wait for exact legacy adapter semantics" implementation direction only for
this owned subsystem.

## Concrete Go APIs

Package `ports/authority`:

- `New(Store, Config) (*Service, error)` constructs the issuer. Config requires
  an explicitly trusted RSA signing key/key ID, issuer, audience, independent
  refresh-hash and replay-seal keys. It discovers no keys.
- `NewVerifier(ReadStore, Config) (*Verifier, error)` retains only trusted public
  keys, issuer/audience, clock and maximum access lifetime. Domain services do
  not receive signing/sealing/refresh secrets or issuance/write methods.
- Both expose `Authenticate(ctx, token)`, `Authorize(ctx, token, keys...)`,
  `AuthenticateRequest`, `AuthorizeRequest`, and
  `AuthorizePrincipal(ctx, principal, keys...)`.
- `Principal` contains Account and Session projections and an unexported proof
  binding the original projection, signed claims and exact verifier instance.
  Forged, changed or cross-verifier projections are rejected. Independent scope
  copies prevent caller mutation from changing the proof. Reauthorization
  reconstructs the bound identity and reloads account/session/policy state.
  Store a Principal unchanged in a private request-context key; project domain
  IDs separately. Raw bearer credentials need not be retained in adapters/logs.
- `Service.Handler()` serves the evidenced first-party routes and the bounded
  OAuth refresh lane. `BearerToken` accepts exactly one Bearer header.
- Trusted in-process hooks: `IssueSession`, `CreateLoginCode`,
  `ExchangeCode`, `InvalidateAccount`. They are not public bypass endpoints.
  The first-party public code exchange still requires an unexpired, one-use
  code created by the trusted hook. An arbitrary account ID cannot obtain one.
- Session operations: `Sessions`, `Revoke`; refresh: `Refresh`.

Package `internal/authoritypg`:

- `New(*sql.DB)` is operational, owns no connection lifecycle and never migrates
  automatically. `Schema` is an explicit new-schema migration artifact.
- `WithinAccount` takes an account-row `FOR UPDATE` lock in a real SQL
  transaction. Refresh, issuance, code consumption, revocation, version changes
  and `ReplacePolicy` commit atomically; callback failure rolls back.
- `ReadAccount` provides a read-only repeatable-read snapshot for `Verifier`.
  This supports a database role with SELECT-only grants and does not lock rows
  for update. Write methods reject calls within this snapshot.
- `LookupAccount(ctx, idOrName)` resolves an exact canonical UUID or case-sensitive
  name for domain lookups. This creates no authentication proof or permission
  and returns no factors/credentials. Disabled status remains visible to callers.
- `ReplacePolicy` is a trusted, account-locked provisioning hook replacing direct
  grants and punishments. There is no public grant-escalation API.
- `SeedSynthetic` inserts only an explicit `synthetic_` fixture name. It is a
  fixture helper, not an account signup or production provisioning API. Its
  caller must first establish a disposable database, as integration `database(t)`
  does by validating the root marker and exact socket DSN.

## New security semantics, explicitly specified

### JWTs and identity

Only strict RS256 JWTs in the Bearer user lane are accepted. `alg`, `typ=JWT`
and a trusted exact `kid` are mandatory. No token-selected URL/JWKS, `none`,
HMAC key confusion, Bot/API-key, refresh-token-as-access-token, legacy header,
query `tk`, auth cookie or proxy identity header is accepted. Cookie/query
support is a deliberate unimplemented compatibility surface, not evidence that
all existing clients avoid them.

Required claims: `iss`, `aud` (single configured string), account UUID `sub`,
matching session UUIDs `jti`/`sid`, `token_use=user`, positive `ver`, positive
owned `epoch`, integer `iat`, `nbf`, `exp`. Duplicate/unknown JSON keys,
noncanonical base64url, future issuance/not-before, wrong issuer/audience,
unknown key, invalid ordering and excessive lifetime are rejected. There is
no implicit clock skew. Default access lifetime and verifier maximum is 10
minutes; configurable whole-second lifetime is 1 second through 1 hour. Align
verifier configuration with the issuer. `epoch` and the exact equality/version
rules below are the owned authority's new contract, not claimed legacy keys.

All accepted tokens are cryptographically verified and joined to their stored
account/session. Missing records and dependency failure cannot become anonymous
success. A disabled account or active disable punishment denies access. Account
version and the session's bound account version must equal token `ver`; session
epoch must equal signed `epoch`; explicit revocation wins over token expiry.
No grant, superuser status or scope claim in a JWT bypasses stored authority.

An authorization decision is current at its database transaction/snapshot.
Revocation cannot retroactively cancel a domain mutation already authorized;
a cross-database/domain operation must define its own transaction boundary.
There is no authentication-result cache or Redis dependency.

### Refresh and revocation

Refresh tokens are opaque 256-bit random values. Only HMAC-SHA-256 digests are
searchable in the database; the HMAC key is separate from the AES-256-GCM replay
key. Raw refresh tokens are never logged or stored as plaintext. The immediately
previous successful response is authenticated-encrypted with the session ID as
associated data. It is retained only as the current row's one-generation replay
record, replaced on the next rotation and cleared on explicit revocation.

A current refresh, under the account lock, increments epoch once, rotates the
refresh secret and extends session expiry (default 30 days). Ordinary account/
permission reads never extend sessions. An immediately previous token submitted
within the configured grace (0 through 60 seconds) returns the same current
access/refresh token values, with remaining lifetime fields recalculated; it
does not perform a second rotation. Access-token validity is not extended by
replay. Grace is half-open: `now < previous_until`. If the replayed access token
has already expired, no expired pair is returned. A zero grace disables replay.
Earlier generations and previous tokens outside grace are invalid refreshes.
This replay policy is newly specified to handle concurrent clients without
inventing unsupported legacy equality/epoch behavior. Possession of the
previous secret during grace is intentionally still a limited bearer capability.

Session revocation increments epoch, records revocation and clears both refresh
digests plus the sealed response. Account invalidation monotonically increases
version and invalidates access, refresh and outstanding login codes. Session
save operations cannot lower epoch, change account/version/type/client binding
or clear an existing revocation. All revocations are account-scoped; selected
session descendants are recursively included. Child-session creation itself is
not exposed yet. Login codes are hashed, expire in one minute, bind the account
version, and are consumed atomically with session creation.

No password/PIN, private signing key or provider credential is imported, migrated
or discovered. Production signing/sealing key storage/rotation, refresh-hash
key rollover, rate limiting/abuse controls and key-provider integration remain
operational work requiring a separately approved deployment design. Expired
sealed records are not decrypted after grace but remain encrypted in the row
until replaced/revoked; a retention cleanup job is not claimed implemented.

### Permission policy

Exact keys and dot-boundary `prefix.*` are supported, with `[starts_at,
expires_at)` activation. Direct and group rules are read from PostgreSQL.
No automatically seeded all-permissions/default group exists. Every requested
permission must pass:

1. Verified active account/session and current versions/epoch.
2. At least one persisted session scope matches, including for superusers.
3. No active permission-modification punishment matches.
4. No active explicit deny rule matches.
5. At least one active allow rule matches, or the stored account is a superuser.

`*` scope removes scope restriction; it never independently grants account
permissions. Denials and punishments outrank superuser grants. This is an
explicit conservative owned policy where source prose is ambiguous or describes
different bypasses. `block_login` denies new issuance and refresh; a currently
valid access session can remain usable until expiry unless disabled/revoked.
Self account/session management requires a valid interactive first-party
session for session actions. It does not require a separate global grant;
OAuth sessions cannot list/revoke interactive sessions. Account lookup supports
both implemented user-session types. Realm roles, payment PIN/reauthentication
and product-specific checks are separate required domain policies.

All policy writers must use account serialization. Group grant changes affecting
multiple accounts must lock affected accounts in deterministic ID order if used
alongside issuer-side read-committed decisions. There is no public group-admin
write API; verifier read-only snapshots remain internally consistent.

### HTTP errors and response lanes

First-party errors use the owned `code`, `message`, `status` envelope:

- `UNAUTHORIZED` / 401: invalid credentials, unknown/missing account/session.
- `TOKEN_EXPIRED` / 401: actually expired access credentials after signature,
  binding, disabled/revoked/version checks. It is never used for stale/revoked
  sessions or unavailable authority. This preserves Flutter's expiry-only retry.
- `TOKEN_STALE` / 401: account-version or session-epoch supersession.
- `SESSION_REVOKED` / 401: explicit revocation.
- `INVALID_REFRESH_TOKEN` / 401: missing/invalid/expired refresh secret.
- `ACCOUNT_DISABLED` / 423, `FORBIDDEN` / 403,
  `AUTHORITY_UNAVAILABLE` / 503, and `SESSION_NOT_FOUND` / 404 for an authenticated
  account's absent/cross-account revoke target.

These non-expiry code spellings are explicit new semantics; eligible client
sources establish non-expiry distinction, not every legacy string. OAuth refresh
uses OAuth `invalid_grant` / 400 for invalid/stale/revoked/lane/client mismatch,
`temporarily_unavailable` / 503 for dependency errors, and `access_token` output.
The first-party endpoint uses JSON and `token` output. Supported token responses
include both duration fields and are marked `Cache-Control: no-store` and
`Pragma: no-cache`. OAuth device grant and other unsupported grants fail clearly;
there is no invented successful challenge/provider flow.

## Initialization, execution and verification

The binary `cmd/authority` only validates injected configuration by default; it
does not parse the database driver configuration, connect, migrate, seed, mint or
listen. `--serve` explicitly enables DB connection and the HTTP listener. Inputs
are explicit issuer/audience/key ID, injected PEM signing/public keys, independent
raw hash/seal-key files and an explicit PostgreSQL URL. Certificates are not
accepted as JWT public-key files. Database URLs suppress ambient pgpass/service/
client-certificate credential loading and default to verify-full TLS. `sslmode=disable`
is supported for disposable local fixtures, never a production recommendation.
HTTP serving assumes a separately configured trusted TLS ingress. No server was
started or activated in this work.

Socket-free local verification:

- `go test -race ./ports/authority ./internal/authoritypg ./cmd/authority ./tests/integration`
  passes; database-specific tests explicitly skip without `PG_TEST_DSN`.
- Unit/recorder coverage: strict signing/key/claim validation; actual expiry,
  stale epochs and revocation; cross-account management; concurrent rotation,
  grace boundary, encrypted replay, rollback; code replay/version invalidation;
  scopes/grants/punishments/superuser ordering; principal mutation/forgery;
  public-key-only verification; first-party/OAuth response fields; query/cookie/
  spoofed-header rejection; offline command config and certificate rejection.

Real disposable PostgreSQL/process acceptance is `TestPGAuthorityLifecycle`:

- `isolation_and_policy`
- `concurrent_refresh_and_restart` (12 concurrent rotations, second connection
  pool, encrypted-at-rest checks and `TestPGAuthorityProcessHelper` subprocess)
- `transactions_code_and_global_invalidation`
- `public_key_verifier_read_only_snapshot`
- `refresh_racing_revoke`

It applies `authoritypg.Schema` only after the existing `database(t)` disposable
marker/DSN checks, seeds two random-ID `synthetic_authority_...` accounts, and
uses ephemeral keys in a test temp directory. `PG_TEST_REQUIRED=1` makes missing
DB configuration fail rather than skip. Actual PostgreSQL/process results must
come from the parent CI run, not be inferred from local compilation.

## Remaining gaps, not completion claims

Legacy account/password/session import; MFA/factors/providers; consent/device
flows; API keys/Bot/OIDC lanes; cookies/CSRF and legacy/query credential
compatibility; full hydrated account/device metadata; account signup and
production provisioning; remote authenticated authority transport; legacy
Redis/gRPC compatibility; durable revocation events/WebSocket disconnects;
rate limiting and durable security audit events; key lifecycle/retention;
production migration/deployment and real frontend end-to-end verification.
The trusted login hooks enable fixtures and future correctly authenticated
integrations, not a public passwordless account-ID shortcut.
