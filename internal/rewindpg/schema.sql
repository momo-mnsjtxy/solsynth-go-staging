-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit migration after canonical authority and report-source domains.
CREATE TABLE IF NOT EXISTS owned_account_rewind_points(
 id uuid PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),year integer NOT NULL CHECK(year BETWEEN 1901 AND 2199),
 schema_version integer NOT NULL CHECK(schema_version=1),snapshot jsonb NOT NULL CHECK(jsonb_typeof(snapshot)='object' AND octet_length(snapshot::text)<=131072),
 sharable_code varchar(32) UNIQUE,created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,
 UNIQUE(account_id,year),CHECK(sharable_code IS NULL OR sharable_code ~ '^[A-Za-z0-9_-]{32}$')
);
CREATE TABLE IF NOT EXISTS owned_account_rewind_audit(id bigserial PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),point_id uuid NOT NULL REFERENCES owned_account_rewind_points(id),action text NOT NULL,occurred_at timestamptz NOT NULL);
