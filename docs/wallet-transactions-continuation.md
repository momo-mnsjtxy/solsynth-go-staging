# Wallet/account/pocket/transfer continuation

## Result and scope

A real PostgreSQL-backed, authenticated wallet family is implemented in
`ports/wallet/accounts`, `internal/walletpg` and `cmd/wallet-service`. It uses the
independently owned authority's public-key verifier and fresh database state.
The executable has no fixture-user header, signing key, generated key, account
bootstrap, schema application, provider call or payment-enrollment shortcut.

Implemented HTTP operations:

- GET/POST `/api/wallets`: default wallet and personal/realm creation
- GET `/api/wallets/all`, GET `/api/wallets/{id}`
- POST `/api/wallets/{id}/default`
- POST `/api/wallets/{id}/public-id/enable` and `/disable`
- POST `/api/wallets/transfer`
- GET `/api/wallets/transactions`, `/transactions/{id}`, `/transactions/pending`
- POST `/api/wallets/transactions/{id}/confirm` and `/reject`
- GET `/api/accounts/me/pin-status` and `/stargate/accounts/me/pin-status`

The licensed authored client calls `/wallet/wallets/transfer`; a combined host
may rewrite `/wallet/wallets` to `/api/wallets`. That gateway rewrite is not an
implicit trust boundary. All aliases must enter the same verified handler.
Unknown routes remain 404. A transfer containing `transfer_request_id` fails
with an explicit unavailable-adapter error; it cannot fall through to an
unrelated transfer. Funds, order/payment-provider flows, exchange, analytics,
QR request fulfillment, top-ups and arbitrary balance modification are not
implemented by this family. There is no API to mint or seed money.

## Source and client evidence

`ports/wallet/accounts/source/manifest.json` preserves 16 exact source/license
entries. The accompanying provenance and pinned-source test identify each
revision, Git blob, SHA-256 and byte length. Only licensed owned root source and
licensed authored Solian code were used. No excluded protocol schemas or
unlicensed SDK/generated code are included.

The controller source and newer service take precedence over older prose:
`all` includes accessible realm wallets; current transfers have no fee creation.
The wire projection preserves evidenced snake_case fields, nullable owners and
public IDs, numeric decimals, numeric transfer/status values, timestamp fields,
empty arrays and `X-Total` on transaction lists. Stable ID tie-breaks are added.

The authored parser test validates the implemented JSON projection without
copying an SDK. Full existing-client E2E and complete model serialization are
not established. In particular, nested payer/payee account hydration is not
fabricated, error envelopes use independently selected stable codes, and
unimplemented families do not return successful-looking empty projections.

## Independent storage and transaction policy

Apply `internal/walletpg/schema.sql` explicitly to a dedicated database; it is
also embedded as `walletpg.SchemaSQL`. The application performs no startup DDL.
All tables are in `wallet_v1`. This is a new schema, not an in-place migration
of the original `wallets`, `wallet_pockets` or `payment_transactions` tables.

- Exactly one immutable owner shape: account or realm. Account creation is
  external to this service; HTTP account identity comes from verified authority.
- Personal-wallet quota and primary selection use a shared owner advisory lock.
  Partial unique indexes enforce one active primary per account or realm.
- Public IDs use cryptographic randomness and a uniqueness backstop. Enabling
  twice keeps the same current ID; disabling sets null. Read resolution only
  returns active wallets. No current retained ID is shared between wallets.
- Currency pockets are unique per wallet/currency. Every writer first locks the
  participating wallets in UUID order. Conditional debit/hold updates verify
  sufficient available funds under that lock, preventing concurrent overdraft.
- Exact immutable base-10 arithmetic never passes through float64. Inputs are
  truncated toward zero at three places; positive requests truncating to zero
  are rejected. JSON numbers and exponent notation are parsed without rounding.
  Storage is numeric(32,3), bounded to the .NET decimal maximum magnitude.
  This explicit bound and no-overdraft constraints are new storage policy.
- Transfer state, both balance changes and outbox rows commit in one database
  transaction. Pocket creation, insufficient balance, credit overflow, SQL,
  event serialization or outbox failures roll back all ledger changes.
- Nonempty `Idempotency-Key` is account-scoped and serializes concurrent retries.
  The canonical effective source, recipient, exact amount, currency, remark and
  lifecycle flags form its fingerprint; PIN and session secrets do not. Same
  key/same effective request returns the existing transaction. A changed payload
  or changed resolved default wallet conflicts. Empty keys retain the legacy
  separate-request behavior: old clients do not gain transparent retry safety.
- `deleted_at IS NULL` is an explicitly authored visibility rule. Inactive
  wallets and pockets cannot transact or reappear via default/public-ID lookup.
  There is no deletion API or silent resurrection. Tombstoned pocket uniqueness
  blocks creating a replacement pocket with an unexplained balance. Operators
  must reconcile outstanding holds before any future archival workflow.
- Cooperating writers must follow this protocol. Mixed legacy/Go writers,
  direct admin balance edits and unreviewed legacy import are unsupported.

## Held funds and lifecycle differences

The retained legacy implementation deducts amount and adds held amount while
its documentation also defines available as amount minus held. This can count
a hold twice. The authored Solian client displays `pocket.amount` directly, so
the independent policy preserves that immediate reduction on a hold:

- `amount` = spendable funds, excluding holds
- `held_amount` = funds reserved by outstanding transfers
- `available_amount` = amount (the corrected availability projection)
- owned total for conservation = amount plus held_amount

An instant transfer subtracts payer amount and credits payee amount. A pending
or frozen transfer subtracts payer amount and increases held. Settlement reduces
held and credits payee; rejection/expiration reduces held and restores payer
amount. Constraints keep both nonnegative and bound their sum. Conservation uses
spendable plus held, without the legacy double subtraction. Legacy balances
still need explicit hold reconciliation before import, not a blind copy.

Frozen transfers do not settle early. A recipient's early confirm records
`confirmation_received_at` and keeps Frozen. At the 24-hour deadline, frozen
funds settle if confirmation is unnecessary or already received; otherwise
held funds are refunded. A non-frozen confirm-required transfer settles on
confirm. Unaccepted confirm-required funds refund at the deadline. These rules
repair the legacy implementation's ability to confirm a frozen transfer early.
Deadline equality is treated as due; legacy code used strict comparisons.

Confirm/reject retry of the same final outcome is idempotent; incompatible
terminal transitions conflict. A transaction row lock prevents double credit
or refund. The maintenance API `Repository.ProcessDue(ctx, limit)` uses
`FOR UPDATE SKIP LOCKED`, deterministic wallet locks and separately committed
bounded items. The executable runs batches of at most 100 every 15 minutes by
default; scheduling and batch timing are explicit flags. Only committed items
are counted. A failed item remains pending and emits no successful completion.

## Identity, permissions, realm access and PIN

`accounts.NewAuthorityHandler` composes the proof-bearing owned authority
verifier. The original principal is retained only in a private request context,
compared to its account/session projection, and freshly rechecked through
`AuthenticatePrincipal` for realm/list reads and `AuthorizePrincipal` for
nonempty mutation permission keys. Auth-only checks never call an empty
authorization policy. Raw bearer tokens are not retained in domain state. Arbitrary
identity headers, unsigned claims, raw JWT scopes, spoofed principal values,
cross-verifier proofs and revoked grants cannot authorize operations.

Independent permission keys are `wallets.transactions.manage` and
`wallets.public-id.manage`; these are deliberate owned policy keys, not copied
or inferred values from excluded shared constants. Authority grants and token
scopes must both permit the requested key. Authentication itself is required
for personal reads/create/default, matching the owned controller boundary.

Realm access uses fresh `wallet_v1.realm_grants` rows, with viewer/editor roles,
expiration and revocation checked against database time. Viewer can read; editor
can create, spend, manage public ID and confirm/reject a realm's received funds.
Only trusted provisioning can edit those rows. No grant-write HTTP endpoint is
present. This is a new authority model; legacy publisher selection, membership
and role import are not implemented. Missing/expired/revoked grants deny.

The `PaymentVerifier` dependency is mandatory for every transfer. The concrete
`walletpg.PINVerifier` reads a fresh, locked per-account policy:

- Missing, disabled, malformed or unsupported policy denies
- No-PIN operation requires an explicit trusted `pin_required=false` row;
  client flags and the legacy `NO_PIN_PROVEDED` string never disable verification
- Configured PINs accept 6–12 ASCII digits, use a fresh random 32-byte salt,
  PBKDF2-HMAC-SHA256 at 600,000 iterations, and a domain-separated HMAC with an
  independent external pepper of at least 32 bytes; comparison is constant-time
- Five failed attempts lock the account's payment verification for 15 minutes;
  counters and lock survive process restarts and serialize across replicas
- No PIN, bearer, pepper or derived verifier is logged or placed in an outbox
- `ProvisionPIN` is a trusted provisioning hook only. It is not exposed by HTTP,
  does not grant authority, and must be called only after separately authorized
  privileged enrollment/replacement. There is no public disable/no-PIN shortcut

The PBKDF2 work factor follows the published
[OWASP password-storage guidance](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html).
Low-entropy PINs additionally require the independent pepper and online limit;
this is not a claim that six digits provide password-strength entropy. Pepper
rotation needs explicit reprovisioning/key-version policy. Legacy Passport factor
hashes, reauthentication proofs and enrollment/import are not reconstructed.

## Outbox and external effects

Outbox rows represent queued intent only. `published_at` remains null; this code
has no publisher/ack path and makes no provider, push, notification, action-log,
websocket or other external call. Internal ledger Confirmed means the database
ledger settled, not that an external payment provider completed anything.
Transaction/pocket events and their payloads reflect the actual committed state.
A future delivery adapter needs authorized destinations, at-least-once delivery,
consumer deduplication, retry policy and a verified acknowledgement protocol.

## Runtime composition

`cmd/wallet-service` validates configuration by default without opening a socket
or database. `--serve` explicitly opts into serving. Required settings include:

- Named DSN environment variable containing a complete PostgreSQL URL
- Exact issuer, audience, public RSA PEM file and key ID
- Matching maximum access-token lifetime
- Loopback listen address behind a correctly configured TLS ingress
- Optional explicitly named base64 pepper environment and pepper ID; PIN-required
  policies deny when their pepper is unavailable

Only public verification keys are loaded. Private key/certificate PEMs, ambient
pgpass/service/client-key discovery, arbitrary identity headers, unverified TLS
modes and remote cleartext database connections are rejected. Local disposable
fixtures may explicitly use disabled database TLS. No fixture credentials or
accounts are created by the executable. Production resource grants and least-
privilege database roles remain operator-controlled prerequisites.

## Verification and CI handoff

Executed locally, without sockets or financial/provider/account operations:

- Full temporary combined-checkout `go test -race -count=1 ./...`: pass
  (actual database tests skip without the guarded DSN)
- Scoped `go vet`: pass
- Command configuration test with ephemeral synthetic RSA public/private files:
  pass; no listener or database opened
- Retained source bytes/hash and independently authored wire parser tests: pass
- The wallet command was compiled/tested in a temporary combined checkout using
  the concurrently authored authority packages; the frozen wallet checkout does
  not itself own those dependency files

Nine actual-PG tests compile but were SKIPPED locally for lack of a guarded
`PG_TEST_DSN`; no socket retry was attempted. A package-level `ok` is not a live
PostgreSQL pass. In the parent's disposable harness:

1. Load `internal/walletpg/schema.sql` once before the first test phase. The
   authority composition test applies the owned authority's idempotent schema.
2. Run all `TestPGWallet*` with `PG_TEST_PHASE=before`, mandatory DSN guards and
   no skips. Coverage includes HTTP family/ownership/soft-delete, exact amounts,
   insufficient rollback, injected outbox failure, concurrent debit/idempotency,
   quota/default races, held/confirm/refund/deadline transitions, PIN lockout,
   revoked/expired realm grants and genuine signed-authority composition.
3. Restart PostgreSQL and rerun `TestPGWalletRestartPersistence` with
   `PG_TEST_PHASE=after`. That phase only reads; it must never recreate missing
   wallets, balances, transfer history, public ID or queued event evidence.
4. The parent owns aggregate scripts, workflow manifests, publication, and full
   combined host process checks. No local production launch has occurred.

Remaining acceptance work: actual PostgreSQL/process CI results; full retained
client E2E; complete account hydration; verified legacy account/session/factor/
realm/balance import with reconciliation and rollback; provider and event
adapters; unsupported order/fund/QR/analytics families; production infrastructure,
TLS ingress and operational monitoring. These are not claimed complete.
