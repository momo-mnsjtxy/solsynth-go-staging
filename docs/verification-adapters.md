# Configured first-party CAPTCHA and verification mail

`internal/verification` implements `signin.Captcha` and `signin.Delivery`. These
are independently authored adapters; constructors validate configuration without
network traffic. An absent adapter still produces the sign-in service's explicit
503. Invalid adapter configuration fails construction. No provider is activated
by merely importing this package, and no production credential is generated.

## Turnstile

`NewTurnstile(TurnstileConfig, nil)` uses only
`https://challenges.cloudflare.com/turnstile/v0/siteverify`. Configure `Secret`,
exact lowercase `Hostname`, `Action`, `SiteBinding` and a 1–30 second `Timeout`.
The widget's action and cData must equal `Action` and `SiteBinding`. Its secret
must belong to that deployed widget/sitekey; Siteverify does not return a sitekey.
The shared sign-in CAPTCHA interface has no per-operation action, so enrollment,
resend and recovery use one configured identity action. cData is an additional
context check, not an authentication secret or proof of user identity.

The adapter submits a bounded proof, the directly observed IP and a random UUID
idempotency key. It rejects redirects, oversized/non-JSON/ambiguous responses,
provider errors, mismatched hostname/action/cData and stale/future challenges.
There are at most two attempts, sharing one idempotency key and deadline, only
for transport failures, 5xx or the provider's internal-error. A rejected proof is
`signin.ErrInvalid`; malformed/upstream failures are `signin.ErrCaptcha`.
Default transport verifies TLS and ignores ambient proxy settings. It never
caches successful proofs. The optional transport argument is trusted code
injection for synthetic tests, not a configuration endpoint override.

References consulted: [Cloudflare Siteverify documentation](https://developers.cloudflare.com/turnstile/get-started/server-side-validation/)
and [widget configuration](https://developers.cloudflare.com/turnstile/get-started/client-side-rendering/widget-configurations/).
No alternative provider is inferred from the licensed caller's generic
`captcha_token` contract.

## SMTP

`NewSMTP(SMTPConfig)` requires:

- `Address` (host:port), `ServerName` (certificate identity) and a 1–60 second
  `Timeout`; the timeout and cancellation cover the entire SMTP exchange
- `TLSMode` explicitly `implicit` or `starttls`; TLS 1.2 or later, certificate
  chain and hostname verification are mandatory before credentials/envelopes
- `Auth` explicitly `plain` with `Username`/`Password`, or `none` with both empty
  for an operator-approved relay; authentication never precedes verified TLS
- `From` as one ASCII mailbox and `Name` as a bounded UTF-8 display name
- Distinct `VerifyURL` and `ResetURL` HTTPS completion pages at one origin, with
  no embedded credentials, query, fragment or encoded/control-character path

System certificate roots are used normally. Trusted code may supply `RootCAs`
for an explicitly provisioned CA; the constructor copies the pool. There is no
insecure certificate switch, plaintext fallback, environment discovery or MX
lookup. Configuration and delivery reject address/header injection, multiple
recipients, invalid tokens and unsupported purposes. The two fixed text/plain
UTF-8 templates use quoted-printable MIME encoding and remain under 16 KiB.

The supplied token must be a canonical unpadded base64url encoding of 32 bytes,
as emitted by the owned sign-in service. The selected page receives it through
`url.Values` as the `code` query parameter. Verification pages must submit
`POST /stargate/accounts/verify` with JSON `code`; reset pages must collect the new
password and submit `PATCH /passport/accounts/recovery/password` with JSON `code`
and `password`. GET never consumes a token, protecting against mail-link scanners.
The pages are an explicit deployment dependency, not invented legacy routes.
They should use no-store, a no-referrer policy and no third-party content while
the bearer code is present. The service hashes tokens at rest, enforces their
30-minute expiry, cancels superseded challenges and atomically consumes them.

Nil is returned only after the relay acknowledges complete DATA acceptance.
That establishes relay acceptance, not inbox delivery. An uncertain write or
negative SMTP response returns `signin.ErrDelivery`; the adapter never retries
SMTP because it has no idempotency primitive. Message-ID is stable for a given
purpose/recipient/token but is not a deduplication guarantee. A lost QUIT reply
after DATA acceptance does not turn accepted delivery into a retryable failure.
Connection cleanup aborts incomplete DATA. No provider error, address, proof,
password or verification code is logged or included in returned errors.

Implementation references: [Go SMTP](https://pkg.go.dev/net/smtp),
[Go TLS](https://pkg.go.dev/crypto/tls#Config),
[Go quoted-printable](https://pkg.go.dev/mime/quotedprintable), and
[RFC 8314](https://www.rfc-editor.org/info/rfc8314/).
The sign-in purpose and completion API contracts are the owned contracts in
`ports/authority/signin`, documented in `docs/signin-continuation.md`.
The retained, licensed Solian callers establish enrollment/recovery inputs; no
excluded protocol definitions, generated bindings or unlicensed backend source
are used by this adapter.

## Host integration

Pass the constructed instances as `signin.Config.Captcha` and `.Delivery` in the
existing trusted sign-in configuration. A minimal command integration should
have explicit disabled/turnstile CAPTCHA selection and disabled/smtp delivery
selection; require complete provider fields only when selected, reject unused
partial configuration, and construct before the host's validation-only return.
Only the already-authorized serving path may call the adapters.

Use explicitly named secret environment variables for the Turnstile secret and
SMTP password. Accepting their variable names as flags avoids printing secret
values in command arguments. Public hostname, action, cData, sender, SMTP target,
TLS/auth mode and both completion URLs can be ordinary configuration values.
The process must not print configuration structs or secret-bearing values.
Credential provisioning, provider accounts, DNS, completion pages, service
activation and any real outbound transmission remain deployment work requiring
separate authorization; this change performs none of them.

## Verification

Socket-free unit tests use injected RoundTrippers and SMTP sessions. They cover
provider binding and proof/error parsing, stable retry idempotency, timeouts,
TLS-before-auth sequencing, refused/partial DATA, injection rejection, UTF-8 MIME
and URL/code round trips. The actual network tests require both the repository's
marked disposable PostgreSQL guard and `CI=true`, `PG_TEST_REQUIRED=1`:

- `TestPGVerificationAdaptersHTTPFlow`: loopback TLS provider + SMTP through
  HTTP registration, rejected/misbound/unavailable CAPTCHA, verification and
  password reset, token purpose isolation, and one-use completion
- `TestPGVerificationSMTPRequiredTLSAndFailure`: actual implicit TLS and STARTTLS,
  wrong server name, untrusted CA and rejected recipient
- `TestPGVerificationDeliveryFailureRemainsRecoverable`: relay rejection leaves
  an unverified account; explicit resend delivers a usable, one-use replacement

Add these to the CI before-phase required set after installing the owned sign-in
schema. All recipients and provider credentials are synthetic and end in the
reserved `example.invalid` namespace where applicable. Production provider URLs
are never contacted by tests: the CI-only injected transport checks the fixed
original target, then sends solely to its own pinned loopback TLS fixture.
No listener or real PostgreSQL instance was started in the authoring executor;
local integration-package success means compiled/skipped, not network/DB proof.
