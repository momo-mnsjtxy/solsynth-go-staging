# Factor enrollment origin and account security review

Frozen input: `49fe8e9d50e6638f442431067eb48538299868b3`.
Scope: canonical factor setup history, account security flags, and fresh-login
activation fallback. This is an independent correctness review; no source
retrieval, production account, provider, credentials, or database socket was used.

## Reproduced session-origin defect

Ordinary pending activation already joins the account's setup receipt and checks
its originating session. `PendingEnrollment` does not need duplicate session ID
or authorization-generation columns to enforce this relationship. The receipt
stores both, and the canonical session writer preserves authorization history
through scope restoration and expiry shortening.

The explicit TOTP disable/re-enable flow had a narrower exception: a receipt in
`disabled` state bypassed the entire origin check. A fresh credential login and
correct OTP could therefore re-enable the target after its origin was revoked or
its scope/security generation changed. Disable deliberately increments the
account version, so the old origin's account version is expected to differ. Its
subsequent revocation or authorization-generation change is not expected.

The new socket-free service regression runs the actual `EnableFactor` path with
synthetic current credential assurance and canonical `PrepareSessionWrite`
generation transitions. Before the fix, only four disabled-origin cases fail:
scope restoration, shortened/restored expiry, revoked origin, and missing origin.
They succeed incorrectly with an enabled factor. Ordinary pending cases already
reject these inputs. Unchanged-origin and refresh controls succeed. After the
fix, every case passes. Missing-origin is a fail-closed defensive case; current
receipt foreign keys do not establish a canonical session-deletion path.

The repair always checks origin lookup, revocation and authorization generation,
and exempts only disabled receipt account-version mismatch. Rejected disabled
receipts are explicitly marked cancelled together with invalid pending metadata.
No frontend field, new identity authority, or new security-mode policy is added.

Canonical reachability is bounded and concrete: the session-revocation API can
revoke a stale origin by ID from a fresh current session. `WithinAccount` plus
`SaveSession` is the trusted canonical writer for scope/expiry changes; it uses
`PrepareSessionWrite` and preserves increasing authorization generations. This
review does not claim an HTTP endpoint for arbitrary scope/expiry edits.

## Account security history boundary

Canonical `SaveAccount` accepts changes to `disabled` and `is_superuser` while
requiring only nondecreasing account version. Thus a trusted canonical writer
can change and restore either flag without advancing `version`. The existing
factor policy fingerprint and durable-history triggers did not include those
flag transitions. The final account projection and enrollment fingerprint can
then equal their earlier values. No current HTTP handler that toggles these
flags was found; this is a trusted canonical-write coverage gap, not a claim of
remote account-flag mutation.

A narrowly scoped account trigger now calls the existing durable cancellation
function when either flag actually changes. It advances existing account policy
history and cancels pending/re-enable metadata in the same transaction. No-op
`SaveAccount`, nickname/language edits, and ordinary version updates do not trigger
this cancellation. Updating the history counter itself cannot recursively fire
the `UPDATE OF disabled,is_superuser` trigger. A rolled-back flag write retains
neither the change nor the cancellation. Existing setup transitions therefore
retain their own continuation behavior.

This SQL path is source-evidenced and covered by new canonical-writer regression
cases. It was not executed locally: actual trigger/catalog/rollback behavior is
reserved for authorized private PostgreSQL CI. Do not describe its new database
cases as a reproduced or passed local database result.

## Required validation

Three new before-phase cases are required by the result guard:

- `TestPGFactorSetupAccountSecurityRestoration`: both account flags, original
  pending and explicit re-enable targets, unchanged account version, fresh-login
  rejection, monotonic history, and unaffected-account activation.
- `TestPGFactorSetupAccountSecurityRollbackAndProfileWrites`: rollback, no-op and
  benign profile writes preserve history and usable enrollment.
- `TestPGFactorSetupSessionOriginFreshFallback`: normal pending and re-enable
  targets; canonical scope/expiry restoration and API revocation reject even
  after another fresh login; unchanged, refresh-generation and rollback controls
  still activate after receipt expiry.

Existing single-login recovery/TOTP chains, actual token refresh, fixed deadlines,
separate factor changes, OTP replay, PIN, rollback and restart cases remain
required. Local package success skips the PostgreSQL cases without `PG_TEST_DSN`.
