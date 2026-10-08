-- SPDX-License-Identifier: AGPL-3.0-only
ALTER TABLE wallet_v1.payment_policies ADD COLUMN IF NOT EXISTS version bigint NOT NULL DEFAULT 1 CHECK(version>0);
ALTER TABLE wallet_v1.payment_policies ADD COLUMN IF NOT EXISTS factor_id uuid UNIQUE;
ALTER TABLE wallet_v1.payment_policies DROP CONSTRAINT IF EXISTS wallet_payment_policy_factor_fk;
ALTER TABLE wallet_v1.payment_policies ADD CONSTRAINT wallet_payment_policy_factor_fk FOREIGN KEY(factor_id) REFERENCES owned_authority_factors(id);
CREATE TABLE IF NOT EXISTS wallet_v1.payment_authorizations(
 hash bytea PRIMARY KEY CHECK(octet_length(hash)=32),account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 session_id uuid NOT NULL REFERENCES owned_authority_sessions(id),session_epoch bigint NOT NULL CHECK(session_epoch>0),account_version bigint NOT NULL CHECK(account_version>0),
 policy_version bigint NOT NULL CHECK(policy_version>0),purpose text NOT NULL CHECK(purpose IN('wallet.transfer','commerce.order.pay')),
 binding text NOT NULL CHECK(length(binding)=64),created_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,consumed_at timestamptz,
 CHECK(expires_at>created_at AND expires_at<=created_at+interval '2 minutes')
);
CREATE INDEX IF NOT EXISTS wallet_payment_authorizations_account ON wallet_v1.payment_authorizations(account_id,expires_at);
