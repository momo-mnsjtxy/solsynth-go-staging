-- SPDX-License-Identifier: AGPL-3.0-only
-- Only management audit metadata. Merchant mappings, definitions and orders
-- remain in canonical commerce_v1; money remains in wallet_v1.
CREATE TABLE IF NOT EXISTS owned_merchant_management_audit(
 id bigserial PRIMARY KEY,actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 resource_key text NOT NULL,action text NOT NULL,occurred_at timestamptz NOT NULL
);
