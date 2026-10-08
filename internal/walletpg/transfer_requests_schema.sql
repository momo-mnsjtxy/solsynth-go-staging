-- SPDX-License-Identifier: AGPL-3.0-only
CREATE TABLE IF NOT EXISTS wallet_v1.transfer_requests(
 id uuid PRIMARY KEY,creator_account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 payee_wallet_id uuid NOT NULL REFERENCES wallet_v1.wallets(id),currency varchar(128) NOT NULL CHECK(length(currency)>0),amount numeric(32,3) NOT NULL CHECK(amount>0),remark varchar(4096),
 is_frozen boolean NOT NULL,require_confirmation boolean NOT NULL,status integer NOT NULL DEFAULT 0 CHECK(status BETWEEN 0 AND 2),
 created_at timestamptz NOT NULL,expires_at timestamptz NOT NULL CHECK(expires_at>created_at),fulfilled_at timestamptz,
 transaction_id uuid UNIQUE REFERENCES wallet_v1.transactions(id),
 CHECK((status=1 AND transaction_id IS NOT NULL AND fulfilled_at IS NOT NULL) OR(status<>1 AND transaction_id IS NULL AND fulfilled_at IS NULL))
);
CREATE INDEX IF NOT EXISTS wallet_transfer_requests_wallet ON wallet_v1.transfer_requests(payee_wallet_id,expires_at)WHERE status=0;
