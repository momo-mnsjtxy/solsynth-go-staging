-- SPDX-License-Identifier: AGPL-3.0-only
-- Owned opt-in coupon extension. Apply after commerce_v1 schema, once.
CREATE TABLE commerce_v1.coupons (
 id uuid PRIMARY KEY, app_id uuid NOT NULL REFERENCES owned_custom_apps(id),
 publisher_id uuid NOT NULL REFERENCES owned_publishers(id),
 payee_wallet_id uuid NOT NULL REFERENCES wallet_v1.wallets(id),
 identifier varchar(64) NOT NULL, enabled boolean NOT NULL, document jsonb NOT NULL,
 UNIQUE(app_id,identifier)
);
CREATE TABLE commerce_v1.coupon_redemptions (
 order_id uuid PRIMARY KEY REFERENCES commerce_v1.orders(id),
 coupon_id uuid NOT NULL REFERENCES commerce_v1.coupons(id),
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 gross numeric(32,3) NOT NULL CHECK(gross>0), discount numeric(32,3) NOT NULL CHECK(discount>0),
 net numeric(32,3) NOT NULL CHECK(net>0), currency varchar(128) NOT NULL,
 quote jsonb NOT NULL, redeemed_at timestamptz NOT NULL,
 CHECK(gross=discount+net)
);
CREATE INDEX coupon_redemptions_usage ON commerce_v1.coupon_redemptions(coupon_id,account_id);
