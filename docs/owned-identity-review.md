# Local identity and authorization review

Reviewed immutable base `e7fa653a33bf9970ca2e7b4623d310b929153b64`, archived into an isolated source tree. This report covers the owned first-party authentication, authority/session, app-secret, stream-ticket and related transaction boundaries. It does not cover later host/provider, commerce, resumable upload, timeline, federation or calls changes. No live service, external scan, network listener, real credential, account creation, deployment or publication was used.

## Findings and corrections

1. **Domain mutations could outlive their authorization.** The base host supplied proof-aware HTTP authorization but built account, board, chat and taxonomy adapters without joining that proof to the mutation transaction (`internal/application/backend.go:100–126`). In the base, `accountpg.write` rechecked only `disabled`, board locked only the board advisory key, chat did not acquire the canonical authority lock, and taxonomy's policy adapter only changed SQL schema names. A revocation or permission removal committed between the HTTP check and domain transaction could therefore fail to prevent the subsequent mutation. The adapters now require the original proof at their READ COMMITTED write boundary, before domain locks; the account lock remains held through commit. Explicit authentication-only routes remain authentication-only. The account/board/chat/taxonomy constructors deny unguarded runtime writes.

   Current implementation evidence: `internal/accountpg/repository.go` (`write`); `internal/boardpg/repository.go` (`WithinAccount`); `internal/chatpg/repository.go` (`beginWrite`); `internal/taxonomypg/transaction_guard.go`. Route permissions are matched to the existing HTTP/service contracts, not derived from request headers or IDs. Taxonomy additionally revalidates actor/audit identity, publisher membership and protection quota in the same transaction. Publisher locks precede the taxonomy table lock, matching post association writes; an owner changed during lock acquisition returns a conflict instead of taking locks in reverse order.

2. **App credentials did not enforce active account disable punishments.** `PushPayload` in the base checked only the recipient's `disabled` column. App ancestry authentication also omitted the publisher owner's disabled/punishment status. App operations now lock the owner account before publisher/project/app locks and check its current disabled flag and active `disable_account` punishment after lock waits. Payload writes also check the recipient's active disable punishment after acquiring its canonical account lock and again before mutation. Future/expired punishments and `block_login` retain their existing distinct meanings. See `internal/apppg/identity.go` (`payloadAccountActive`, both payload checks) and `internal/apppg/products_widgets.go` (`activeClientByID`).

3. **Account-invalidated sessions still appeared active.** The base `Sessions` result excluded explicit revocation and expiry but not a superseded account version. Password/factor changes already invalidated those credentials correctly; the list could still present them as active. `ports/authority/service.go` now filters stale account-version sessions, including the total/pagination count. `session_listing_test.go` checks that only the new session remains visible and that old access/refresh credentials stay invalid.

## Reviewed boundaries without additional corrections

- First-party signup, verification and reset use server-generated challenge secrets with purpose, account version, expiry and one-use state. Password/factor changes invalidate prior sessions and challenges under the canonical account lock. TOTP state is encrypted and account/factor bound; accepted counters are monotonic; recovery factors are consumed transactionally.
- Access verification binds signed account/session/version/epoch state to fresh storage. Refresh rotation and the bounded previous-generation grace use the canonical account lock, hashes and authenticated encrypted replay responses. Explicit revocation clears refresh material.
- Principal projections carry private verifier-bound proof and are checked for modification. The PostgreSQL guard verifies pool identity, READ COMMITTED isolation and actor equality before using the locked snapshot.
- Stream tickets are bounded, hashed, short-lived, one-use process-local capabilities. Redemption rechecks original authority proof; cookies/Authorization cannot be combined with a ticket. Tickets are not distributed across replicas and require affinity.
- App secrets remain distinct from account tokens. Their supported scopes are explicit; app identity cannot become a user principal. OAuth metadata is storage metadata, not an implemented authorization-code/OIDC provider.
- SMTP and Turnstile adapters use injected synthetic dependencies in local tests. Real provider delivery and end-to-end signup were not exercised. The reviewed host snapshot does not include the parent's newer optional provider/signin flag integration.

These observations are bounded review results, not a proof of absence of other defects.

## Integration contract

Use:

- `accountpg.NewWithAuthority(db, guard)`
- `boardpg.NewWithAuthority(db, guard, now)`
- `chatpg.NewWithAuthority(db, policy, guard)`
- `taxonomypg.NewWithAuthority(db, publicVisibility, guard)`

Bind the original proof into the request context using the same guard after identity verification and before each domain handler. `identity-host-wiring.patch` is a targeted proposal for the parent-owned combined host and standalone account/chat hosts. It also changes the explicit synthetic passport-board host to the fixture constructor. Parent host files were not overwritten; the patch is checked against the archived base and must be reconciled with current parent changes.

Direct disposable setup uses `accountpg.NewForCompatibilityTests`, `boardpg.NewForCompatibilityTests` and `chatpg.NewForCompatibilityTests`. The existing `taxonomypg.NewSyntheticRepository` remains explicit. All affected direct setup callers in the archived integration tree were updated. Later workers must update any additional direct synthetic seeds; do not weaken runtime defaults.

## Validation

Passed locally against the final isolated source:

- `go test ./...` (database cases skip without the guarded DSN).
- Focused race tests for authority/signin, authoritypg, accountpg, boardpg, chatpg, taxonomypg, apppg, application, verification and the new transaction-guard tests.
- Focused `go vet` for those adapters, authority/signin, application, verification and integration tests.
- `git apply --check identity-host-wiring.patch` against the immutable archived host.

The new socket-free SQL lifecycle driver tests verify actual mutation entrypoints: a denying guard runs once with the exact actor/permission, before any domain SQL, and the transaction rolls back without committing. Taxonomy permission tests cover admin and authentication-only contracts. They do not emulate PostgreSQL locking.

Required real-PG additions, compiled but **not run locally**:

- `TestPGDomainTransactionAuthority`: account, board, chat and taxonomy actor substitution plus revocation/grant removal across a canonical account-row lock wait.
- `TestPGTaxonomyPublisherMembershipGuard`: publisher isolation, membership revocation after lock wait, unchanged state and publisher-before-taxonomy lock ordering.
- Existing `TestPGDeveloperAppsLifecycle` now includes owner/recipient disable-punishment and timing boundaries.

Add the two new top-level cases to the required disposable integration inventory. No new restart fixture is introduced. Real PostgreSQL locking, rollback, process restart, the reconciled host wiring and new provider flows remain required before any readiness assessment. This work does not establish full platform parity or production readiness.
