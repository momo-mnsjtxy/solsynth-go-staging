-- SPDX-License-Identifier: AGPL-3.0-only
-- Synthetic test fixture derived from licensed DysonNetwork-owned board
-- migrations and current EF snapshot at 4ee2d0cebb9da8284a92483fee6dce795bf6947c.
-- No excluded protocol definitions used. Not an executable production migration.
CREATE TABLE account_board_items (
 id uuid NOT NULL,
 account_id uuid NOT NULL,
 "order" integer NOT NULL,
 kind integer NOT NULL,
 widget_key character varying(256) NULL,
 custom_app_id uuid NULL,
 custom_app_widget_key character varying(256) NULL,
 is_enabled boolean NOT NULL,
 payload jsonb NOT NULL,
 created_at timestamp with time zone NOT NULL,
 updated_at timestamp with time zone NOT NULL,
 deleted_at timestamp with time zone NULL,
 CONSTRAINT pk_account_board_items PRIMARY KEY (id)
);
CREATE UNIQUE INDEX ix_account_board_items_account_id_order
 ON account_board_items(account_id, "order");
CREATE INDEX ix_account_board_items_account_id_custom_app_id_custom_app_wid
 ON account_board_items(account_id, custom_app_id, custom_app_widget_key);
