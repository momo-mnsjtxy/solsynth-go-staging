# Passport administrative account reads and display badges

Base: `f9af28ef286c451ccce7a26d215f4dbf5447f374`. This bounded candidate adds seven authored routes through the composed backend. It does not make real account changes, contact providers or claim complete account-administration parity.

## Routes and ownership

On both `/api/admin/accounts` and `/passport/admin/accounts`:

- `GET /metrics/activity` returns the authored UTC day, trailing seven-day and trailing thirty-day activity/acquisition metrics, plus preceding non-overlapping windows.
- `GET /{identifier}/contacts` uses the canonical sign-in contact projection, including primary-contact references to canonical credentials.
- `GET /{identifier}/factors` returns only a summarized factor projection. Raw secrets, recovery values, counters, enrollment receipts and provisioning configuration never enter this SQL result.
- `GET /{identifier}/badges` reads the canonical progression badge rows.
- `POST /{identifier}/badges` grants a display badge with the authored type, label, caption, metadata and optional activation/expiry fields.
- `POST /{identifier}/badges/{badgeId}/activate` returns an empty HTTP 200 and switches the account's one active badge atomically.
- `DELETE /{identifier}/badges/{badgeId}` returns HTTP 204 and retains a revoked row with its provenance.

Names are exact case-insensitive canonical names; UUIDs resolve the canonical account directly. There is no fuzzy fallback to a different account. Unknown accounts return `PASSPORT_ACCOUNT_NOT_FOUND`. The existing board, verification, global-statistics and progression routes retain their owners. Other service namespaces cannot reach this Passport adapter. Responses are private, `no-store`, and vary by Authorization.

## Authority and consistency

Read routes require the current explicit `accounts.view` grant; badge writes require the current explicit `progression.badges.manage` grant. Both also require the canonical global-administrator flag and an ordinary root session (Type 0, no parent and no client ID). A Type-0 child is rejected even when its own proof is valid; this adapter does not infer authority from an unchecked ancestor. Superuser status alone, display-badge labels or metadata, forged headers, actor strings and a request's account ID confer no permission. The original opaque authority proof, current session scopes, grants, account/session validity and current time are checked within the same read-committed transaction.

An observation-only explicit-grant preflight occurs before target existence is resolved. Actor and target account rows are locked in sorted UUID order; the proof and exact target name/ID mapping are then checked again. Badge rows are locked in sorted order before a further current-authority check. The same original proof is revalidated after every potential domain wait and after the audit insert. Activation and grants also recheck target disable/punishment state and badge expiry before commit. Revocation remains available to an authorized operator for a disabled target.

Writes share canonical progression rows and the existing administrative audit transaction. Grant failure, duplicate type, expired proof, disabled target or audit failure rolls back every activation/deactivation/revocation side effect. New `origin` and `granted_by` columns distinguish earned progression badges from administrative display grants. A database constraint requires an issuer for administrative grants and forbids one for progression-origin rows. No reward ledger entry, identity grant, credential, session, account version or security status is created or changed by badge writes. No canonical credential or token is read or copied into a badge or its audit entry. Arbitrary badge metadata is caller-supplied display data.

The progression public badge list and self progression summary immediately read the same canonical active/revoked state. This candidate does not fabricate a remote profile event or a second active-badge cache.

## Source evidence and bounded differences

`ports/admin/accounts/source-evidence.json` pins the retained complete authored AccountAdminController, AccountService, Passport migration model, AppDatabase, ACCOUNT_ADMIN_API documentation, Solian badge consumer and root licenses. DysonNetwork revision is `4ee2d0cebb9da8284a92483fee6dce795bf6947c`; Solian revision is `ffd0838e5f04cb40bea311a39118ebeedad210c1`. The two older progression snapshots contain exactly one appended LF; removing only that byte reproduces the original already-pinned blob hashes. Both stored and original hashes are verified. No excluded Shared/Spec/generated protocol, Suki content, canceled source or fortune corpus was consumed.

The authored metric documentation explicitly excludes accounts without profiles, while the controller's service loops a remote account projection. This adapter follows the documented profile population and UTC windows against local canonical profile/last-seen rows. It includes disabled profiled accounts in these aggregate counts; no unspecified remote filter is imported. Future timestamps are excluded from observed activity/acquisition windows. These are current-window counts, not a historical DAU series.

The factor projection maps canonical password (0), timed-code (3) and payment-PIN (4) kinds to the authored strings. Canonical recovery kind 5 is `unspecified`, not the excluded upstream enum's passkey. Canonical PIN state lives in its own service, so the factor summary reports no factor-row secret. Unsupported config, expiry and updated-at metadata are null; no timestamp or security material is invented.

The badge migration model proves 1024-character type/label and 4096-character caption bounds. Metadata is an object bounded at 16 KiB. Unknown input fields, duplicate JSON keys, oversized/deep payloads, NUL strings and unrelated query parameters fail closed. The owned canonical one-badge-per-account/type invariant remains in force, including revoked types, and duplicates return conflict. This differs from upstream's unconstrained duplicate-type insert; automatic revival could erase provenance or revive an earned reward and is deliberately not implemented. No unseen soft-delete/global-filter behavior is imported.

Still unimplemented: account-admin list and combined detail, including their complete hydrated account/status/activity/board projections and filter/order semantics; auth-side administrative factor/contact/session mutations; provider presence scans, notification sends and other unrelated admin operations. Self-service canonical security flows and existing administrative verification/board remain separate.

## Verification contract

Eleven actual-PostgreSQL declarations are included for private CI; they are not claimed passed by offline `go test`:

1. `TestPGBackendAccountAdminSensitiveReadRoutes`
2. `TestPGAccountAdminBadgeLifecycleCanonicalProjection`
3. `TestPGAccountAdminExplicitAuthorityAndTargetIsolation`
4. `TestPGAccountAdminActivityMetricUTCWindows`
5. `TestPGAccountAdminBadgeAuditRollbackAndDuplicateRace`
6. `TestPGAccountAdminOriginalProofAfterAccountWait`
7. `TestPGAccountAdminBadgeExpiryAndProofAfterDomainWait`
8. `TestPGAccountAdminTargetRenameAfterWait`
9. `TestPGAccountAdminGrantAndRevokeRecheckDomainWait`
10. `TestPGAccountAdminRootSessionsAndSingleConnection`
11. `TestPGAccountAdminReciprocalLocksAndSessionABA`

The additional `TestPGAccountAdminSavedRestartPersistence` must run before and after the same disposable PostgreSQL cluster restarts. It saves an original one-hour synthetic proof and public verifier keys, original session row, authority projection and all badge rows/provenance including revocations. The after phase advances by real elapsed wall time, performs direct and composed HTTP reads with the original proof, and verifies unchanged saved state and audit count. It never seeds target data, issues replacement credentials, restores private signing material or resets the clock after restart. Markers are exclusive runner-local files and are not CI upload artifacts.

Wait fixtures provision grants and future punishment transitions before taking later badge locks. Account-affecting policy is never changed while the fixture holds a later domain lock. All authority and target state is disposable synthetic data. PostgreSQL execution, transaction/type/locking behavior, actual restart and exact CI group acceptance remain pending the coordinator's authorized private CI.

## Independent offline authority review

The review reproduces an original Type-0 child-session admission before the correction: valid child reads succeeded and a missing target returned account-not-found. Root-session checks now run both before target observation and on every later fresh-authority check. Socket-free SQL-boundary tests use real signed synthetic proofs and the real verifier/transaction guard to check explicit-grant denials without a target oracle, sorted account lock requests, changed target mappings, current root/global authority, revocation and session-generation ABA, final proof expiry and factor projection with a one-connection pool. These tests establish the exercised Go boundary behavior, not PostgreSQL lock, transaction, catalog or constraint semantics. The two additional PostgreSQL declarations cover all seven child-denied routes, a one-connection administrative lifecycle, reciprocal administrator locks, and canonical narrowing/restoration of session scopes. They remain unrun until authorized private PostgreSQL validation.
