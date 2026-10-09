# Explicitly configured internal treasury exchange

This owned, opt-in runtime corrects the original source's unbacked null-counterparty
exchange. It performs two funded canonical wallet transfers in one transaction.
It creates no treasury/account, starting balance, provider payment, withdrawal,
external transfer, default price, fee or implicit reciprocal rate.

## Licensed source and compatibility

Original AGPL-3.0-only WalletController.cs at DysonNetwork revision
4ee2d0cebb9da8284a92483fee6dce795bf6947c, blob
10650f1a12912532d70f6ea422a69557a508a224, supplies the exchange route, request,
response, configured source/target operands and truncation toward zero to three
decimal places. Full authored PaymentService.cs blob
1d599181db3e6482b21e1e86463739d3cf2dadd1 confirms null counterparties can create
or remove value. Retained WalletService and owned EF migrations establish
wallet ownership and precision. The authored Solian exchange sheet, revision
ffd0838e5f04cb40bea311a39118ebeedad210c1, submits amount, currency and walletId.
All retained files/licenses/hashes are in ports/wallet/accounts/source/manifest.json.
No external SDK, Shared model/protocol, canceled mirror or unlicensed source is
used. No boosts price/treasury policy is reused or inferred.

POST /api/wallets/exchange and /wallet/wallets/exchange preserve amount,
currency, wallet_id and remark fields and the paired debit_transaction /
credit_transaction response fields. The owned mutation additionally REQUIRES
Idempotency-Key and supports pin_code. The retained original exchange caller
supplies neither: it is not compatible without a client update. The retained
authored network.dart blob ee0704898668aaaa9294ebd1f71d47ce48059729 wires that
SDK wrapper to apiClientProvider. Its request interceptors set Authorization and
User-Agent, its generic retries are GET-only, and token-expiry retry preserves
RequestOptions with an internal marker. It establishes no durable logical
exchange/request identifier. No random server key or transfer-request identifier
is substituted. Omitted PIN is
accepted only when the canonical current payment policy explicitly permits it.
No omission, legacy sentinel or source omission bypasses payment authorization.

Each returned leg has the existing Transfer=0 type because it is a real transfer
between two non-null canonical wallets. The unlicensed Shared System numeric
value is not guessed. Managed exchange legs cannot be changed by ordinary
transaction confirmation/rejection or lifecycle maintenance. No automatic
reversal/refund endpoint is added.

## Opt-in policy and activation

The embedding host accepts application.Config.WalletExchange. cmd/backend
requires both --enable-wallet-exchange and an absolute regular file supplied
with --wallet-exchange-config. Missing policy keeps mutation disabled. An
unconfigured host can continue exposing the original independent read-only
exchange catalog. When exchange is configured, the anonymous GET catalog uses
that exact immutable rate snapshot; it cannot show unrelated catalog prices.
The catalog is a configuration snapshot, not a liquidity or active-policy quote.

The JSON file contains exactly version, treasury_account_id, treasury_wallet_id,
and rates. Each rate contains source_currency, source_amount, target_currency,
and target_amount. Operands are positive exact DECIMAL STRINGS, never binary
floating point; up to 28 fractional places, value at most the canonical decimal
maximum. At most 32 explicit one-way rates are allowed. Currency identifiers are
ASCII letter-led tokens of at most 64 characters. A case-insensitive source
currency has exactly one target. Source and target must differ. The version is
an explicit ASCII identifier of at most 64 characters. Unknown/duplicate fields,
ambiguous duplicate sources, symlinks and malformed files fail validation.

Validation does not connect to a database. In dry-run mode the CLI prints only
the policy version and SHA-256 commitment. The commitment is over the canonical
ExchangeConfig JSON after removing insignificant decimal zeroes and sorting
rates by source currency; it includes treasury ownership and all rate terms.
The library exposes the same value through NewExchangePolicy(config).Digest().

An operator must separately apply exchange_schema.sql after the funds/PIN
schemas, provision an existing eligible treasury wallet/account and target funds,
insert the explicit version/hash into wallet_v1.exchange_policy_versions, and
point wallet_v1.exchange_active_policy at that version with enabled=true.
Runtime never performs those actions. The version/hash row is immutable.
Changing terms requires a new version and deliberate active-pointer change.
An absent active row, disabled policy, wrong hash, stale version/replica, disabled
account, active canonical disable-account punishment, deleted wallet or wrong
treasury ownership fails closed. Readiness
checks policy/treasury eligibility without changing state. The treasury owner
cannot also be the requesting exchange customer.

## Exact economic and replay contract

For positive input, first truncate the input to canonical mills. Multiply its
integer mills by the exact target operand divided by the exact source operand.
Truncate the result toward zero to whole target mills. Require at least one
target mill and no canonical amount overflow. Source funds move customer to
treasury; target funds move treasury to customer. Both legs require sufficient
available funds, preserve held funds, and conserve each currency independently.

The fractional target mill that cannot be represented is retained by treasury;
no fractional ledger value is minted, moved to a third currency, or called a fee.
The immutable quote and response store remainder_mills as an exact reduced
rational fraction (for example, 2/3 means two thirds of 0.001 target units).
The receipt also retains the exact source/target rate operands and policy hash.

Original proof/current account, session, scope, wallet owner or realm ownership,
PIN/payment-policy version and bounded payment proof are checked in the same
transaction as both legs. Account locks are sorted; realm/policy/ledger locks
follow. Active-policy and wallet lifecycle locks are held to commit. Payment
proof is rechecked and consumed only with a successful atomic commit. Errors
roll back both balances, legs, receipt, events and proof consumption.
Treasury eligibility is checked again after all later waits, including scheduled
punishments that become active while the transaction waits. Leg timestamps use
PostgreSQL microsecond precision before the first response, so persisted replay
and restart responses retain the exact original timestamps.

Idempotency keys are mandatory 1..128 visible ASCII characters, scoped to the
actor. The binding includes actor, wallet, exact quote/version/hash and remark,
never raw PIN/session credentials. Same-key/same-terms replay returns the same
persisted legs and still requires current authority and a fresh payment proof.
Changed terms with the same key conflict; stale policy fails closed. An operator
rate change does not authorize a client to silently retry with a new key.

## Verification boundary

All new test accounts, balances, policies, peppers and failures are synthetic
and disposable. Named guarded PostgreSQL cases cover composed PIN/scope/replay,
per-currency conservation, treasury liquidity and disablement, receipt-failure
rollback, canonical transfer contention, concurrent idempotency, active-policy
lock-wait disablement, elapsed payment-proof and realm-grant expiry, same-version rate drift,
and original-session/public-key/elapsed-clock restart replay. The before phase
creates the restart target and active policy; the after phase seeds no account,
policy, balance, replacement session or signing key. A fresh bounded payment
authorization is issued from the original still-valid session for replay.

Ordinary exchange fixtures restore the exact previously active version/enabled
tuple during their before-phase cleanup, retaining every immutable policy-history
row. The restart fixture alone persists its active policy. Its before phase
explicitly creates and cleans up another temporary fixture after saving the
restart marker to check that file order and group redistribution cannot invalidate
the marker. Fixture cleanup is synchronous before that test returns; the after
phase performs no policy installation or restoration. These singleton fixtures
are sequential within each isolated CI database and must not use t.Parallel.

Offline compilation, race unit tests, SQL argument checks and PostgreSQL-16
grammar parsing do not execute PostgreSQL or prove database behavior. The
coordinator must run the named actual-PG and restart cases before acceptance.
No local socket, provider request or real financial action was performed.
