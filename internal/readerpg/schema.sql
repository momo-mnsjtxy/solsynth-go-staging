-- SPDX-License-Identifier: AGPL-3.0-only
CREATE TABLE IF NOT EXISTS owned_reader_cache_state (
    singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
    generation bigint NOT NULL DEFAULT 0 CHECK(generation>=0)
);
INSERT INTO owned_reader_cache_state(singleton) VALUES(true) ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS owned_reader_cache (
    cache_key text PRIMARY KEY CHECK(cache_key~'^[a-f0-9]{64}$'),
    url text NOT NULL CHECK(length(url) BETWEEN 1 AND 4096),
    preview jsonb NOT NULL CHECK(jsonb_typeof(preview)='object' AND octet_length(preview::text)<=65536),
    updated_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL CHECK(expires_at>updated_at)
);
CREATE INDEX IF NOT EXISTS owned_reader_cache_expiry ON owned_reader_cache(expires_at);
CREATE TABLE IF NOT EXISTS owned_reader_cache_audit (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
    action text NOT NULL CHECK(action IN('invalidate_url','invalidate_all')),
    cache_key text CHECK(cache_key IS NULL OR cache_key~'^[a-f0-9]{64}$'),
    generation bigint NOT NULL CHECK(generation>0),
    occurred_at timestamptz NOT NULL,
    CHECK((action='invalidate_url')=(cache_key IS NOT NULL))
);
