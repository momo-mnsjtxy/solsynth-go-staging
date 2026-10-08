# Managed transaction index scope correction

The `wallet_commerce_order` unique index originally covered every non-null
`managed_by`. Commerce uses that field as an order ID and requires one transaction
per order. Funds use it as a fund ID across several distinct canonical receipts.
Consequently the second fund claim, contribution or refund failed with SQLSTATE
23505 before its transaction could commit. This was a runtime schema defect, not
a wallet balance, PIN, or fixture funding defect.

The base index now enforces uniqueness only for `purpose='commerce'`. The explicit
`order_index_scope.sql` migration constructs that replacement before removing the
old index, then preserves its original name. All three DDL operations are inside
one atomic DO statement; failure restores the old index. The private runner also
requires `--single-transaction` and installs it after the commerce base schema,
before host readiness or fixtures. No ledger data or balances are rewritten.

## Complete writer audit

There are exactly three canonical `wallet_v1.transactions` insert sites:

- `walletpg/transactions.go`: ordinary transfers omit `managed_by` and use the
  schema's `transfer` default. Transfer requests use this writer and retain their
  unique transaction reference, locked request state and idempotent fulfillment.
- `walletpg/commerce.go`: `CommerceHold` explicitly writes `purpose='commerce'`
  and the order ID. Orders, subscriptions, gifts, name cards, sponsorships, post
  awards and livestream awards use this path. Post/livestream distribution holds
  have their own distribution order IDs and also call CommerceHold. All remain
  protected by the same unique index and transaction/purchase receipt constraints.
- `walletpg/funds.go`: `newFundReceipt` explicitly writes `purpose='fund'` and a
  typed hold/claim/payout/refund. Multiple receipts per fund are intentional.
  Fund reservations and participants retain unique `(fund_id, account_id)` keys;
  backing/participant receipt references and allocation transaction IDs remain
  unique. Immutable backing records, current fund locks and allocation triggers
  enforce receipt linkage and total allocation not exceeding each reservation.

No writer changes purpose/managed ownership to bypass this distinction. The
existing purpose enum/check and managed-purpose check still reject arbitrary
purposes and managed ordinary transfers. Generic wallet transitions/expiry still
exclude all managed transactions. Conservation, fund replay and PIN checks are
unchanged. Subscription economics and publisher payout bindings are untouched.

## Regression coverage

`TestPGWalletManagedTransactionPurposeScopes` performs three distinct canonical
fund claims plus replays, verifies conservation, rejects a second commerce receipt
for the same order with the original unique-index name, and rejects invalid managed
purposes. `TestPGWalletManagedIndexLegacyMigrationAtomicity` applies the exact
migration to a synthetic legacy index, injects failure after DROP to verify rollback,
then verifies successful upgrade, fund multiplicity, commerce uniqueness and safe
reapplication. The synthetic table exists only in the guarded disposable database.

Local affected tests and integration compilation, vet, private-runner guard/wiring
checks, and PostgreSQL 16 SQL/PLpgSQL parsing pass. Actual database execution remains
for private CI; no local PostgreSQL socket was attempted.
