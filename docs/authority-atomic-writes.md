# Authority checks and domain commits

The unified PostgreSQL application now uses the same SQL transaction for fresh
owned authority authorization and supported post/wallet request mutations.
Previously, the verifier read a fresh authority snapshot and then released it
before a domain repository opened its write transaction. Revocation could commit
in that interval. A successful preflight check was therefore insufficient to
order the later domain commit against revocation.

## Same-pool behavior

`application.NewPostgres(db, nil, config)` and an explicitly identical authority
pool pointer install `authoritypg.TransactionGuard` automatically. The existing
HTTP binding verifies the signed credential and removes bearer and cookie
headers. A private request context carries only the original service-bound
proof, with independently preserved account/session projections. No bearer bytes,
refresh secrets, signing key, long-lived proof map, or application mutex is added.

Each protected repository write performs these steps:

1. Begin its existing read-committed SQL transaction.
2. Require the guard's configured pool and original proof; reject an altered
   projection, another verifier's proof, or an unexpected actor.
3. Lock the canonical `owned_authority_accounts` row with `FOR UPDATE`.
4. Read fresh account state, session revocation/epoch/version/expiry, scopes,
   grants, group grants and punishments using that same transaction.
5. Acquire existing domain locks, apply the mutation and its audit/outbox writes,
   and commit once. Any failure rolls everything back.

The account row remains locked through the domain commit. When a cooperating
revocation wins that lock first, the waiting domain transaction observes its
committed state and denies. When the domain mutation wins first, revocation waits
until that mutation commits or rolls back. Separate pools/processes using the
same PostgreSQL data obey the same row lock; this is not a process-local mutex.
An unrelated account does not wait on that authority row.

`AuthorizeTransaction` keeps the same named-permission policy as the existing
authorizer: no empty permission list, no wildcard requested permission, no scope
expansion, and active denies/punishments override allows. Authentication-only
wallet routes use a separate `AuthenticateTransaction` method. Returning a safe
projection does not mint a new reusable proof.

## Protected operations and lock order

- Post create and publish: `posts.create`; update: `posts.update`; delete:
  `posts.delete`. Authority account → existing publisher/member/post locks →
  mutation and audit.
- Wallet create and set-default: fresh authentication; public ID:
  `wallets.public.id.manage`; transfer and transition:
  `wallets.transactions.manage`. Authority account → optional realm grant →
  existing owner/idempotency/transaction/wallet/pocket locks → mutation/outbox.

Account-owned wallet writes bind expected ownership to the verified actor. Realm
writes recheck the editor grant with `FOR SHARE` in the same transaction, so
ordinary updates/deletes of that grant serialize too. Existing owner equality,
balance, idempotency, decimal precision, quota, and transition checks remain.

All authority writers must follow the existing account-row protocol. This
includes session rotation/revocation, account updates, direct grants,
punishments, group membership and group grants. A group policy writer must lock
every affected account in a deterministic order before changing shared grants.
Direct SQL writers that ignore this protocol are outside the guarantee. Existing
`WithinAccount`, `ReplacePolicy`, refresh, revoke, and invalidate hooks cooperate.

## Separate authority pools

Two different `*sql.DB` values are treated as separate authority boundaries even
if their DSN happens to identify the same database. The application cannot prove
cross-database atomicity from configuration strings. With separate pools, reads
remain usable and the affected post/wallet request writes return unavailable by
default. The embedding application can explicitly choose the earlier behavior
with `Config.AllowNonAtomicAuthorityWrites = true`. That opt-in preserves fresh
preflight checks but does not serialize authority revocation with domain commit.
Use one shared pool for the atomic composition.

This change does not apply a distributed transaction protocol. The bare domain
`New(db)` constructors remain trusted storage adapters for isolated fixtures or
explicit legacy compositions; they do not promise transactional authentication.
Standalone executables that do not install this guard retain their documented
preflight-only boundary. No new migration, table, role, credential, policy seed,
or enrollment step is introduced.

## Remaining boundaries

The guarantee is the order of a successful authorization at its account lock and
the domain transaction relative to cooperating authority changes. Clock-driven
token/grant expiry is evaluated at the authorization check, not continuously
while a transaction waits for later domain locks. Request deadlines should bound
transaction duration. The guard rejects repeatable-read/serializable snapshots
instead of risking a snapshot established before a waiting grant update.

Wallet PIN/payment policy validation retains its existing separate transaction,
including durable failed-attempt accounting. This patch does not claim atomic
payment-factor policy revocation through settlement. Background `ProcessDue`
transitions are system maintenance, not request writes, and retain their bounded
domain transaction and lock protocol. Profile, publisher, board, taxonomy, chat
and other repositories are not covered by this first post/wallet composition.

## Verification

`ports/authority/transaction_test.go` verifies original-proof identity, modified
projection rejection, cross-verifier rejection, expiry/epoch/version/revocation,
disabled accounts, fresh grants/punishments, scope caps, empty-permission denial
and cancellation without a database.

`tests/integration/authority_atomic_test.go` adds
`TestPGAuthorityAtomicWrites`. Its guarded synthetic PostgreSQL subtests observe
actual database lock waits instead of assuming ordering from sleeps. Both post
and wallet cases cover mutation-before-revocation, session revoke/rotation,
disable/version, grant removal and punishment-before-mutation, unchanged
audit/outbox on denial, independent-pool revocation and unrelated-account
progress. Additional cases cover every wallet request mutator, missing/forged
proof, actor substitution, wrong pool/isolation, separate-pool reads, deny by
default and explicit non-atomic opt-in.

The test must be added to the next integration harness's `before` inventory.
It requires the existing authority, account, wallet, application and post schemas
in their current order, and has no restart fixture or `after` case. It must run
only through the marked disposable PostgreSQL runner in CI. Local compilation
or skipped database cases are not real PostgreSQL validation.
