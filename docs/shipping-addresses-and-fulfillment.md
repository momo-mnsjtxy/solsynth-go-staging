# Canonical shipping addresses and order fulfillment

This slice extends existing canonical contacts and commerce. It adds no account
store, address service, payment ledger, carrier adapter or external transmission.
The complete retained authored `OrderController.cs`, `MerchantController.cs`,
`APP_PRODUCTS.md`, Solian contact settings and order-detail callers establish the
source contracts. Their exact hashes, revisions and licenses remain in the
existing commerce, operation-source and account-settings manifests. No excluded
Shared/SDK/Spec/NeTo/generated source was consulted.

The authored Solian dropdown explicitly selects address type **2**, email 0 and
phone 1. This value is observed in the licensed caller, not inferred from an
excluded enum. The controller requires `meta.address_contact_id` and
`meta.address_snapshot` when any product requires an address, then replaces the
supplied text with the paying account's canonical address at payment. The product
documentation distinguishes a required checkout destination from broader saved
contact access: a purchase never grants an app access to a customer's address book.

## Contact routes and authority

Existing `POST /stargate/contacts` with `{type,content}`, owner list and DELETE
remain. The canonical `owned_authority_contacts` rows and existing audit are
reused. The explicit owned additions are:

- `GET /stargate/contacts/{id}`: current owner detail.
- `PATCH /stargate/contacts/{id}` with `{ "type": 2, "content": "..." }`:
  address-only replacement. It cannot change kind, account ownership, primary
  status or verification state. An identical repeat is a no-op without a new
  revision or audit. Changing the address advances an internal revision.

No authored address-update controller/caller was found in retained evidence, so
these two routes are owned extensions. No frontend change or original SDK update
route compatibility is claimed. Existing fields and strict type/content shape
are preserved. Content is bounded to 2,048 UTF-8 bytes, accepts multiline text,
rejects NUL/empty values and is never parsed as a URL or sent to a geocoder.
Contact bodies remain bounded to 8 KiB and duplicate/unknown fields are rejected.

All private contact reads now require the existing `account.contacts.manage`
permission, including the session's explicit scope; a scoped publishing token
cannot read saved addresses. Mutation requirements and the current first-party
contact service remain. Creation, deletion, primary promotion and the new update
retain the existing ten-minute session-creation window. This is an existing age
bound, not a new assertion of recent credential proof. Current account/session
state and scope are revalidated through the canonical transaction guard after
relevant waits. An ordinary first-party session with explicit contact-management
scope is permitted; OAuth sessions remain forbidden by the canonical contact
service even when they declare that scope. No enduring authentication credential
is created.

Addresses remain private and unverified. They cannot become login factors,
primary email contacts or public verified profile contacts. The existing physical
address verification/provider gap remains explicit. Soft deletion retains the
old row; recreating the same text creates a new identifier and cannot revive an
old checkout selector.

## Required-address checkout

The original app order request shape is retained, including product items,
scoped client credentials and `meta.address_contact_id` / `meta.address_snapshot`.
Only one-time app products with required-address fulfillment are enabled here.
Recurring physical delivery and one-time products with additional required app
consent scopes remain unavailable until their own renewal/consent flow is defined.
The digital recurring path is unchanged.

Creation validates the original two metadata fields and records an unverified
UUID selector in `commerce_v1.order_destinations`. It does not query another
account's contact using an app credential. The caller's text is accepted for the
original request shape, included in the request fingerprint, and discarded. It
never becomes a trusted snapshot, public value or general event payload.
Unpaid order metadata exposes only `fulfillment.requires_address=true`.

Payment still requires current `orders.pay`, owned payer-wallet access and the
real configured payment/PIN policy. Under the same payer account lock as contact
writers, checkout locks the order and live type-2 contact, verifies that the
contact belongs to the payer account, and captures its current canonical text,
revision and time. Missing, deleted, foreign or wrong-kind contacts fail before
any wallet hold. The capture, canonical hold, payer/order binding, order metadata
and payment event commit together. Rollback restores all of them.

An edit committed before checkout is reflected in the paid snapshot. An edit or
deletion waiting behind checkout changes the saved address only after the snapshot
commits. Paid replay returns the original destination without requiring that the
saved contact still exists. This is a payment-time snapshot, as evidenced by the
authored controller; no app-only request may claim canonical ownership at order
creation. The paid snapshot's text and selector are immutable, including after
cancellation, through both adapter behavior and explicit database guards on the
receipt and its paid order JSON projection.

Product prices, quantity, currency, payee binding and paid transaction identity
remain the existing immutable order economics. Stock uses the existing reservation
and status model. Address handling does not mint credit, replace beneficiaries,
reprice an order, create another ledger or turn a failed payment into fulfillment.
A deleted unpaid destination requires creating a new order with a valid selector;
there is no unverified destination-rebinding endpoint.

## Audiences and fulfillment state

- Anonymous checkout retains public order/app display data and the required-address
  flag, with no saved-contact identifier, address text, payer wallet or transaction.
- Private `orders/mine` list/detail now require current `orders.view` and existing
  canonical ownership. The payer sees the immutable destination in original
  `meta.fulfillment` shape.
- App create/idempotency replay and status-operation responses redact canonical
  shipping data. A token that can create orders or manage status cannot thereby
  retrieve a payer's updated private destination.
- Existing merchant order lists require both `orders.view` and current publisher
  management membership. Paid/finished rows expose destination text for that
  merchant's order, but omit saved-contact identifiers and payer-wallet fields.
  Unpaid/cancelled/expired rows expose no address text. Payout-wallet reuse does
  not transfer the audience; existing publisher/app ancestry remains the scope.
- General commerce outboxes redact canonical destination text and identifiers.
  No address is copied into a notification or delivered to an external provider.

The owned `POST /api/orders/{id}/fulfillment` endpoint takes the existing
`client_id`/`client_secret` body and requires the new explicit app credential scope
`orders.fulfillment.read`, present in both current app configuration and that
credential. It returns only order_id, status, item snapshots, address_snapshot and
captured_at for a paid/finished order belonging to that exact app. It does not
return account IDs, saved-contact IDs, payer wallet, arbitrary metadata or an
address book. This scope is separate from `orders.create`, `orders.status.manage`
and `contacts.read`; no old credential gains it automatically. A read-only
fulfillment credential cannot change status or settle funds. Account/app state,
revocation and credential expiry are checked again after the order wait.

Existing `PATCH /api/orders/{id}/status` keeps the original client-credential/status
body, requiring `orders.status.manage` for the exact app. Paid→Finished settles
its real canonical hold; unpaid/paid→Cancelled releases/refunds the same ledger
transaction under existing rules. Repeats do not duplicate settlement or events.
Original numerical statuses are 0 unpaid, 1 paid, 2 cancelled, 3 finished, 4 expired,
evidenced by retained authored callers. Finished means the merchant has declared
completion; it is not a claim that a carrier accepted, shipped or delivered a
parcel. There are no fabricated tracking numbers or external shipment successes.

## Migration and verification

Install `internal/signinpg/address_schema.sql` after canonical signin/contact
schema and `internal/commercepg/shipping_schema.sql` after commerce schema.
`signinpg.Schema` includes its contact revision addition. Constructors, requests
and readiness never run DDL. The disposable CI runner installs both explicitly.
The host readiness check requires them. Existing non-shipping orders need no
backfill. Original unsupported address-order records are not imported implicitly.
Preserve destination receipts and canonical contacts when downgrading; deleting
history is not an automatic rollback procedure.

Nine new actual-PG cases cover address CRUD/scope/account isolation, required and
foreign selectors, immutable price/destination snapshots, paid create/payment and
status replay, independent concurrent edit/delete versus checkout, exact account
and app credential waits, delayed secret expiry, limited merchant/app views,
rollback on wallet failure, database immutability guards, real composed host
contact/order/payment routes, and saved before/after restart state. Addresses,
accounts, app credentials and payment-policy fixtures are synthetic only.
The restart after phase only reads the saved random IDs and original typed
snapshot/economics; it never reseeds.

Build, vet, normal/race and source/SQL/wiring checks run offline independently.
Ordinary Go integration tests skip without the guarded DSN; no actual-PG success
is claimed here. The current two-group registry needs an independently reviewed
assignment refresh for the nine new cases. A deterministic draft and marker audit
are delivered separately with review_required intact; the full aggregate remains
blocked until that review. No workflow is activated or published by this slice.
