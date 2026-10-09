# Pending subscriptions and activation switching

This implements the next supported subscription billing flow: list purchased
subscriptions waiting for activation and choose which paid subscription in a
group starts now. It uses existing canonical definitions, subscriptions, account
authority, app consent and commerce outbox. It changes no money, order pricing,
wallet balance, provider account, coupon or purchase record. No new table,
migration, constructor side effect or external provider call is required.

## Coupon investigation and scope decision

The requested coupon flow could not be recovered from eligible retained evidence.
The search covered the retained Wallet operation controllers, SubscriptionService,
SubscriptionCatalogService, SubscriptionAdminController, the operation/remaining-
service inventory and acceptance map, and complete authored Solian payment and
stellar-program callers. `activation-source-manifest.json` pins the complete
relevant retained files and their hashes.

SubscriptionController.CreateSubscription and SubscriptionGiftController.PurchaseGift
accept a `coupon` string. SubscriptionService resolves that string against a
coupon ID or identifier and attaches the resulting object. Its actual discount
calculation delegates to `SnWalletSubscription.CalculateFinalPriceAt`; neither that
implementation nor an authored coupon model/issuance controller is retained in
the eligible source set. The retained code does not establish issuer authority,
app/account scope, discount formula, use limits, validity windows, reservation,
redemption or refund policy. Catalog/admin source and full authored payment
callers do not supply those missing contracts.

No such behavior is inferred from an unavailable SDK/model or a prohibited source.
The existing explicit unavailable result for coupons remains. Implementing an
invented promotion economy would not constitute source-supported completion.
The acceptance map already identifies remaining subscription billing views; its
activation-switch operation has a complete eligible controller/service/caller
chain, so this is the authorized fallback.

## Exact retained contracts

- `GET /api/subscriptions/pending-activations`, also exposed under the existing
  `/wallet/subscriptions/pending-activations` gateway, requires an authenticated
  account and accepts offset/take. The response is `total_count`,
  `next_activation_at`, `subscriptions`. As in the retained controller,
  `next_activation_at` is the first record on the requested page, or null.
- `POST /api/subscriptions/groups/{groupIdentifier}/activate`, also under the
  `/wallet` alias, accepts exactly `{"subscription_id":"<UUID>"}` and requires
  `subscriptions.groups.manage`. The permission spelling is retained in the
  authored permission documentation; the controller names the corresponding
  capability. The full stellar caller submits this route and body, then refreshes
  its subscription state.
- The switch response is the same current/next/history/catalog group projection
  already served by the owned backend.
- SubscriptionService.SwitchSubscriptionActivationAsync moves the selected paid
  grant first, the previous current grant's remaining duration second, and the
  remaining queue after that. It does not charge for the selection.

## Owned corrections and boundaries

The source has defaults and side effects which are not safe to infer as owned
contract. This implementation makes these corrections explicit:

- It uses a single transaction and the canonical account lock, then stable
  subscription row locks. Payment, cancellation and renewal for that account
  serialize with the switch. Scalar/document projections must agree.
- Only active, non-testing, unexpired paid grants belonging to the authenticated
  account and the requested stored group may move. Current definitions must
  still identify that same group and app scope. App consent and current app/
  product visibility are checked; a revoked grant cannot become usable through
  switching. Cross-account/group targets fail without changing any timeline.
- Invalid or overlapping timelines fail explicitly. The source's default of
  thirty days for missing/invalid duration is not used. Expired trailing rows
  are not requeued. This prevents elapsed or invented time from becoming a new
  entitlement.
- Remaining duration is calculated after lock waits. Moving a current grant
  keeps only its unconsumed interval; pending grants retain exactly their saved
  interval. Every grant appears once. Captured unit/final price, currency,
  purchase order, payer, cycle days and product terms remain unchanged.
- The existing renewal opt-in, payment method and allowed renewal policy govern
  the new renewal timestamp. Selecting a plan cannot restore an opt-out or
  authorize a provider charge. Auto-renewal policy itself is not expanded.
- Selecting the already-current grant is an inert replay. A real switch saves all
  rescheduled rows and one `commerce.subscription.activation_switched` event
  atomically. There is no repeated payment fulfillment, reward, badge, currency
  grant or assumed external event delivery.
- Groups/pending scans are bounded to 1,000 eligible candidate records. Larger
  sets fail unavailable instead of partially rescheduling. Sandbox grants remain
  separate and cannot be activated into ordinary access. Current authorization
  and consent are rechecked before commit.

Provider-generated subscriptions can have locally recorded purchased duration,
but this endpoint only rearranges that canonical local duration; it does not
cancel, amend or claim to synchronize external billing. Production provider
execution and coupon/redemption behavior remain explicitly unavailable.

## Verification

Pure tests verify exact remaining-duration conservation, repeated switches,
renewal opt-out, input immutability, invalid/overlapping intervals and strict
authored HTTP bodies/permission checks. The guarded PostgreSQL suite adds:

1. `TestPGSubscriptionActivationConservesPaidTimeAndMoney`
2. `TestPGSubscriptionActivationRollbackAndConcurrentReplay`
3. `TestPGSubscriptionActivationCurrentAuthorityAndIsolation`
4. `TestPGSubscriptionActivationRejectsUnpaidCancelledAndExpired`
5. `TestPGSubscriptionActivationRevokedAppConsent`
6. `TestPGBackendSubscriptionActivationRoutes`
7. `TestPGSubscriptionActivationRestartPersistence`

The final case is required before and after the actual database/process restart.
They cover partial elapsed time, untouched wallet charge count/balance, transactional
outbox rollback, concurrent replay, disabled authority during a lock wait, account/
group isolation, cancelled/unpaid/expired grants, revoked app consent, real host
routes and persisted timelines. All fixtures are synthetic and guarded by the
existing disposable-cluster checks. No local PostgreSQL/socket execution was
attempted; real transaction and restart validation belongs to private CI.

The restart case uses the shared `fixtureRestartPhase`, `saveRestartFixture`,
and `loadRestartFixture` helpers. Before saves exact account/group/subscription,
original purchase order and switch event IDs plus complete expected timelines
in an exclusive 0600 marker. After reads those exact records, validates timeline/
pricing/purchase identity and unchanged charge count/balance, and performs no setup
or database writes. The helper is an integration dependency supplied by the parent;
it is not duplicated in this patch.
