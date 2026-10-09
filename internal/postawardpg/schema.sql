-- SPDX-License-Identifier: AGPL-3.0-only
CREATE TABLE IF NOT EXISTS owned_publisher_payout_wallets(
 publisher_id uuid PRIMARY KEY REFERENCES owned_publishers(id),wallet_id uuid REFERENCES wallet_v1.wallets(id),updated_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS owned_post_award_purchases(
 id uuid PRIMARY KEY,order_id uuid NOT NULL UNIQUE REFERENCES commerce_v1.orders(id),post_id uuid NOT NULL REFERENCES owned_text_posts(id),
 publisher_id uuid NOT NULL REFERENCES owned_publishers(id),account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 treasury_account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),treasury_wallet_id uuid NOT NULL REFERENCES wallet_v1.wallets(id),
 beneficiary_wallet_id uuid NOT NULL REFERENCES wallet_v1.wallets(id),beneficiary_account_id uuid REFERENCES owned_authority_accounts(id),beneficiary_realm_id uuid REFERENCES owned_realms(id),
 amount numeric(32,3) NOT NULL CHECK(amount>0),attitude integer NOT NULL CHECK(attitude IN(0,2)),message text CHECK(char_length(message)<=4096),
 policy text NOT NULL CHECK(policy IN('merchant_full_v1','documented_80_v1')),share_amount numeric(32,3) NOT NULL CHECK(share_amount>=0),
 created_at timestamptz NOT NULL,fulfilled_at timestamptz,
 CHECK(num_nonnulls(beneficiary_account_id,beneficiary_realm_id)=1),CHECK(treasury_wallet_id<>beneficiary_wallet_id),
 CHECK(share_amount=CASE WHEN attitude=2 THEN 0 WHEN policy='merchant_full_v1' THEN amount ELSE trunc(amount*0.8,3) END)
);
CREATE TABLE IF NOT EXISTS owned_post_award_receipts(
 purchase_id uuid PRIMARY KEY REFERENCES owned_post_award_purchases(id),transaction_id uuid NOT NULL UNIQUE REFERENCES wallet_v1.transactions(id),
 distribution_order_id uuid UNIQUE REFERENCES commerce_v1.orders(id),reserve_transaction_id uuid UNIQUE REFERENCES wallet_v1.transactions(id),
 due_at timestamptz NOT NULL,next_attempt_at timestamptz NOT NULL,attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 30),settled_at timestamptz,created_at timestamptz NOT NULL,
 CHECK((distribution_order_id IS NULL)=(reserve_transaction_id IS NULL))
);
CREATE INDEX IF NOT EXISTS owned_post_award_receipts_due ON owned_post_award_receipts(due_at,purchase_id) WHERE settled_at IS NULL AND distribution_order_id IS NOT NULL;
CREATE TABLE IF NOT EXISTS owned_post_award_audit(
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,actor_id uuid REFERENCES owned_authority_accounts(id),publisher_id uuid NOT NULL REFERENCES owned_publishers(id),
 post_id uuid REFERENCES owned_text_posts(id),order_id uuid REFERENCES commerce_v1.orders(id),action text NOT NULL CHECK(action IN('wallet','created','fulfilled','settled')),occurred_at timestamptz NOT NULL
);
