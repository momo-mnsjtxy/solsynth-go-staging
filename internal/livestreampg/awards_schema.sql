-- SPDX-License-Identifier: AGPL-3.0-only
-- Apply after canonical authority/accounts/wallet/commerce and livestream runs.
CREATE TABLE IF NOT EXISTS owned_livestream_award_purchases(
 id uuid PRIMARY KEY,order_id uuid NOT NULL UNIQUE REFERENCES commerce_v1.orders(id),
 stream_id uuid NOT NULL REFERENCES owned_livestreams(id),generation bigint NOT NULL,
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),owner_id uuid NOT NULL REFERENCES owned_authority_accounts(id),owner_version bigint NOT NULL,
 beneficiary_wallet_id uuid NOT NULL REFERENCES wallet_v1.wallets(id),treasury_account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),treasury_wallet_id uuid NOT NULL REFERENCES wallet_v1.wallets(id),
 currency varchar(128) NOT NULL,amount numeric(32,3) NOT NULL CHECK(amount>0),share_amount numeric(32,3) NOT NULL CHECK(share_amount>0 AND share_amount=amount*0.9),
 message varchar(4096),sender_name varchar(128),created_at timestamptz NOT NULL,fulfilled_at timestamptz,
 FOREIGN KEY(stream_id,generation)REFERENCES owned_livestream_runs(stream_id,generation),CHECK(beneficiary_wallet_id<>treasury_wallet_id)
);
CREATE INDEX IF NOT EXISTS owned_livestream_award_purchase_actor ON owned_livestream_award_purchases(stream_id,account_id,created_at,id);
CREATE INDEX IF NOT EXISTS owned_livestream_award_history ON owned_livestream_award_purchases(stream_id,fulfilled_at DESC,id) WHERE fulfilled_at IS NOT NULL;
CREATE TABLE IF NOT EXISTS owned_livestream_award_receipts(
 purchase_id uuid PRIMARY KEY REFERENCES owned_livestream_award_purchases(id),order_id uuid NOT NULL UNIQUE REFERENCES commerce_v1.orders(id),transaction_id uuid NOT NULL UNIQUE REFERENCES wallet_v1.transactions(id),
 distribution_order_id uuid NOT NULL UNIQUE REFERENCES commerce_v1.orders(id),reserve_transaction_id uuid NOT NULL UNIQUE REFERENCES wallet_v1.transactions(id),
 fulfilled_at timestamptz NOT NULL,payout_state text NOT NULL DEFAULT 'held' CHECK(payout_state IN('held','released')),released_at timestamptz,
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),next_attempt_at timestamptz NOT NULL,reason text
);
CREATE INDEX IF NOT EXISTS owned_livestream_award_payout_due ON owned_livestream_award_receipts(next_attempt_at,purchase_id) WHERE payout_state='held';
-- Exactly one backed distribution order may spend a settled award receipt.
CREATE UNIQUE INDEX IF NOT EXISTS owned_livestream_award_distribution_source ON commerce_v1.orders((document->'meta'->>'source_order_id')) WHERE app_id IS NULL AND product_identifier='livestreams.award.distribution';
