# Recurring custom-app products

This continuation completes a digital recurring product purchase inside the
existing owned app, commerce, authority and wallet model. It adds no tables,
balance store, credentials, provider calls, startup migration or implicit funding.
The composed backend installs the product-catalog hook; standalone app hosts may
install `apppg.Repository.WithProductCatalog(commerceRepository)` explicitly.

## Source contracts and conflicts

`ports/wallet/commerce/recurring-source/manifest.json` records the complete pinned
licensed files, Git blob IDs, SHA-256 hashes, revisions and source links. Existing
retained sources are referenced in place. New evidence includes the authored
Wallet `PaymentService` and `AppProductService`, and the complete authored Solian
quota purchase sheet. Complete retained OrderController, SubscriptionController,
Develop AppProductController/AppProductService, SubscriptionService, Solian order
detail, payment overlay and stellar-program UI are also in the manifest.

- Develop's controller establishes product `recurrence`, `group_identifier`,
  fulfillment `required_scopes` / `is_address_required`, state, and CRUD routes.
  Its service maps weekly/monthly/yearly to exactly 7/30/365 days.
- Wallet's OrderController establishes product-item order creation, canonical
  decimal snapshots, `meta.app_subscription`, app identity, required-address
  intent, and account payment. Its use of the first line to determine recurrence
  is ambiguous for mixed baskets; the owned implementation requires one recurring
  SKU with quantity one. It never applies the first item's entitlement to a basket.
- Upstream raw product identifiers are app-local while this commerce catalog's
  identifier is global. The independently authored identifier is
  `app-product:<canonical-product-UUID>`. The public definition retains the SKU
  as `product_identifier`, the app as `app_identifier`, and app-local groups are
  namespaced under `developer.app:<app-UUID>`. Another app's identical SKU cannot
  replace a definition or share a subscription group. Rename retains product ID.
- The full stellar caller sends `cycle_duration_days = 30 * quantity`, clamped
  to 1..12, sometimes omits `payment_method`, opens the canonical order checkout,
  and cancels by subscription UUID. Omitted method remains `solian.wallet`; reads
  and cancel accept either an actor-owned identifier or UUID. The independently
  authored pricing rule accepts whole base-cycle multiples up to twelve and at
  most 366 days. It multiplies the exact unit price before one truncation to wallet
  thousandths. Unsupported fractional or out-of-range cycles fail explicitly.
- Source default auto-renewal cannot be delegated to a merchant's request. A
  merchant-created order grants a single paid cycle without renewal. Direct
  app-product subscription creation requires explicit buyer `is_auto_renewal:true`
  to opt into future charges. Platform definitions retain their existing default.
- The authored quota sheet and Wallet AppProductService expose a separate
  provider/workspace delivery path. No workspace authorization or external
  entitlement completion is inferred from that evidence. Those providers remain
  unconfigured; no success stub or fabricated provider receipt was added.
- Product docs require explicit app scope consent separate from an address.
  The implementation reads the same `owned_app_consents` records used by owned
  apps and OAuth, checks the app's declarations and every product-required scope,
  and records the exact consent version. Purchasing never grants scopes.

No unlicensed Shared/NeTo/Spec schema, proto, generated binding, canceled
SoSYS/Socommon source or unlicensed Suki repository was read or ported. Imports
appearing in licensed authored evidence do not authorize retrieving those sources.
Pinned Solian root tree metadata showed no standalone authored app-product model
or checkout caller beyond the retained payment flows; no SDK implementation was
substituted. Status values and existing metadata conventions retain the earlier
commerce provenance's explicit independent-policy boundaries.

## Runtime behavior

Product create/update/delete synchronizes its definition in the same SQL
transaction as the canonical product mutation. It reuses the operator-provisioned
commerce merchant binding and verifies the wallet belongs to the app's canonical
publisher. Missing payout configuration keeps product metadata editable but does
not publish a purchasable subscription definition. Checkout also synchronizes a
supported existing product after provisioning. Deleted/disabled/nonrecurring or
unsupported products disable their existing definitions, preserving history.

Supported recurring fulfillment is digital, no address, with
any requested scope backed by current explicit consent. Address/contact delivery, mixed recurring baskets and merchant quantities
above one remain unavailable or invalid. A one-time item requiring additional
scope fulfillment now fails unavailable instead of silently ignoring its policy.

The buyer needs active consent to the specific app even when required scopes are
empty. Existing first-party board consent can cover board-capable products; the
existing OAuth consent flow covers its supported scopes. Unsupported scopes never
gain a new grant endpoint. Consent is checked under the account, app and consent
locks and checked with `clock_timestamp()` after waits. Revocation, expiry, or a
new consent version stops renewal and makes the app subscription unavailable in
account/group access projections. Product/app disablement or deletion also hides
the catalog and prevents new purchases. Revoked app secrets cannot create orders.

An app order snapshots definition identity, app, SKU, exact unit price, currency,
group, cycle and required scopes in its server-owned metadata. Public checkout
exposes this safe purchase description while omitting payer, transaction and
arbitrary private metadata. The client cannot submit or replace recurrence data.

Payment uses existing order PIN proofs, same-pool authority, canonical wallet
hold/settlement and outboxes. It atomically creates and activates one canonical
subscription, links that subscription to the existing order, settles that order
and marks it finished. Repeated or concurrent payment produces one transaction,
one subscription and one activation event. Failed fulfillment or outbox insert
rolls back money, order changes and entitlement together. Finished purchases
cannot be cancelled through merchant status into an entitlement-plus-refund.

Direct subscription checkout uses the existing subscription/order/pay routes.
The subscription snapshots product terms, buyer consent version, cycle and final
price. Order creation, payment and renewal reject changed economics or scope.
The existing bounded maintenance worker expires subscriptions when price,
currency, payout, recurrence, consent, app or product eligibility changes. It
never silently upgrades a subscription to newly edited terms. Existing platform
renewal now also verifies cycle-scaled price rather than comparing a multi-cycle
total to a one-cycle price. Cancellation is immediate, account-bound and durable.
External delivery of outbox events is still the host publisher's responsibility.

## Validation

All Go packages compile and their local tests pass. Focused race tests cover
commerce, owned apps, their HTTP ports and the composed application. `go vet
./...` passes. Retained evidence tests verify full bytes and Git blob hashes.
Offline PostgreSQL grammar validation is distinct from database execution.

The guarded disposable PostgreSQL runner requires these tests before restart:

1. `TestPGRecurringProductCheckoutAtomicAndIsolation`: full rollback, concurrent
   payment, exact money conservation, single grant/event, app/SKU isolation,
   default no renewal, UUID cancel and no post-fulfillment refund.
2. `TestPGRecurringProductRenewalEconomicsAndRevocation`: exact two-cycle price,
   one renewal/replay, then no charge after price, currency, payout, cycle,
   consent or product-state changes.
3. `TestPGRecurringProductConsentWaitAndSnapshot`: consent revoked during a real
   lock wait, stale-price checkout, forged metadata, invalid quantity and revoked
   app credentials.
4. `TestPGRecurringProductOppositeWalletPayments`: concurrent purchases between
   two app owners sharing opposing payer/payee wallets use sorted canonical locks.
5. `TestPGRecurringProductHostPINCatalogAndCancel`: real backend route composition,
   product catalog synchronization, missing payment policy, consumed canonical
   payment proof, settlement, cancellation, deletion and unavailable provider.
6. `TestPGRecurringProductRestartPersistence`: fresh pool reads and guard isolation,
   then actual database/process restart checks of linked subscription, frozen
   terms, no standing charge, exactly one wallet transaction and one event.

The sixth test is also mandatory after restart. No real PostgreSQL/socket run
was attempted in this workspace; the parent's authorized private CI must execute
these cases without skips. All fixtures are synthetic and run only behind the
existing marked disposable-cluster guard.

The dependent product-stock continuation adds all source stock windows and
cycle-based recurring reservations; see `product-stock.md` for the owned renewal
failure policy and additional concurrency coverage.
