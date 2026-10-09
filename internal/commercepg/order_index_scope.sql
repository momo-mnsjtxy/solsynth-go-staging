-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit migration after the canonical wallet and commerce schemas.
-- The replacement is built before the old index is removed. The DO statement
-- is atomic even when invoked without psql --single-transaction; a failure
-- retains the old index. No money or transaction rows are changed.
DO $$ BEGIN
 EXECUTE 'CREATE UNIQUE INDEX wallet_commerce_order_scoped ON wallet_v1.transactions(managed_by) WHERE purpose=''commerce'' AND managed_by IS NOT NULL';
 EXECUTE 'DROP INDEX IF EXISTS wallet_v1.wallet_commerce_order';
 EXECUTE 'ALTER INDEX wallet_v1.wallet_commerce_order_scoped RENAME TO wallet_commerce_order';
END $$;
