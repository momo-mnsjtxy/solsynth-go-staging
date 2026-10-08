# Orders, subscriptions and gifts

## Runtime integration

Construct `commercepg.New(db, guard, commercepg.AppAdapters(apppg.New(db)))`.
The guard must be the same pool identity as `db`, with a real proof bound by
`TransactionGuard.Bind`. Nil, unbound, mismatched-pool, revoked and expired proofs
fail closed for account operations. Compose its `commerce.Service` with the
host's verified `Identity` and `Authorizer`, canonical `walletpg.PINVerifier`,
and optional named provider adapters. Mount `/api/orders` and
`/api/subscriptions`; the existing gateway adds `/wallet/...` aliases.

Apply schema explicitly after account authority, canonical accounts/publishers,
canonical apps/products and wallet tables. Runtime constructors perform no DDL,
account creation, credentials provisioning or external network calls.
`commercepg.SchemaSQL` owns orders, non-money product reservations, subscription
and gift lifecycle records, provider receipt deduplication and outbox. It does
not contain a balance or parallel money ledger. Operators provision catalog
`definitions` and validated app-to-payout `merchants` bindings; there is no public
catalog/merchant provisioning API. Payout binding is checked against the app's
canonical publisher account/realm owner at order creation.

The only canonical wallet changes are `purpose`/`managed_by` columns and the
transaction type projection, plus excluding managed rows from ordinary wallet
confirm/reject/expiry. Managed holds may have no wallet expiry because commerce
owns their lifecycle. `internal/walletpg/commerce.go` adds transaction-scoped
hold/release/refund primitives using existing deterministic wallet locks,
conditional exact numeric updates, wallet transaction rows and wallet outbox.
There is no standalone public settlement method.

## Implemented routes

- Orders: app credential create, public sanitized checkout with canonical public
  app/developer display fields (and built-in system-app enrichment), account `mine` list/
  detail, PIN-verified pay, app status finish/cancel, app metrics. Credentials
  require the matching canonical `orders.create`, `orders.status.manage`, or
  `orders.metrics.read` declaration and token scope.
- Subscriptions: public catalog/groups; account list/detail/group/fuzzy/active
  reads; create unpaid subscription, create/reuse payable order, atomic payment
  activation, cancel, bounded durable internal renewal. The authored client
  omitted `payment_method` on one wallet purchase path, so omitted subscription
  method defaults to `solian.wallet`. Other methods are explicit.
- Gifts: purchase unpaid record, create/reuse order, pay into canonical hold,
  sent/received/detail, check code, send only after payment, redeem atomically,
  cancel with held refund, bounded expiry with refund. Owner and intended
  recipient checks apply. The same redeemer's replay returns the existing result;
  any other concurrent redemption fails. A direct recipient redeems explicitly.
- Providers: exact subscription checkout, restore and webhook route families
  for `afdian`, `paddle`, `apple`. Each configured adapter must verify signatures,
  merchant/app audience, product mapping, payment completion, identity, replay
  semantics, environment and timestamps before yielding `VerifiedPurchase`.
  Restore additionally enforces the authenticated account. Verified receipts
  deduplicate durably and create subscription duration only; no wallet minting.
  No provider clients, live API calls or credentials are included. Missing
  providers return 503, and invalid verified identity/signatures do not apply.

## State and money rules

An unpaid order snapshots server-side product prices and its configured payout
wallet. Duplicate items are combined and products locked in sorted order.
Bounded stock counts unpaid unexpired plus paid/finished reservations within its
source-defined UTC/manual window;
cancelled/expired orders release availability. Digital recurring app products use the canonical subscription fulfillment
described in `recurring-products.md`; address-required products remain unavailable.
Exact rational prices are added before the single truncation to canonical
thousandths. Positive amounts and quantities, ranges, body sizes and duration
limits are bounded. Idempotency keys bind scope, operation and payload.

Payment reserves payer availability in the existing wallet's held amount, creates
one managed canonical transaction, records the payer, updates the order and
writes wallet plus commerce outboxes in one SQL transaction. Merchant finish
releases the hold; cancellation refunds it. Replays do not duplicate events or
money. An already finished order cannot be cancelled into a second refund.
Orders cannot be paid by a different account or wallet on replay. An unpaid
app order locks and rechecks its canonical publisher, project and app before the
order row, refusing deleted ancestry or a non-production app. Those locks remain
held through payment commit, including after a concurrent suspension lock wait.

Subscription payment releases the same hold and activates in that transaction.
A first paid cycle records the payer and price for renewal. Renewal consumes
that durable opt-in only while account and definition are active and price,
currency and payee remain unchanged. It never consults caller-provided renewal
amounts. Each cycle is durable and serialized with cancellation. Insufficient
funds expire the subscription without a partial order or debit; manual order
creation can recover. Cancellation immediately ends access, as in the retained
service. New paid grants queue after the group tail; extension of an active
subscription preserves its current beginning. Provider sandbox subscriptions
remain marked testing and are excluded from active group entitlements.

A gift cannot become redeemable before its order is paid. Until redemption its
money remains held. Cancel/expiry returns the hold to its original payer;
redemption releases it and creates exactly one subscription. Codes are random
128-bit capabilities, are returned only to authorized gift readers, and are
stripped from outbox payloads. Gifter self-redemption is rejected.

`ProcessDue(ctx, limit)` handles expiry and renewal only, never delivery or remote
payment. Every item commits independently, and concurrent workers recheck
locked state. Outbox publication/ack, delivery retries and subscriber side effects
require the host's publisher and idempotent consumers.

## Explicit remaining boundaries

No automatic migration of legacy Wallet databases; realm-owned payer wallet
payment; merchant payouts; coupon/free-trial programs; nonintegral or unbounded cycle durations; address-contact fulfillment; subscription activation switching;
billing relation views; sponsor badge/golden-point grants; provider gold-currency
minting; external billing cancellation; production provider clients; delivery of
outbox events. Unsupported requests fail closed or are unrouted. These are not
represented by fake-success adapters. Subscription definitions can configure
only actual supported durations and internally provisioned payout wallets.

## Verification

Local compiler tests passed across `go test ./...`. Commerce and existing wallet
unit suites passed with the race detector; focused `go vet` passed. Local real
PostgreSQL tests were deliberately not run because no local sockets were
allowed. The following guarded disposable-PostgreSQL tests compile and must run
without skips in CI:

1. `TestPGCommerceOrderSettlementAndIsolation`: canonical decimal total, replay,
   escrow settlement/refund, sanitized public detail, account privacy and ordinary
   wallet transition/expiry isolation.
2. `TestPGCommerceConcurrentPaymentStockAndRollback`: independent-pool duplicate
   payment, stock contention and rollback of wallet/outbox effects on injected
   commerce outbox failure.
3. `TestPGCommerceSubscriptionRenewCancelAndGiftRedeem`: activation, exact renewal,
   renewal replay, cancellation, unpaid gift rejection and competing redemption.
4. `TestPGCommerceRestartPersistence`: same-pool proof, missing proof, verified
   provider replay/conflict, revoked app token, stale grants; restart branch
   verifies persisted money hold/order/outbox without recreating rows.
5. `TestPGCommerceHTTPPaymentAndUnavailableProvider`: payment factor boundary and
   fail-closed provider routing.
6. `TestPGCommerceExpiryAndAuthorityOrdering`: durable unpaid expiry, held gift
   refund on expiry, app/project/publisher state rejection and app suspension
   during a lock wait, and authority revocation while payment waits on the account.

Add all six to the before phase and only RestartPersistence to the after phase.
All funding, accounts, app credentials and provider purchases in these tests are
synthetic and confined to the marked disposable cluster.

Recurring app product fulfillment and bounded whole-cycle pricing are now implemented;
see `recurring-products.md` for the complete contracts, independent policy and tests.

`product-stock.md` documents periodic/manual stock windows, atomic recurring
reservations, release/consume transitions and restart validation.
