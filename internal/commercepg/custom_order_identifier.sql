-- SPDX-License-Identifier: AGPL-3.0-only
-- Apply explicitly after commercepg/schema.sql, before serving custom orders.
-- Only widen the known canonical column; never truncate or rewrite order data.
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='commerce_v1' AND table_name='orders' AND column_name='product_identifier' AND data_type='character varying' AND character_maximum_length IN(256,1024)) THEN
  RAISE EXCEPTION 'Unexpected commerce order identifier schema';
 END IF;
 ALTER TABLE commerce_v1.orders ALTER COLUMN product_identifier TYPE varchar(1024);
END $$;
