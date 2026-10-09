-- SPDX-License-Identifier: AGPL-3.0-only
-- Apply after the canonical wallet schema.
CREATE TABLE IF NOT EXISTS owned_affiliation_purchases(
 spell_id uuid PRIMARY KEY REFERENCES owned_affiliation_spells(id),
 transaction_id uuid NOT NULL UNIQUE REFERENCES wallet_v1.transactions(id),
 actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 idempotency_key varchar(128) NOT NULL,
 currency varchar(128) NOT NULL,
 amount numeric(32,3) NOT NULL CHECK(amount>0),
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 created_at timestamptz NOT NULL,
 UNIQUE(actor_id,idempotency_key)
);
