# Owned first-party registration, login and factors

This is an independently authored implementation on the existing canonical
`owned_authority_accounts`, `owned_authority_sessions` and authority code issuer.
It does not import a canceled identity backend or reproduce its internals.

## Contracts and evidence

The pinned, licensed Solian authored callers under
`inventory/identity-client-evidence/solian` establish:

- `lib/auth/create_account_content.dart`: `POST /stargate/accounts` with
  `name`, `nick`, `email`, `password`, `language`, `captcha_token` and optional
  `affiliation_spell`. The unified host joins the configured affiliation adapter to registration atomically; see [affiliation and activation](affiliation-and-activation.md). Stores without that optional adapter continue to reject nonempty affiliation spells.
- `lib/auth/login_content.dart`: `POST /stargate/auth/challenge` with account
  and device metadata; `GET/PATCH /stargate/auth/challenge/{id}`; factor listing
  and preparation; `factor_id` and `password` verification; `step_remain` and
  `blacklist_factors`; exchanging a completed challenge's ID through the
  existing `POST /stargate/auth/token` authorization-code grant.
- `test/auth/challenge_ws_listener_test.dart` supplies an authored JSON fixture
  for `step_total`, `failed_attempts`, audiences/scopes arrays, request metadata,
  account ID and both timestamps; those fields are retained in challenge DTOs.
- The same caller and `lib/accounts/screens/me/account_settings.dart`:
  `POST /passport/accounts/recovery/password` with `account` and `captcha_token`.
- Authored `lib/accounts/screens/me/settings_auth_factors.dart`, pinned blob
  `53b3eacfe79fe4c5d04c4e2d34b54cbb9dd42a65`: password type 0, TOTP type 3,
  recovery type 5, `createdResponse.uri`, `createdResponse.recovery_code`, and
  `POST /stargate/factors/{id}/enable` with a JSON string verification code.
  Its source bytes and license are retained and hash-checked in the manifest.

Factor creation/list/delete/disable appear through authored typed-client calls,
without authored literal paths. This implementation supplies owned REST routes
`GET/POST /stargate/factors`, `DELETE /stargate/factors/{id}` and
`POST /stargate/factors/{id}/disable`; these exact routes are not claimed as
verified legacy parity. Email completion/resend (`POST /stargate/accounts/verify`
with `code`, `POST /stargate/accounts/verification` with account/CAPTCHA), password
reset completion (`PATCH /passport/accounts/recovery/password` with code/password)
and direct password change (`PUT /stargate/accounts/me/password` with
old_password/new_password) are similarly explicit owned extensions.

## Runtime and guarantees

`signin.New(store, issuer, verifier, Config)` requires the existing signing
`authority.Service`, the canonical public verifier, a dedicated operator-supplied
32-byte TOTP seal key and an issuer display name. `signinpg.New(db, canonical,
guard)` shares the same database and `authoritypg.TransactionGuard` with domain
requests. `RegisterRoutes(mux)` adds precise routes; the existing authority
handler continues token exchange, refresh, account lookup and session management.

The explicit `internal/signinpg/schema.sql` migration adds credentials, factors,
challenges and attempt budgets, all identity-bearing rows referencing the
canonical account. Constructors never run migrations. Registered accounts are
ordinary, unverified accounts with no implicit grants or superuser privileges.
The optional trusted `Config.InitialMemberPermissions` supplies a bounded list of
concrete permission keys, checked against authority’s existing grammar; wildcard
and duplicate keys are rejected. The list is copied on construction, never read
from requests, and its grants are inserted in the registration transaction. It
defaults to an empty list.
Account creation and password-factor persistence are one transaction.

Passwords use PBKDF2-HMAC-SHA256, 600,000 iterations, unique 128-bit salts and
256-bit outputs. New passwords are 12–1024 bytes. Challenge and delivery tokens
contain 256 random bits and are persisted only as hashes. Login challenges last
five minutes; verified completion publishes the existing authority's one-minute
code within the same canonical account transaction. Failure of the final
challenge write rolls back the published code as well. Token exchange stays
single-use in the existing issuer. Password reset/change increments the canonical
account version, invalidating all access, refresh and pending login codes, and
consumes or cancels pending challenges atomically. Reset preserves MFA.

TOTP uses RFC 6238 HMAC-SHA1, six digits, 30-second steps and a ±1-step window.
Seeds are AES-256-GCM encrypted, with account/factor IDs as authenticated data;
they appear only in the initial enrollment URI. Enrollment expires after
15 minutes and requires a valid code. A persisted monotonic counter rejects
reuse across challenges, restarts and confirmation/login. With TOTP enabled,
password plus TOTP or a one-use recovery secret are required, in either order.
Recovery secrets are separately generated 256-bit values, hashed at rest and
removed atomically when used. Listing never returns secrets. Factor operations
require a first-party session created within ten minutes and a fresh guard check
under the canonical account lock. Enabling, disabling or removing an enabled
factor invalidates existing sessions. The password factor cannot be removed.

Durable, independently committed account/challenge/peer attempt budgets survive
failed verification, transaction rollback and process restart. Defaults are
eight checks per challenge, twenty checks per account and sixty per peer per
ten minutes; registration is five per peer per hour, verification/reset requests
three per account reference and ten per peer per hour. Factor mutations share a
twenty-per-account, ten-minute budget. The handler uses the actual remote peer,
never untrusted forwarding headers; proxy deployments must account for their
connection topology. JSON bodies are bounded and reject unknown/duplicate keys
and trailing input; credential responses use no-store.

## External adapters and limits

Registration, email verification delivery and email recovery require injected
`Captcha.Verify` and `Delivery.Send` implementations. This slice contains no
network sender, CAPTCHA bypass, fabricated email success or OAuth identity
provider. Missing adapters return 503. A delivery failure leaves a durable,
unverified account or pending reset; verification can be resent through the
explicit resend route. Unknown-account recovery requests receive the same
accepted response without sending anything. Providers must deliver the supplied
one-use token privately and must not log it. Signing and seal keys must be
operator-managed; no runtime path generates persistent production keys.

Passkeys, device approval, PIN, external OAuth and physical-passport factors are
not implemented; unsupported factor creation is explicit. No legacy credential
import is attempted. Expired challenge and attempt-row archival is an operational
maintenance task. This slice supplies the first-party handler and adapter;
host signing-role/configuration integration and deployment remain separate.

## Verification

Pure tests cover password hashing/validation, RFC TOTP vectors, replay/window
checks, authenticated encryption binding, secret-free DTOs and strict HTTP input.
Existing authority tests also pass after the additive transaction hooks.
Six guarded PostgreSQL tests cover complete HTTP enrollment/token/current-account
flow, factor and challenge isolation, expiry, password-reset replay/session
invalidation, TOTP/recovery use, concurrent one-use operations, rollback after
code publication, durable limits, configured initial-member policy, fresh guard
rechecks after revocation, and a read-only database-restart after phase.
They compile but are skipped in this socket-restricted executor without an
approved disposable `PG_TEST_DSN`; this is not a claim of database validation.
CI must install `signinpg/schema.sql` after `authoritypg/schema.sql`, run all
`TestPGSignin*` cases before restart, and rerun only
`TestPGSigninRestartPersistence` after restart. No real accounts, third-party
messages, outside-service requests or non-ephemeral credentials were created.
