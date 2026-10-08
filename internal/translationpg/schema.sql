-- SPDX-License-Identifier: AGPL-3.0-only
-- Account-scoped owned cache; source text and provider credentials are never stored.
CREATE TABLE IF NOT EXISTS owned_translation_cache(
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 cache_key text NOT NULL CHECK(cache_key~'^[a-f0-9]{64}$'),
 result text NOT NULL CHECK(length(result)>0 AND octet_length(result)<=32000),
 created_at timestamptz NOT NULL,expires_at timestamptz NOT NULL CHECK(expires_at>created_at),
 PRIMARY KEY(account_id,cache_key)
);
CREATE INDEX IF NOT EXISTS owned_translation_cache_expiry ON owned_translation_cache(account_id,expires_at);
