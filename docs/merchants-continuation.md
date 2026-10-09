# Merchant configuration, catalog administration and order reporting

The owned runtime now has reviewable configuration APIs over its existing `commerce_v1.merchants` and `commerce_v1.definitions`, plus private order reporting. There is no new money ledger, balance-minting operation, payment provider, payout or settlement-success simulator.

## Evidence and installation

The authored MerchantController, SubscriptionAdminController, SubscriptionCatalogService, WALLET_ADMIN_API and MERCHANT_SETTLEMENT documentation are pinned to DysonNetwork `4ee2d0cebb9da8284a92483fee6dce795bf6947c`. `ports/wallet/merchants/source/manifest.json` records hashes and URLs, with its AGPL license. No excluded shared model, generated binding or other repository was read.

Install `internal/merchantpg/schema.sql` after authority, publisher/app, social, wallet and commerce schemas. It adds only `owned_merchant_management_audit`; configuration and orders remain in their canonical tables. Construct `merchantpg.New(db, guard)` and mount `merchants.NewHandler` with the existing post-style Auth adapter. `BindAuthority` is implemented; a nil guard fails closed.

## Routes and scope

| Route | Behavior |
|---|---|
| GET `/api/merchants/{publisher}` | Canonical publisher profile with app payout bindings |
| GET/PATCH `/api/merchants/{publisher}/apps/{app}/wallet` | Inspect, set or explicitly clear one existing app's payout binding |
| GET `/api/merchants/{publisher}/orders` | Publisher-ancestry order history, filtered before count/page |
| GET `/api/merchants/{publisher}/orders/report` | Exact order totals by currency, status and UTC day |
| GET `/api/merchants/me/orders` | Authenticated buyer's own canonical order history |
| GET `/api/merchants/me/orders/report` | Buyer-only aggregates |
| GET/POST `/api/admin/subscriptions/catalog` | Paged administrator catalog or upsert definition |
| GET/DELETE `/api/admin/subscriptions/catalog/{identifier}` | Administrator detail or disable definition |

Merchant reads require current publisher Manager/Owner membership. Binding mutation additionally requires `merchants.manage`; chosen wallets must match the canonical publisher account/realm, and the actor must own the account wallet or hold current canonical realm Owner membership. Omitting `wallet_id` is invalid; explicit null clears the binding. Existing order payee snapshots are preserved.

Catalog operations require `subscriptions.groups.manage`. Existing/new payout wallets must be managed by the actor. Optional `app_identifier` resolves to the existing canonical app and requires current publisher management rights, with matching wallet ownership. Persisted app identifiers are canonical `developer.app:<uuid>` values; reassignment of an existing definition's scope is rejected. System definitions without this field retain their existing behavior.

Supported catalog input matches the canonical commerce projection: identifier/group/display name/currency, exact base price, cycle days, payee wallet, optional app identifier/enabled state, allowed payment methods, renewal/gift flags and provider mappings. The evidenced `payment_policy` and bounded `gift_policy.allow_purchase` forms map into those existing fields. Complex perk/experience/reward/display policy fields are not silently accepted. Cycle days are 1–366; prices must be positive and exactly representable in canonical thousandths. Excess precision is rejected instead of silently truncated. Missing group defaults to identifier.

Delete disables the existing definition rather than deleting referenced history. It disappears from public checkout catalog and new purchases; prior subscription/order rows remain. No provider call occurs when configuration is edited.

## Authority, concurrency and shared seam

Actor proof and operation permissions are checked inside the same read-committed SQL transaction under the canonical authority account lock. Publisher management reuses the existing canonical publisher lock; app ancestry is re-read after publisher/project/app waits. Wallet ownership and realm authority are checked before committing configuration. Audit failure rolls back the entire operation. Concurrent catalog upserts serialize and converge on one existing canonical row.

Only three shared files change, captured precisely in `docs/merchants-shared-commerce.patch` against continuation `82afbbd`:

- `ports/wallet/commerce/model.go`: additive evidenced optional `AppIdentifier` field.
- `internal/commercepg/catalog_scope.go`: canonical optional app ancestry gate, retaining publisher → project → app shared locks and rechecking active/disabled ancestry after waits.
- `internal/commercepg/lifecycle.go`: scoped definition read/recheck and preflight of subscription/gift order definitions before order/wallet locks. Original system definitions with no app identifier continue to work.

This ordering matters: adding app checks only during final activation would acquire publisher locks after wallet locks and conflict with management. The preflight avoids that inversion. A scope change observed between the initial definition read and its lock fails with a conflict rather than authorizing an unvalidated scope. No package-wide overwrite is needed to integrate the patch.

## Reporting semantics

Reports deliberately identify their basis as `orders`; they do not label unpaid/paid/finished orders as merchant settlement records. No settlement table or payout result is invented. Settlement routes return 501.

Publisher scope follows immutable app→project→developer→publisher ancestry, including historical soft-deleted app rows. It never follows a shared or newly rebound payout wallet. Account scope uses the exact canonical buyer `actor_id`; no caller-provided alternate account is accepted. Order summaries omit payer wallet identity and private account authority fields.

Filters include status, currency, app ID, product identifier, bounded pagination and date range. Dates use inclusive `from`, exclusive `to`; a date-only `to` is expanded to the next UTC midnight. The default range is the previous 30 days and the maximum is 366 days. Count/page are computed in one SQL statement, including empty high-offset pages. Aggregate status/day buckets come from one database snapshot.

No binary float is used for money. SQL numeric totals are carried as JSON decimal numbers and combined with arbitrary-precision rational arithmetic. Totals are separated by currency, so unrelated units are never summed into one misleading amount. UTC daily grouping and exact values larger than JavaScript's integer precision are covered.

## Differences and gaps

- The legacy merchant is publisher-wide; the already-owned canonical runtime uses app→wallet mappings. The app-binding route makes that existing distinction explicit rather than creating a second merchant configuration store. Legacy publisher wallet-update and settlement-report shapes are not claimed.
- These are bounded owned admin definitions, not complete excluded Shared entity parity. Unknown complex reward/perk/fulfillment fields fail before writes. External provider mappings configure identifiers only; unavailable providers still fail through the existing adapter boundary.
- Public catalog discovery may still list an app-scoped definition whose app was subsequently retired; actual new subscription/gift operations and payment activation fail closed against current ancestry.
- Default date bounds and exact-thousandth rejection are deliberate owned API choices. Production migration, publisher-wide settlement accounting, payouts and global financial administration are out of scope.

## Verification

Focused exact-price/date/explicit-unlink tests pass with `-race`; all merchant integration cases compile. Actual PG execution is delegated to the guarded parent runner and is not implied by skipped tests.

`tests/integration/merchants_test.go` covers canonical binding/catalog consumption, foreign publisher/wallet denial, unchanged order snapshots, disabled catalog/history preservation, exact >2^53 totals, currency separation, shared-wallet publisher isolation, account history, exclusive date boundaries, concurrent upserts, failed audit rollback, stale/deleted/disabled app ancestry, admin scope waits and queued proof revocation. Fixtures derive report bounds and deadlines from their frozen commerce clock and use canonical UUID selectors with explicit policy grants.

Before phase: `TestPGMerchants*`. After a real PostgreSQL restart: `TestPGMerchantsRestartPersistence`; the after branch only reads existing binding, definition and audit rows. All fixtures are synthetic and use the existing strict disposable-cluster guard.
