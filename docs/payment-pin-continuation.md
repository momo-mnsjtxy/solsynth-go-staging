# Payment PIN and credential assurance

This continuation was reconstructed after the executor reset and revalidated against exact committed baseline `5e695228fab2e3c4eb956851731e09dcad15d04a`, whose 1,738 tracked files were independently hash-verified from the private source archive. Local validation and the remaining guarded PostgreSQL requirement are recorded in the delivery manifest.

The retained Solian revision `ffd0838e5f04cb40bea311a39118ebeedad210c1` creates factor type 4 with a PIN in `secret`; enable explicitly posts a JSON string to `/stargate/factors/{id}/enable`. Factor settings call list/create/disable/delete directly. Account settings opens these sheets without another authentication flow. Authored network interceptors attach ordinary bearer credentials and retry only `TOKEN_EXPIRED` through refresh. Login completes ordinary password/second-factor challenges and exchanges the one-use code for a session. Excluded SDK internals were not inspected or copied.

The existing GET/POST `/stargate/factors`, POST `/{id}/enable`, POST `/{id}/disable` and DELETE `/{id}` request shapes remain. Creation accepts type 4 plus exactly six ASCII digits in `secret`. Enable keeps its JSON-string body. Disable/delete need no body. No new field, header or endpoint is required.

An independently owned five-minute, one-use server-held assurance permits a PIN mutation only after canonical password plus required TOTP/recovery completion. Verified authentication time/methods are recorded with the login code and attached atomically to its exact new root session, account version and session Epoch. Delayed code exchange cannot change auth_time. Generic session/code issuance, delegated/OAuth sessions and provider/passwordless flows create no assurance. An in-flight pre-upgrade challenge with missing method metadata can complete ordinary login but receives no fabricated assurance. Refresh never creates or extends it and its epoch rotation invalidates the old assurance.

Missing/expired/consumed/stale assurance returns `403 REAUTHENTICATION_REQUIRED` with an ordinary-login explanation. Existing clients can sign in normally and retry the same factor request, but do not automatically open a reauthentication sheet for this error. A successful mutation advances Account.Version and revokes dependent access/refresh authorization. No new Principal is manufactured.

Residual tradeoff: theft of an eligible freshly authenticated bearer within the five-minute window permits use of that session's one remaining assurance. Removing that window would require an extra client protocol. This accepted compatibility policy measures time from actual credential completion, never session.created_at, access iat or refresh.

Canonical factors own identity/type/enabled metadata; type-4 secret is SQL NULL. The sole salt/PBKDF2-SHA256/peppered MAC remains in wallet_v1.payment_policies. Canonical mutation links the factor, current policy version and configured pepper ID, consumes assurance for the exact action/factor UUID, writes an audit and bumps account version in one transaction. Rollback restores them all while separate durable attempt budgets survive.

Disable blocks payment but retains the verifier for re-enable. Delete clears the verifier/link and retains blocked policy. Status becomes has_pin=false, validation_required=true. Re-enrollment requires new assurance. No-PIN policy is never inferred; existing explicitly provisioned no-PIN policy stays usable. PIN never becomes a login factor.

Canonical wallet transfer/order payment always uses Atomic() and repository LockPayment/CheckPayment. Preverification commits durable failure accounting and a hashed two-minute opaque authorization bound to provider instance, account/session/version/epoch, policy version, purpose and request binding. At most 32 outstanding receipts per account are admitted, with bounded cleanup. Policy/proof locks precede domain waits; final original-proof/expiry checks consume proof before commit. Rollback permits retry. Wrong purpose/binding/payer/session/provider, stale policy and replay deny.

Transfer uses its full existing fingerprint. Order binds its UUID plus exact payer selector; amount/currency/payee are immutable through exposed UpdateStatus, which accepts credentials plus status only. Strict decode and status-transition tests cover this boundary. Legacy standalone PINVerifier keeps the original error-only interface for explicit trusted composition; the canonical host always requires the Atomic verifier. Base wallet schema remains independent; canonical foreign keys/proofs are additive.

Standing mandates retain their saved payer/amount/currency/payee. Canonical maintenance locks account, policy and subscription in that order. Disabled/missing policy blocks future renewal debit and expires the due subscription without changing settled history or creating a new mandate.

Explicit migration order: authority base then authoritypg/assurance_schema.sql; sign-in base then signinpg/payment_pin_schema.sql; wallet base then walletpg/pin_schema.sql after sign-in. Authority evidence/assurance and sign-in method metadata contain no new PIN secret. Constructors never install schemas. Backend readiness checks additions.

Host PINEnrollmentPepperID must reference an existing PINPeppers entry and same-pool sign-in authority. CLI enrollment requires --enable-pin-enrollment, --enable-signin and existing --pin-pepper-id/--pin-pepper-env. No keys/peppers are generated. Missing configuration fails closed.

Commerce fixed-product hooks must remain after LockPayment and before final CheckPayment, including paid idempotent commits. Award/sponsorship fulfillment shares the same rollback boundary. Payment amounts, payees, currencies, ledger/hold settlement and idempotency remain canonical.

Thirteen guarded PostgreSQL cases cover authored bodies, secret isolation, assurance time/origin/refresh/expiry/revocation, no delegated inheritance, concurrent actions, TOTP/recovery rollback/replay, durable failures, payment bindings/policy waits/expiry/retry, immutable order economics, renewal disable and restart. The reconstructed source passed the full local Go suite, targeted race checks, vet, integration compilation and offline migration checks on Go 1.26.4. Guarded PostgreSQL/restart execution remains the parent CI requirement; no PostgreSQL was run locally. No production/live-provider action occurred.

No generic no-PIN opt-out, provider/passwordless assurance, new frontend flow, universal other-factor policy or automatic PIN recovery is introduced.
