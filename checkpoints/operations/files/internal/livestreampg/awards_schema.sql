-- SPDX-License-Identifier: AGPL-3.0-only
-- Apply after canonical authority/accounts/wallet/commerce and livestream runs.
CREATE TABLE IF NOT EXISTS owned_livestream_award_purchases(
 id uuid PRIMARY KEY,order_id uuid NOT NULL UNIQUE REFERENCES commerce_v1.orders(id),
 stream_id uuid NOT NULL REFERENCES owned_livestreams(id),generation bigint NOT NULL,
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),owner_id uuid NOT NULL REFERENCES owned_authority_accounts(id),owner_version bigint NOT NULL,
 beneficiary_wallet_id uuid NOT NULL REFERENCES wallet_v1.wallets(id),beneficiary_account_id uuid REFERENCES owned_authority_accounts(id),beneficiary_realm_id uuid REFERENCES owned_realms(id),treasury_account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),treasury_wallet_id uuid NOT NULL REFERENCES wallet_v1.wallets(id),
 currency varchar(128) NOT NULL,amount numeric(32,3) NOT NULL CHECK(amount>0),share_amount numeric(32,3) NOT NULL CHECK(share_amount>0 AND share_amount=amount*0.9),
 message varchar(4096),sender_name varchar(128),created_at timestamptz NOT NULL,fulfilled_at timestamptz,
 FOREIGN KEY(stream_id,generation)REFERENCES owned_livestream_runs(stream_id,generation),CHECK(num_nonnulls(beneficiary_account_id,beneficiary_realm_id)=1),CHECK(beneficiary_wallet_id<>treasury_wallet_id)
);
CREATE INDEX IF NOT EXISTS owned_livestream_award_purchase_actor ON owned_livestream_award_purchases(stream_id,account_id,created_at,id);
CREATE INDEX IF NOT EXISTS owned_livestream_award_history ON owned_livestream_award_purchases(stream_id,fulfilled_at DESC,id) WHERE fulfilled_at IS NOT NULL;
CREATE TABLE IF NOT EXISTS owned_livestream_award_receipts(
 purchase_id uuid PRIMARY KEY REFERENCES owned_livestream_award_purchases(id),order_id uuid NOT NULL UNIQUE REFERENCES commerce_v1.orders(id),transaction_id uuid NOT NULL UNIQUE REFERENCES wallet_v1.transactions(id),
 distribution_order_id uuid NOT NULL UNIQUE REFERENCES commerce_v1.orders(id),reserve_transaction_id uuid NOT NULL UNIQUE REFERENCES wallet_v1.transactions(id),
 fulfilled_at timestamptz NOT NULL,payout_state text NOT NULL DEFAULT 'held' CHECK(payout_state IN('held','released','refund_pending','refund_ready','refunded')),released_at timestamptz,
 refund_requested_by uuid REFERENCES owned_authority_accounts(id),refund_session_id uuid REFERENCES owned_authority_sessions(id),refund_account_version bigint,refund_session_epoch bigint,
 refund_reason varchar(256),refund_transaction_id uuid UNIQUE REFERENCES wallet_v1.transactions(id),refund_requested_at timestamptz,refunded_at timestamptz,
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),next_attempt_at timestamptz NOT NULL,reason text,
 CHECK((payout_state IN('refund_pending','refund_ready','refunded'))=(refund_requested_by IS NOT NULL AND refund_session_id IS NOT NULL AND refund_account_version IS NOT NULL AND refund_session_epoch IS NOT NULL AND refund_reason IS NOT NULL AND refund_requested_at IS NOT NULL)),
 CHECK((payout_state='refunded')=(refund_transaction_id IS NOT NULL AND refunded_at IS NOT NULL)),
 CHECK(payout_state<>'released' OR released_at IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS owned_livestream_award_payout_due ON owned_livestream_award_receipts(next_attempt_at,purchase_id) WHERE payout_state IN('held','refund_pending','refund_ready');
-- Exactly one backed distribution order may spend a settled award receipt.
CREATE UNIQUE INDEX IF NOT EXISTS owned_livestream_award_distribution_source ON commerce_v1.orders((document->'meta'->>'source_order_id')) WHERE app_id IS NULL AND product_identifier='livestreams.award.distribution';
-- Realtime awards reuse the durable, ordered and privacy-rechecked data outbox.
-- This block is a no-op when chat delivery is not installed.
DO $$BEGIN
 IF to_regclass('owned_livestream_chat_outbox') IS NOT NULL THEN
  ALTER TABLE owned_livestream_chat_outbox DROP CONSTRAINT IF EXISTS owned_livestream_chat_outbox_permission_check;
  ALTER TABLE owned_livestream_chat_outbox ADD CONSTRAINT owned_livestream_chat_outbox_permission_check CHECK(permission IN('livestreams.chat.send','livestreams.chat.moderate','livestreams.awards'));
  ALTER TABLE owned_livestream_chat_outbox DROP CONSTRAINT IF EXISTS owned_livestream_chat_outbox_kind_check;
  ALTER TABLE owned_livestream_chat_outbox ADD CONSTRAINT owned_livestream_chat_outbox_kind_check CHECK(kind IN('message','timeout','award'));
 END IF;
END$$;
