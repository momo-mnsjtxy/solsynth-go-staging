-- SPDX-License-Identifier: AGPL-3.0-only
-- Only management audit metadata. Merchant mappings, definitions and orders
-- remain in canonical commerce_v1; money remains in wallet_v1.
CREATE TABLE IF NOT EXISTS owned_merchant_management_audit(
 id bigserial PRIMARY KEY,actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 resource_key text NOT NULL,action text NOT NULL,occurred_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS commerce_v1.order_reversals(
 order_id uuid PRIMARY KEY REFERENCES commerce_v1.orders(id),transaction_id uuid NOT NULL UNIQUE REFERENCES wallet_v1.transactions(id),
 actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),created_at timestamptz NOT NULL
);
