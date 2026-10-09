-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit owned migration after authority, accounts, social, wallet and commerce.
CREATE TABLE IF NOT EXISTS owned_name_card_purchases (
 id uuid PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 order_id uuid NOT NULL UNIQUE REFERENCES commerce_v1.orders(id),amount numeric(32,3) NOT NULL CHECK(amount>0),
 points_currency varchar(128) NOT NULL,payee_wallet_id uuid NOT NULL REFERENCES wallet_v1.wallets(id),
 fulfilled_at timestamptz,consumed_at timestamptz,target_type varchar(32),target_id uuid,old_name varchar(256),new_name varchar(256),
 created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,
 CHECK((consumed_at IS NULL AND target_type IS NULL AND target_id IS NULL AND old_name IS NULL AND new_name IS NULL) OR
 (consumed_at IS NOT NULL AND fulfilled_at IS NOT NULL AND target_type IN('account','realm','publisher') AND old_name IS NOT NULL AND new_name IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS owned_name_cards_account ON owned_name_card_purchases(account_id,created_at,id);
CREATE TABLE IF NOT EXISTS owned_name_card_receipts (
 order_id uuid PRIMARY KEY REFERENCES commerce_v1.orders(id),purchase_id uuid NOT NULL UNIQUE REFERENCES owned_name_card_purchases(id),
 transaction_id uuid NOT NULL UNIQUE REFERENCES wallet_v1.transactions(id),fulfilled_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS owned_name_card_audit (
 id bigserial PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),purchase_id uuid NOT NULL REFERENCES owned_name_card_purchases(id),action text NOT NULL,occurred_at timestamptz NOT NULL
);
