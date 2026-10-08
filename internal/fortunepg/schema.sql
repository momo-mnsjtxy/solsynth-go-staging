-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit owned migration after canonical authority/accounts/progression.
CREATE TABLE IF NOT EXISTS owned_fortune_corpora (
 revision text PRIMARY KEY CHECK(length(revision) BETWEEN 1 AND 64),sha256 char(64) NOT NULL,
 definition jsonb NOT NULL CHECK(jsonb_typeof(definition)='object' AND octet_length(definition::text)<=2097152),created_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS owned_fortune_config (
 id integer PRIMARY KEY CHECK(id=1),corpus_revision text NOT NULL REFERENCES owned_fortune_corpora(revision),updated_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS owned_fortune_requests (
 checkin_id uuid PRIMARY KEY REFERENCES owned_progression_checkins(id),
 request_data jsonb NOT NULL CHECK(jsonb_typeof(request_data)='object' AND octet_length(request_data::text)<=8192),
 state text NOT NULL CHECK(state IN('pending','processing','ready','failed','cancelled')),
 provider_revision text CHECK(length(provider_revision)<=100),attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 100),
 not_before timestamptz NOT NULL,lease_id uuid,lease_until timestamptz,
 generation jsonb CHECK(generation IS NULL OR (jsonb_typeof(generation)='object' AND octet_length(generation::text)<=65536)),
 error_code text CHECK(error_code IN('provider_failed','invalid_response','account_unavailable','provider_changed')),
 created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,completed_at timestamptz,
 CHECK((state='processing')=(lease_id IS NOT NULL AND lease_until IS NOT NULL)),
 CHECK((state='ready')=(generation IS NOT NULL AND completed_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS owned_fortune_due ON owned_fortune_requests(not_before,created_at,checkin_id)WHERE state IN('pending','processing');
