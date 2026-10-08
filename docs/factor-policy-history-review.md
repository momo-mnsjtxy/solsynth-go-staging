# Factor setup policy history review

Frozen input: `77dc03227542b9e7016de49eaff0b878ae84966a`.
The preserved independent review tree contained an unintegrated regression for
empty-policy restoration and otherwise matched the frozen input.

## Confirmed defect

A pending TOTP enrollment stored an HMAC of surviving policy row IDs/`xmin`.
Adding a direct grant or punishment and then removing it before the next setup
request restores the original empty snapshot. The same issue applies to a group
membership or a group's grants. The older persistent-rule restoration test
retains a row whose identity changes, so it does not exercise this empty ABA
case. A fresh credential login must not rescue provenance invalidated by a
committed policy transition.

## Repair

`factor_setup_schema.sql` adds one monotonic version to each existing canonical
account and group. Row triggers retain committed changes and atomically cancel
existing setup/provenance for the affected account(s). They cover direct grants,
punishments, memberships, credentials and security-relevant factor state. Group
grant changes also advance the group's version. `SetupPolicyIdentity` hashes
these durable versions for the account and its current groups instead of live
row mutation identities. There are no new identity/grant tables or secrets.

The group version is necessary even with per-member cancellation: a grant
transaction can miss an uncommitted new membership, then commit after that
member creates a receipt. The regression holds precisely that schedule and
performs an insert/delete inside the grant transaction so that surviving grants
cannot independently explain rejection.

The version increments and cancellations share the writer's transaction. An
ordinary rollback removes both. After-row triggers skip conflict-do-nothing
inserts and unchanged updates. Forward TOTP replay counter changes are excluded;
security-state changes and counter reversal retain history. Existing canonical
writers continue to hold the account lock before dependent row writes. Group
policy changes visit affected group/account IDs in sorted order; PostgreSQL can
still reject inverted concurrent provisioning lock orders atomically for retry.

The migration is explicitly atomic in the guarded runner and its wiring guard.
Reapplying it does not reset history. A privilege that can disable triggers or
rewrite version counters is outside the application writer contract. No
TRUNCATE-based canonical provisioning path exists in this tree.

## Preserved behavior

- Recovery-first creation, pending TOTP creation and OTP activation still use one
  completed credential login and the original five-minute receipt deadline.
- Setup refresh continuity still binds AuthorizationEpoch; every incoming Bearer
  and PIN credential assurance retains its strict Epoch check.
- Setup-owned factor writes rebind their own metadata inside the same account
  transaction. Separate factor mutations, including delete/recreate, cancel it.
- Successful activation still consumes OTP/provenance and invalidates account
  authorization atomically. Recovery codes retain one-response disclosure.
- Original Solian request bodies are unchanged. No extra credential field,
  header, provider contact or fake success is introduced.

## Required regression cases

`tests/integration/factor_setup_review_test.go` adds six mandatory before-phase
cases to the guarded PostgreSQL required set:

1. Empty direct grant/punishment add/remove through `ReplacePolicy`.
2. Exact-ID grant/punishment/member/group-grant/factor restoration and credential
   verification restoration, with unaffected-account activation as a control.
3. Empty group membership and group-grant add/remove cycles.
4. In-transaction cancellation followed by rollback for all six writer classes,
   with original OTP activation still succeeding after rollback.
5. Concurrent joining membership and otherwise invisible group-grant history.
6. Conflict-do-nothing, no-op credential and forward counter writes preserving
   the original setup.

Invalidation cases require both the original receipt and a fresh canonical
credential login to reject the same old pending target. Existing end-to-end
factor, refresh, fixed deadline, PIN, recovery and restart tests remain required.

## Verification and limits

The full offline `scripts/verify.sh` suite passed: source integrity and negative
controls, required-case/schema wiring, pinned pglast 6.7 / PostgreSQL 16.1 syntax,
Go formatting, build, vet, tests and race tests. The parser covered 64 SQL files
and 12 PL/pgSQL bodies, with four malformed controls rejected. The before-phase
required count increases from 449 to 455; after-phase remains 60.

Actual PostgreSQL was not started or contacted in this review environment. The
GuardedPG tests compiled and were skipped without `PG_TEST_DSN`; package success
is not database runtime validation. Catalog/type behavior, triggers, locking,
transaction schedules and restart execution await the parent's authorized
private CI. All fixtures are synthetic and disposable. Retained licensed
sources were read only; no excluded sources or providers were retrieved.
