# Session refresh and durable admission continuity

This owned architecture correction separates access-token rotation from revocation of durable livestream, stream-chat and call admissions. It is based on the canonical implementation at exact green `5e695228fab2e3c4eb956851731e09dcad15d04a`. The licensed authored session refresh/logout contracts remain the same; no new client fields, scopes, identity type or credentials are introduced. The relevant retained contract material is `internal/auth/source-docs/SESSION_MANAGEMENT_API.md`, `UNIFIED_JWT_AUTH.md`, `AUTH_SESSION_REVOKED_EVENT.md`, and the source inventories in the initial livestream/chat and calls continuation guides. The split generation and migration are explicitly owned internals, not claims of legacy storage parity.

## Behavior and mutation audit

`authority.Session.Epoch` remains the strict JWT generation. Refresh increments it, and the old access JWT fails. Concurrent refresh and the existing bounded previous-token grace return the same rotated pair. PIN/payment and credential assurance must continue to compare this exact generation. Presence heartbeat snapshots and short-lived OAuth previews/codes also retain their existing token-generation binding.

The new internal `AuthorizationEpoch` is never serialized to clients. Ordinary first-party and bound OAuth session creation initialize both generations to one. Ordinary refresh and OAuth rotation preserve the authorization generation. Durable stream runs, stream admissions, stream-chat outbox captures and call admissions now record it in their existing `session_epoch` columns. Those capture rows are not rewritten by this migration.

The runtime session writer audit found one persistence implementation, `authoritypg.transaction.SaveSession`. Its callers are ordinary issuance/refresh/logout, OAuth issuance/rotation and `oauthpg.revokeFamily`. `PrepareSessionWrite` is a shared pure transition helper invoked by the SQL store and the authority memory test store. Scope changes, including restoration/reordering, new revocation and expiry shortening increment both generations conservatively. Ordinary expiry extension during an active-session refresh preserves the durable generation. Generation decreases, unrevocation, invalid generations and changes to established identity/parent/client/account-version/creation/category/trust bindings are rejected. Revocation callers that already increment Epoch do not double-increment it.

Password reset/change and enabled factor mutations retain their existing canonical account-version bump. Account disable/version, current permission grants, punishment, expiry/revocation and every ancestor remain checked. Livestreams additionally retain current publisher ownership/version, role, visibility, blocking and realm policy. No saved principal is manufactured, and every new HTTP/domain mutation still requires its original private verifier proof and the locking same-transaction guard. Unknown/unbound delegated sessions remain denied in this resource lane.

The exact-permission recorded eligibility method still rejects an empty permission list. Calls use the distinct `RecordedAuthenticationEligible` / `StoredAuthenticationEligible` lane because their authored join contract requires authentication and canonical room membership, without a separate call-join scope. This method only keeps/revokes an existing admission and returns a boolean. It shares current account/session/ancestor/type/scope-shape checks and cannot authorize a request. Current room/DM/realm/membership/ban/timeout gates remain mandatory.

## Migration and host composition

Apply `internal/authoritypg/continuity_schema.sql` atomically (for standalone psql installation, use `--single-transaction`) after the base authority schema and before enabling these readers/writers. Stop old session writers for this transition; it is not a mixed-version rolling migration. The migration adds `authorization_epoch`, fills only NULL values with each session's current `epoch`, makes it required, and installs a positive generation-ordering constraint. A current capture remains current; an old capture whose generation already differs stays stale. Reapplying the migration never resets a populated authorization generation. There is no default: every new trusted insertion must provide both generations explicitly.

`authoritypg.Schema` now concatenates the base schema and `ContinuitySchema`; constructors do not execute DDL. The parent host's explicit SQL installer must install the new file as well. The existing authority restart fixture's one direct synthetic INSERT now supplies its authorization generation explicitly.

The host must call `authoritypg.New(authorityDB).ContinuityReady(ctx)` from authority readiness. This read-only check requires the non-null column and validated constraint. Preserve the independently developed PIN `AssuranceSchema` embedding when merging `internal/authoritypg/repository.go`; concatenate base, assurance and continuity without dropping either migration. Preserve PIN's ExchangeCode assurance hook, original Epoch comparisons and payment adapter checks. There is no new configuration, HTTP route or scheduler hook.

The call schema adds `call_authority_reconcile`, an `(updated_at,id)` partial index for ongoing calls. Existing `ProcessDue(ctx, limit)` visits at most 100 calls in fair updated-time order, including apparently healthy calls, so shared canonical authority checks are not bypassed by an incomplete SQL prefilter. Each call has at most 100 admissions; ancestor traversal remains bounded to 64. Its return count now includes completed healthy reconciliation visits in addition to projected events. Provision cadence/capacity for the maximum active-call inventory.

Call reconciliation holds its room lock, then takes secondary canonical account locks in stable order with NOWAIT. Contention rolls the whole maintenance transition back with `calls.ErrConflict`; a later scheduled pass retries. This avoids inversion against an account-first room writer. It never holds a waiting secondary account lock. Participant reads also use the shared recorded check and fresh room policy. Healthy calls require no provider operation; actual revocation/cleanup remains durable and provider-bound. Livestream maintenance retains its existing sorted account/publisher/stream lock plan and independent cleanup obligations.

Canonical trusted mutations must use `SaveSession`; direct administrative SQL changes to scopes/generations are outside this API. There is no trigger that silently changes session generations. Existing synthetic fixtures that directly expire/revoke a row are still denied by the current-state checks. Replacing/removing account-wide permission policy is evaluated fresh; once cleanup cancels/removes an admission, a later grant does not resurrect that row. The migration never repairs a previously stale capture.

## Validation inventory

Local validation uses the restored official Go 1.26.4 and verified shared module cache. Full `go test ./...`, focused race tests and vet, and race compilation of integration tests pass. Local PostgreSQL tests skip without the guarded disposable harness; no database, socket or provider request was started by this worker.

New required before-restart cases:

- `TestPGRefreshContinuityMigrationPreservesStaleCaptures`
- `TestPGRefreshContinuityConcurrentRotationAndScopeRollback`
- `TestPGBackendRefreshContinuesBroadcastChatThenLogoutRevokes`
- `TestPGRefreshContinuityPasswordAndFactorEndBroadcast`
- `TestPGRefreshContinuityCallAdmissionAndScopeRemoval`
- `TestPGRefreshContinuityStreamScopePolicyAndAncestorRevocation`
- `TestPGRefreshContinuityCallAccountWaitRollsBack`
- `TestPGRefreshContinuityRestartPersistence`

After an actual database restart, rerun `TestPGRefreshContinuityRestartPersistence`. It records only synthetic IDs/provider binding, verifies persisted split generations, reconstructs the repository/provider, and proves the original refreshed native stream remains active.

The new composed case rotates both broadcaster and viewer through the real HTTP refresh route, rejects their old JWTs, delivers a message queued before rotation to the original admitted identity, accepts post-refresh chat/start retry and revokes on logout. Security cases use actual canonical password/TOTP services, and cover session-scope change, current grant removal, permission punishment, ancestor logout and account disable. SQL and memory transition tests cover scope restoration, rollback, concurrent/grace replay, internal serialization and invalid transitions. All HTTP fixture bodies use the canonical JSON helpers; verifier proofs and transaction guards share their issuing instance/pool. Timestamps use the fixture clocks or database clock, and SQL comparisons preserve database precision.

Existing required calls, livestream/chat, OAuth, authority, identity-guard, signin, password/factor and PIN-assurance tests must remain in the composed inventory. PIN's positive issuance and stale-on-refresh regression is owned by the PIN delivery and must not be weakened. No claim of actual PostgreSQL passage is made before the parent's disposable CI completes.

## Integrated candidate

The integration patch is based on `67162c4028c6bb26273f930baaa94748b2352d4f` and preserves its paid awards, PIN assurance and domain-collections wiring. It combines `baseSchema + AssuranceSchema + ContinuitySchema`, calls the continuity readiness check from the composed backend, installs the migration with `--single-transaction`, and extends the offline installation guard to reject non-atomic installation. The explicit CI inventory becomes 418 before / 55 after; these are required counts, not a claim of completed database execution. The workflow summary uses those counts only after guarded CI succeeds.
