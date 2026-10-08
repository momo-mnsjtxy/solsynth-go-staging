-- SPDX-License-Identifier: AGPL-3.0-only
CREATE TABLE IF NOT EXISTS owned_post_sponsorships(
 id uuid PRIMARY KEY,order_id uuid NOT NULL UNIQUE REFERENCES commerce_v1.orders(id),
 post_id uuid NOT NULL REFERENCES owned_text_posts(id),account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 publisher_id uuid NOT NULL REFERENCES owned_publishers(id),treasury_account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 treasury_wallet_id uuid NOT NULL REFERENCES wallet_v1.wallets(id),currency text NOT NULL CHECK(currency='golds'),
 amount numeric(32,3) NOT NULL CHECK(amount>=5),created_at timestamptz NOT NULL,
 transaction_id uuid UNIQUE REFERENCES wallet_v1.transactions(id),fulfilled_at timestamptz,expires_at timestamptz,
 CHECK((fulfilled_at IS NULL AND expires_at IS NULL AND transaction_id IS NULL) OR (fulfilled_at IS NOT NULL AND expires_at=fulfilled_at+interval '24 hours' AND transaction_id IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS owned_post_sponsorships_active ON owned_post_sponsorships(expires_at,post_id) WHERE fulfilled_at IS NOT NULL;
CREATE TABLE IF NOT EXISTS owned_post_sponsor_placements(
 id uuid PRIMARY KEY,post_id uuid NOT NULL REFERENCES owned_text_posts(id),total_amount numeric(32,3) NOT NULL CHECK(total_amount>=5),
 valid_from timestamptz NOT NULL UNIQUE,valid_until timestamptz NOT NULL,created_at timestamptz NOT NULL,
 CHECK(valid_until=valid_from+interval '1 hour')
);
CREATE TABLE IF NOT EXISTS owned_post_sponsor_impressions(
 post_id uuid PRIMARY KEY REFERENCES owned_text_posts(id),shown_count bigint NOT NULL CHECK(shown_count>=0),last_shown_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS owned_post_sponsor_audit(
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,actor_id uuid REFERENCES owned_authority_accounts(id),post_id uuid NOT NULL REFERENCES owned_text_posts(id),
 action text NOT NULL CHECK(action IN('created','fulfilled','placement')),order_id uuid REFERENCES commerce_v1.orders(id),occurred_at timestamptz NOT NULL
);
