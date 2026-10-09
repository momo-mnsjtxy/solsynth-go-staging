# App product stock and reservation lifecycle

This change extends the existing product CRUD, order checkout, payment,
cancellation, expiry and recurring subscription routes. It creates no inventory
counter, table, parallel wallet balance, provider call, shipping job or delivery
success. An order item is the durable reservation; canonical order status is its
reservation/consumption state. The source manifest is
`ports/wallet/commerce/stock-source-manifest.json`.

## Retained source contracts

The complete licensed OrderController's `ValidateStockAsync` and
`GetStockWindowStart` establish these rules:

- `unlimited` ignores stock capacity.
- `daily` begins at UTC midnight; `weekly` begins Monday at UTC midnight;
  `monthly` begins on the first UTC day; `yearly` begins January 1 UTC.
- `manual` begins at `last_restocked_at`. A legacy manual product without that
  timestamp counts its whole order history.
- Active unexpired unpaid orders reserve their item quantities. Paid and finished
  orders consume quantities. Cancelled and expired orders consume none.
- Each window counts qualifying order items whose order was created within that
  window. Refunding an old-window order does not enlarge the current window.

The complete licensed Develop AppProductController establishes stock mode and
quantity request fields, and stamps `last_restocked_at` / `last_restocked_quantity`
when a manual quantity is supplied. Published authored APP_PRODUCTS documentation
describes the same semantics and product/order routes. Full authored Solian order
detail, payment overlay and stellar purchase caller remain retained; the latter
establishes bounded cycle multiples. No generated model or prohibited dependency
was substituted for missing authored stock UI. No enum ordinals were inferred.

## Atomic implementation

`owned_app_products` remains the source of product state. The public/private product
projections now include both restock fields. All six source modes are accepted;
bounded modes require a nonnegative quantity. Unknown modes fail explicitly.
Manual restock uses the database clock after product-management locks are held,
so a lock wait cannot stamp a restock before earlier admitted purchases.

Commerce locks product rows in the existing deterministic identifier order before
admitting a basket. After every lock is acquired, it reads one fresh clock for
all capacity checks and the order creation/expiry timestamps. A waiter crossing
a UTC boundary or manual restock therefore cannot allocate against an old window.
A manual timestamp ahead of the commerce clock fails closed until clocks agree.

The stock query joins existing `commerce_v1.order_items` with existing orders.
Capacity admission and inserting the order, items, idempotency record and outbox
occur in one transaction. There is no mutable availability field to reconstruct
or double-refund. Product IDs keep reservations attached to their canonical
product even after a SKU rename; separate products/apps never share capacity.

Canonical payment turns reserved quantity into consumed quantity while placing
the existing wallet hold. Merchant finish retains consumption. Unpaid cancellation
releases reservation; paid cancellation refunds the canonical hold and releases
consumption in the same transaction. Unpaid expiry stops counting immediately at
the expiry boundary, independently of the bounded worker's later status update.
Failed writes or outbox inserts roll back reservations and wallet effects together.
Replaying order creation reuses the same reservation.

## Explicit owned recurring policy

The retained source supports recurring products and stock fields, but does not
fully specify stock-limited subscription renewal behavior. These are independently
authored policies, not claims of exact upstream parity:

1. A recurring purchase reserves one unit per purchased base cycle. Quantity is
   derived only from the subscription's persisted cycle days and validated base
   cycle, limited to 1..12 whole cycles. Stored document and row cycle days must
   agree. Item quantity multiplied by the exact captured unit price must equal
   the immutable order amount/currency before the reservation can commit.
2. Direct subscription order creation and renewal acquire the product write lock
   before definition/order admission. This avoids concurrent shared-lock upgrades
   and serializes different buyers competing for the same capacity. Merchant
   recurring orders retain the earlier single-SKU, quantity-one policy.
3. Renewal checks its previous finished paid order's amount, currency, payer and
   payee against the durable subscription contract. It cannot reserve a new number
   of units by changing untrusted renewal metadata or a stale catalog price.
4. Out-of-stock renewal uses the existing renewal savepoint, rolls back any
   tentative order/reservation and expires the subscription at its already-paid
   boundary without charging. This failure-to-expiry transition is owned policy.
   It does not shorten an unexpired paid interval or fabricate an entitlement.
5. Cancelling a subscription also atomically cancels its still-unpaid orders and
   releases those reservations. Paid/finished orders retain consumed stock and
   receive no invented refund. Account authority serializes cancellation with
   payment/renewal; repeat cancellation produces no extra release.

Operational restock/capacity changes do not alter a buyer's captured price or cycle.
Price, currency, cycle, payout, app/product eligibility and consent changes retain
the earlier immutable recurring purchase policy. Address/contact fulfillment,
external workspace/provider fulfillment and physical delivery remain unavailable.
An authorized merchant's existing finish transition is a commerce state update,
not a claim that this backend shipped an item.

## Verification and integration

The stock patch is based on continuation `2d43b198bfd980bf7ecb9ddf01f16c469173fd6d`
plus frozen recurring patch
`80d7f8d54f5303b9d5735a194fdef044fbb9c999e9ba37cb7fe70ad54975b8d1`.
The earlier recurring and wallet-funds artifacts remain unchanged. There is no
new SQL migration or host route. The already installed product-catalog hook and
commerce handler expose the implementation.

New guarded disposable PostgreSQL tests cover:

- concurrent single-product reservations, consume/finish, cancellation/refund and
  idempotent creation;
- each UTC rollover, expiry without a sweep, old-window cancellation and outbox
  rollback;
- authenticated host manual-restock mutation, public field projection and capacity
  reset without double release;
- cycle-scaled recurring reservations, stock-exhausted renewal, and cancellation
  releasing unpaid subscription orders;
- concurrent subscription buyers, restock/window lock waits, and payment/cancel
  races with conservation and no stale capacity;
- rejected mismatches between captured cycle/price economics and reservation units;
- persistence of both reserved and consumed units across database/process restart.

`TestPGProductStockRestartPersistence` is required in both phases; all other
`TestPGProductStock*` cases are required before restart. Existing recurring renewal
tests now make only their fixture subscription due instead of advancing a global
maintenance clock through unrelated fixtures. This test-only correction is in the
dependent stock patch; the frozen recurring artifact was not rewritten.

Local unit/compile tests, source-hash checks, focused race tests, vet, integration
guard/wiring checks and offline PostgreSQL grammar checks are recorded in the
delivery manifest. Actual PostgreSQL locking, transaction and restart execution
is reserved for the parent's authorized private CI; no local socket attempt was made.
