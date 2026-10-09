# Owned coupon checkout extension

This is an explicitly configured independent policy. It does not claim legacy
coupon schema, enum, issuance, discount formula, rounding, renewal, usage or
refund compatibility. Existing `coupon` string request members on subscription
and gift purchase are retained. The issuance and order quote routes below are
new owned interfaces.

## Source findings and boundaries

The pinned AGPL-3.0 root licenses for DysonNetwork
`4ee2d0cebb9da8284a92483fee6dce795bf6947c` and Solian
`ffd0838e5f04cb40bea311a39118ebeedad210c1` were rechecked. Exact source bytes,
Git blobs and SHA-256 digests are in
`ports/wallet/commerce/coupon-source/manifest.json`.

The complete authored Wallet/Payment directory listing at that Dyson revision
has no separate coupon service/controller. The authored Wallet/Models directory
contains SubscriptionCatalog.cs and WalletProductOptions.cs only. This is a
bounded negative finding for those directories, not a claim that every historic
repository or external document was searched. SubscriptionController's
CreateSubscriptionRequest and SubscriptionGiftController's purchase request
accept an optional coupon string. The complete retained SubscriptionService
looks up that string as UUID or identifier at lines 281–296 and 1493–1501;
subscription pricing and gift pricing invoke the excluded model's
CalculateFinalPriceAt (209 and 1522). OrderController and PaymentService expose
no coupon calculation/issuance contract. The missing calculation is therefore
unavailable within the permitted source boundary, not merely unresearched in
those authored services. No excluded models or generated bindings were read.

The actual retained Solian payment overlay, order detail and stellar program
callers do not define a coupon model or formula. The exact payments subtree was
checked: its authored files are those callers, quota purchase UI, IAP service
and README. The retained authored Swift Models.swift provides explicit
snake_case base_price/final_price decoding in SnWalletSubscriptionRef, but no
coupon schema. The payment README has simplified examples that disagree with
the actual current caller (including status and PIN bodies); it is evidence of
UI intent only and is not used to override executable caller contracts. There
is no retained evidence establishing legacy numeric coupon type/scope values,
a percentage formula or a cancellation usage-restoration rule.

## Explicit operator policy

Nothing is enabled by default. Configure both:

- `-coupon-policy seller_fixed_mills_v1`
- `-coupon-cancellation-policy retain_usage`

Equivalent Go composition is
`Config.CommerceCouponPolicy = commerce.CouponPolicy{Type:
commerce.SellerFixedMills, Cancellation: commerce.RetainCouponUsage}`.
Unknown, partial and omitted configuration never chooses financial behavior.
The host verifies the configured tables at readiness; constructors do no DDL.
Apply `internal/commercepg/coupon_schema.sql` once after the canonical commerce,
authority, app and wallet schemas. Runtime issuer checks also use the canonical realm tables. CI registration is supplied separately.

The seller explicitly sets a positive exact-mills reduction, currency, complete
product allowlist, audience (`all` or specific `accounts`), starts/expires times,
maximum uses and per-account maximum uses. `type` must be `fixed_mills`; `scope`
must be `order`, `subscription` or `gift`. No missing scope/audience/cap defaults
are supplied. Discount input with nonzero fractional mills is rejected; it is
never silently rounded. Compute the existing canonical whole-order gross once
(exact product prices summed, then truncated to mills), then subtract the exact
discount. The resulting net must remain positive. There are no fees, percentage
math, platform subsidy, value credits or wallet minting. The seller receives
only the net actually held and settled from the buyer's canonical wallet.

`retain_usage` is the required configured cancellation policy, not an assertion
about legacy behavior. Failed or rolled-back payment consumes no use. Successful
payment consumes one use permanently, including if subsequently cancelled or
refunded. Cancellation returns the captured net, once. It does not restore use
or credit the undiscounted gross. No other cancellation policy is implemented.

## Runnable interfaces

- `POST /api/coupons`: app credentials, `identifier`, and explicit `terms`.
  `Idempotency-Key` binds app, request and operation. Both the app declaration
  and credential must contain the owned `coupons.manage` scope.
- `POST /api/coupons/{id}/revoke`: issuer credentials. Revocation is idempotent;
  coupon terms, identifier, issuer and beneficiary cannot be edited or reused.
- `POST /api/orders/{id}/coupon/preview`: authenticated `orders.pay` account and
  `{ "coupon": "code-or-uuid" }`; canonical order-based eligibility and net
  preview, without reservation or usage consumption.
- `POST /api/orders/{id}/coupon`: same body and authority; captures the quote
  once and binds the order to that buyer. An identical application replays;
  substitution, removal or foreign-buyer application rejects. Payment does not
  require the client to repeat a coupon field and cannot omit the saved discount.
- Existing subscription/gift purchase accepts its evidenced `coupon` string.
  Coupons may target only an explicitly app-owned definition with the issuer's
  canonical merchant beneficiary. Platform definitions, provider purchases and
  unsupported app-product gifts remain unavailable.

The gateway exposes `/wallet/...` aliases. Coupons use UUID or exact
case-sensitive app-local identifier; discovery across issuers is not supported.
Order coupons require canonical items, not arbitrary caller-supplied amounts.
Subscription/gift scopes use canonical definition identifiers; order scope uses
canonical item identifiers. All items must be eligible, with one discount per
whole order. Public checkout strips coupon codes, buyer and policy snapshots.

## Immutable quote and payment invariants

Each quote captures gross, discount, net, currency, buyer, issuer app/publisher,
beneficiary, scope, selected products, coupon identity, expiry, explicit policy
and a hash of the complete immutable coupon terms. A quote does not reserve
capacity. The separate `checkout_terms_hash` captures the canonical product or
definition snapshot, including product version and current purchase policies.
Coupon checkout rejects changes to that snapshot, including price or currency;
it never substitutes a new net. Product share locks precede order and coupon
locks. This opt-in requirement does not change non-coupon orders' captured-price
and beneficiary policy. A canonical coupon row lock serializes all use checks and inserts;
order ID is unique in redemption records. Payment locks/rechecks canonical app
ancestry, declared issuer scope, current direct or realm owner enablement (including effective disable-account punishments), live realm state and merchant binding,
then requires exact unchanged coupon terms and the bound buyer. The coupon row
is held through wallet hold, fulfillment and final admission. Expiry, account
and issuer observations are refreshed after wallet waits. Quote rejection never
silently reprices an order. All wallet changes, redemption, fulfillment and
outboxes commit or roll back together.

A paid replay returns the saved quote and transaction without revalidating a
later coupon revocation or creating a second use. Refunds also use the captured
transaction independently of later policy shutdown. Neither operation changes
the historical quote. Owner/realm eligibility uses fresh READ COMMITTED observations without new account locks; a change after the final observation can serialize after payment. State is durable across fresh repository/pool instances.

An app-bound coupon gift retains the gift escrow lifecycle. App order-status
credentials cannot finish or cancel a gift order; the canonical gift redemption,
buyer cancellation or expiry path settles or refunds the captured net.

Discounted subscriptions authorize one initial cycle purchase only. An explicit
`is_auto_renewal:true` with a coupon rejects, and omitted auto-renewal becomes
false for that coupon purchase. The coupon marker follows a paid gift into its
recipient subscription, preventing the discounted final price becoming an
unmarked standing/manual renewal price. A further order for a subscription
carrying that paid coupon rejects. New, explicitly requested purchases remain
subject to coupon capacity. Existing non-coupon renewal behavior is unchanged.
