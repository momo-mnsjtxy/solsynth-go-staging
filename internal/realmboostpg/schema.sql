-- SPDX-License-Identifier: AGPL-3.0-only
-- Independent owned boost ledger, after authority/social/commerce schemas.
CREATE TABLE IF NOT EXISTS owned_realm_boost_purchases (
 id uuid PRIMARY KEY, order_id uuid NOT NULL UNIQUE REFERENCES commerce_v1.orders(id),
 realm_id uuid NOT NULL REFERENCES owned_realms(id), account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 treasury_account_id uuid NOT NULL REFERENCES owned_authority_accounts(id), treasury_wallet_id uuid NOT NULL REFERENCES wallet_v1.wallets(id),
 shares bigint NOT NULL CHECK(shares BETWEEN 1 AND 1000000), currency text NOT NULL CHECK(currency IN('golds','points')),
 amount numeric(32,3) NOT NULL CHECK(amount>0), created_at timestamptz NOT NULL,
 UNIQUE(id,realm_id,account_id,order_id),
 transaction_id uuid UNIQUE REFERENCES wallet_v1.transactions(id), fulfilled_at timestamptz, expires_at timestamptz,
 CHECK(amount=shares * CASE currency WHEN 'golds' THEN 1::numeric ELSE 1000::numeric END),
 CHECK((fulfilled_at IS NULL AND expires_at IS NULL AND transaction_id IS NULL) OR (fulfilled_at IS NOT NULL AND expires_at=fulfilled_at+interval '720 hours' AND transaction_id IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS owned_realm_boost_active ON owned_realm_boost_purchases(realm_id,expires_at) WHERE fulfilled_at IS NOT NULL;
CREATE TABLE IF NOT EXISTS owned_realm_boost_experience (
 id uuid PRIMARY KEY REFERENCES owned_realm_boost_purchases(id), realm_id uuid NOT NULL REFERENCES owned_realms(id),
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id), order_id uuid NOT NULL UNIQUE REFERENCES commerce_v1.orders(id),
 delta bigint NOT NULL CHECK(delta>0), perk_level integer NOT NULL CHECK(perk_level BETWEEN 0 AND 3), created_at timestamptz NOT NULL,
 FOREIGN KEY(id,realm_id,account_id,order_id) REFERENCES owned_realm_boost_purchases(id,realm_id,account_id,order_id)
);
CREATE TABLE IF NOT EXISTS owned_realm_boost_audit (
 id bigserial PRIMARY KEY, actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id), realm_id uuid NOT NULL REFERENCES owned_realms(id),
 order_id uuid NOT NULL REFERENCES commerce_v1.orders(id), action text NOT NULL CHECK(action IN('created','fulfilled')), occurred_at timestamptz NOT NULL,
 UNIQUE(order_id,action)
);
