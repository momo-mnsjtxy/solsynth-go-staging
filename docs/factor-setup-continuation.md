# Canonical factor assurance and bounded enrollment

This independently authored continuation preserves the retained Solian request
shapes. It consumes canonical credential assurance for sensitive factor changes
and uses one server-held continuation for the authored recovery-first TOTP setup.
There is no new header, token field, factor secret store, identity authority or
external provider contact.

## Source contract

The licensed retained
`inventory/identity-client-evidence/solian/lib/accounts/screens/me/settings_auth_factors.dart`
shows:

- factor creation through the existing factor lifecycle, type 3 TOTP and type 5
  recovery; `canAdd` requires an existing type 5 before another factor;
- type 3 creation returning `createdResponse.uri`, followed by JSON-string OTP
  activation at `POST /stargate/factors/{id}/enable`;
- type 5 enable with no request body, returning
  `createdResponse.recovery_code`, displayed once; the settings sheet offers
  enable for a disabled factor and disable for an enabled factor;
- existing delete and disable actions, with ordinary Bearer authentication.

The retained network caller retries expired access through ordinary refresh. It
has no automatic credential reauthentication handler. The retained account
settings password action uses the separate CAPTCHA/email recovery flow; that
flow retains its existing one-use proof. The authenticated old-password route
also retains its explicit old-password verification. Neither accepts a setup
receipt as password authorization. Excluded SDK internals were not used.

## Owned security contract and tradeoff

Only completed canonical password plus currently required TOTP/recovery login
creates the existing five-minute, one-use credential assurance. Generic session
issuance, delegated/OAuth/provider/passwordless authentication and refresh do
not create it. Ordinary sensitive create/enable/disable/delete uses that strict
session Epoch assurance and binds consumption to the exact factor UUID,
expected factor revision, action and current configuration in its transaction.
Missing assurance returns `REAUTHENTICATION_REQUIRED`, explaining ordinary
credential login. Existing bodies and headers remain sufficient.

The accepted residual is a fresh Bearer window: possession of the freshly
credential-authenticated root session can spend its assurance during five
minutes. This is not per-action credential reentry. The current caller displays
an error and the user signs in normally before retrying a separate sensitive
operation. Refresh never makes a session freshly authenticated.

One recovery-first setup consumes assurance once at the first step. Its receipt
is bound to the same canonical account, root session, account version and
AuthorizationEpoch. It permits only initial prerequisite recovery creation
when no TOTP factor exists, one concrete pending TOTP creation, and that target's
OTP-proven activation. A TOTP-only start remains supported for existing backend
callers and for accounts whose recovery prerequisite already exists; it consumes
fresh assurance directly. A recovery replacement while TOTP exists is a separate
sensitive action and cancels setup.

The prerequisite and pending creation do not change account/session generations:
there is no new usable login second factor until activation. The receipt's
original auth_time plus five-minute deadline never slides. Ordinary refresh can
carry this already-authorized continuation via AuthorizationEpoch; the current
Bearer still must pass strict Epoch verification on every request. PIN assurance
continues to bind strict Epoch and never inherits the setup continuation.

Successful activation atomically checks the OTP replay counter, completes the
receipt, consumes pending provenance, and then invalidates account authorization.
Existing old tokens and refresh sessions become stale. Failed OTP attempts retain
the existing separately committed 20-per-ten-minute mutation budget. Failed
transactions roll back OTP consumption, receipt progress and assurance spending.

A new completed credential login can authorize activation of a still-valid owned
pending TOTP as a separate one-use action. This neither revives an old receipt nor
extends the original fifteen-minute pending factor deadline. Revoked origin
sessions, changed authorization generations, password/factor/account changes,
and changed policy invalidate the pending enrollment itself, so fresh login
cannot rescue those targets. A separately authorized TOTP disable creates an
explicit new re-enable target at the resulting account version; enabling it
requires fresh assurance and OTP, never the old setup continuation.

The receipt cannot authorize recovery rotation, deletion, disable, PIN changes
or password changes. Recovery enable/rotation requires separate fresh assurance,
replaces the sole digest, invalidates account authorization and discloses the new
code only in the successful response. Listing and failed/replayed responses do
not disclose it; the existing successful recovery login deletes that factor.

## Transaction and storage boundaries

Apply `internal/signinpg/factor_setup_schema.sql` after canonical authority,
credential-assurance, continuity, sign-in and PIN-factor schemas. It is included
in the explicit `signinpg.Schema` migration input and disposable runner. There
is no constructor migration. This adds a private factor revision, one receipt
row per account, and one pending enrollment row per account. Canonical factors,
password hashes, TOTP sealing, recovery digests and wallet PIN remain their sole
stores. No enrollment material is present in receipt rows.

All mutations hold the canonical account lock before session/proof, receipt,
factor and payment-policy reads/writes. Original proof is rechecked before
consumption/invalidation; no replacement Principal is manufactured. Security
factor revisions change on secret, enabled or pending state changes; ordinary
TOTP counter advancement does not change the security revision. Separate
sensitive mutations cancel pending setup in the same transaction.

The policy fingerprint covers effective canonical rules/punishments, factor
UUIDs/revisions, optional current wallet policy version, trusted issuer and seal
configuration. It also includes canonical grant, punishment, group membership,
group-grant and credentials row mutation identities (`xmin`) so withdrawal and
restoration cannot restore a prior receipt's policy. These short-lived row
version witnesses are HMACed and are not a new authorization policy. A mismatch
invalidates pending metadata durably before returning denial. Canonical writers
must continue to follow the existing account-lock contract for group/grant
changes. A dump/restore or replacement of these rows may conservatively require
restarting enrollment. Normal PostgreSQL restart retains the receipt metadata.

Pre-upgrade pending TOTP factors lack the new provenance and must be deleted
with fresh assurance and enrolled again; existing enabled factors and ordinary
login remain supported. Unsupported factor types remain explicit gaps.

## Verification

Nine additional GuardedPG cases cover composed single-login HTTP enrollment,
refresh and non-renewal, fixed deadlines and fresh-login fallback, origin/session
and restored-policy invalidation, concurrent starts and activation, durable
failed-OTP budgets, rollback, bodyless/JSON-null recovery rotation, single
response disclosure, one-use recovery login, explicit disable/re-enable and
restart persistence. Existing continuity's factor scenario now creates the
pending TOTP before refresh and activates through the receipt afterward; its
broadcast invalidation assertions remain intact.

Local verification uses unit/race tests, build/vet, offline migration ordering and
GuardedPG test compilation. Real PostgreSQL and post-restart execution require
the parent's guarded disposable CI; a local skipped integration suite is not
runtime database validation. No local sockets or external credential/provider
operations were performed.
