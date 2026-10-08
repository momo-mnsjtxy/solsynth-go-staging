# Commerce provenance and policy

This source-informed implementation is AGPL-3.0-only. `source/manifest.json`
records exact bytes, SHA-256, git blob IDs, repositories, revisions and source
links. Existing retained files are referenced in place. New retained files are
SubscriptionService and authored Solian payment/order/stellar UI; no generated
SDK or Shared/NeTo/Spec source was consulted. The root and Solian license copies
already retained under accounts/source apply.

Sources establish `/api/orders`, `/api/subscriptions`, and
`/api/subscriptions/gifts` DTOs/routes, merchant escrow intent, refund and expiry,
subscription purchase/order/renew/cancel, gift creation/payment/redemption, and
provider checkout/restore/webhook routing. PaymentService truncates final order
amounts to three decimals; product prices stay exact until the complete total.
Solian submits `pin_code` and `payer_wallet_id`; it uses `solian.wallet` and `gift`
payment identifiers. The wallet list and order detail both establish order
statuses 0 unpaid, 1 paid, 2 cancelled, 3 finished, 4 expired. The overlay reverses
2 and 3 in one display switch; this implementation follows the two matching
list/detail callers and does not claim to resolve that upstream UI defect.
The stellar caller establishes active subscription status 1 and gifts 0 created,
1 sent, 2 redeemed, 3 cancelled, 4 expired. Other subscription status numbers are
an independent policy, not inferred from unavailable Shared enum sources.

The database is new, independently owned storage. Canonical app ancestry,
products, scoped app tokens, account authority, wallets, pockets, transactions,
and wallet outbox are composed directly. The source's remote gRPC calls, unsafe
implicit system balances, ad hoc push calls, repeated order delivery, and
unverified provider success are not reproduced. New commerce events commit in
a durable outbox; delivery belongs to an authenticated external publisher.

Independent safety contracts are documented in `docs/commerce-continuation.md`.
Notably: app client secrets are the existing scoped opaque app credentials;
public checkout omits private metadata/payer/transaction details; PIN verification
is mandatory at HTTP pay; account writes retain same-pool authority locks;
all held money remains in the canonical wallet ledger; no API creates money;
open gift capabilities use 128 cryptographic random bits and never enter events;
paid gifts require recipient redemption and refund on cancellation/expiry;
external provider support consists of fail-closed injected verification adapters.

The recurring-product continuation retains additional complete licensed source and
client files in `recurring-source/manifest.json`. `docs/recurring-products.md` records
source conflicts, app-scoped identifiers, exact whole-cycle pricing, explicit buyer
renewal opt-in, shared consent enforcement, and remaining fulfillment boundaries.

Stock/reservation source contracts are pinned in `stock-source-manifest.json`;
`docs/product-stock.md` separates retained window/status behavior from owned
recurring allocation, immutable-price checks and stock-exhausted renewal policy.

`activation-source-manifest.json` records complete retained evidence for pending subscription activation and the incomplete coupon investigation. Exact routes, permissions and authored callers plus owned duration/opt-in corrections are documented in `docs/subscription-activation.md`.
