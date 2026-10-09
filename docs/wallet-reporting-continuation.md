# Wallet reporting continuation

Based on retained AGPL-3.0-only authored WalletController.cs, blob
10650f1a12912532d70f6ea422a69557a508a224, and PaymentService.cs, blob
1d599181db3e6482b21e1e86463739d3cf2dadd1, at DysonNetwork revision
4ee2d0cebb9da8284a92483fee6dce795bf6947c. The full retained files, licenses,
WalletService, owned EF migrations and authored Solian callers are indexed in
ports/wallet/accounts/source/manifest.json. The filtered stats caller at
Solian revision ffd0838e5f04cb40bea311a39118ebeedad210c1 sends repeated wallets
and currencies query values. No Shared SDK/model/protocol source was used.

## Runtime routes

- GET /api/wallets/stats: period defaults to 30 days; repeated wallet IDs select
  an authorized union, defaulting to the current primary personal wallet.
  Currency filtering defaults to points and is case-insensitive.
- GET /api/wallets/overview: current primary personal wallet, optional inclusive
  startDate/endDate RFC3339 instants. Explicit offsets are normalized to UTC;
  ambiguous date-only/server-local timestamps are rejected.
- GET /api/wallets/orders: transaction-linked canonical commerce order history,
  with wallet, direction, type, status, offset and take; X-Total remains correct
  for empty/out-of-range pages. Sort by created_at DESC, id DESC before slicing.
  Source direction aliases and authored numeric/name filters are accepted.
- Existing /wallet/wallets aliases route through the same handler.

In canonical same-pool host composition, each read validates the original bearer proof in the canonical transaction,
locks current account/realm lifecycle and selected wallets, and rechecks proof
and the same selected-wallet scope before commit. The latter rejects a realm
grant that expires during a wallet-lock wait. Realm access uses current canonical membership. A public realm
or knowing an ID is insufficient. A mixed authorized/unauthorized request fails
as a whole. No cache can retain a revoked read. Current monetary aggregates and
order counts share one PostgreSQL statement snapshot. Pagination/count share
one snapshot too. GET operations create no ledger/event/authorization rows.
The pre-existing explicit separate-pool compatibility composition does not offer
same-transaction authority guarantees and is not certified by these PG cases.

## Explicit owned corrections and limits

Only confirmed canonical ledger events enter monetary totals. Held/pending,
refunded and cancelled reservations contribute no realized income or spending.
Fund receipt conversion and self-refund records cannot double count value.
TotalTransactions still counts selected retained records, including their
pending/refunded states. TotalOrders retains the source payee-side created-order
count, including unpaid orders; order amounts are not added to ledger totals.
Selected internal transfers are counted once and appear on both sides, net zero.
Reports use transaction creation timestamps, matching source, rather than
retrospectively moving a confirmed transaction to its confirmation day.

Exact base-10 mills are preserved. Unlike currency units are never summed:
overview total_income/total_spending/net_total and stats totals are null for
multiple represented currencies; summary provides type/currency amounts.
A category spanning several currencies is null too. Single-currency and empty
reports preserve numeric totals (empty is zero). This deliberately corrects the
source's dimensional error and requires multi-currency consumers to use summary.
Aggregate overflow fails instead of rounding or wrapping.

This is an explicit owned API tradeoff, not a claim of unchanged client parsing.
The retained WalletController WalletStats and PaymentService WalletOverview
declare non-null decimal totals. The retained Solian wallet.dart filtered
provider (lines 581-598) requests one wallet and one currency, then calls
SnWalletStats.fromJson; its balance cards pass totalIncome/totalOutgoing into a
required double parameter (lines 3112-3149 and 3307-3312). That authored path keeps
numeric totals for a single represented currency. The retained caller does not
contain the generated model/parser, so its handling of null cannot be certified
without excluded SDK source; no such source was read or copied. Multi-currency
consumers must explicitly support nullable totals and render summary by currency
before adopting this extension. No zero fallback or invented conversion hides
unlike units. Exact JSON numeric mills are preserved by the backend; a consumer
that parses them as binary doubles can still lose precision at large values.

These source routes are authentication-only reads, with wallet ownership or realm
membership checks; no new named reporting permission is invented. Same-transaction
authentication verifies the current account, original session/proof, token epoch
and deadline. A canonical session-scope change advances the epoch and invalidates
the old proof, but a valid session with an unrelated scope is not by itself denied
by these authentication-only source contracts.

Order history strips arbitrary Meta and Coupon data for all readers. A wallet
viewer/beneficiary has ledger access, not permission to receive purchaser notes,
fulfillment addresses or coupon state. Canonical relational identity/status/
amount fields overwrite the stored document projection. Only the public typed
transaction DTO is included; actor IDs, fingerprints and replay keys are omitted.

Bounds are period 1..3660, 100 selected wallets, 32 currency filters, take 0..100,
nonnegative offsets. Order type is the supported Transfer=0/Payment=1 mapping;
other original transaction types are not invented. Reporting does not import
legacy rows or add admin, balance-minting, settlement or provider operations.

## Validation boundary

Synthetic disposable PostgreSQL cases cover composed routes/current proof,
ownership/realm privacy, inclusive timezone dates, exact multi-currency values,
transfer holds/refunds, canonical fund payouts/refunds, commerce history
pagination, lock-wait revocation, and original-proof restart persistence.
The lock-wait case also covers session revocation, a session-scope epoch change,
and expiry of realm read grants while stats/orders wait for a wallet lock.
Offline unit/race/build/SQL grammar checks are not PostgreSQL acceptance.
Actual PostgreSQL, concurrent transaction and restart results require the
coordinator's guarded CI. No sockets, real accounts, financial transactions,
provider calls or external transfers are used locally.
