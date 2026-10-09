-- SPDX-License-Identifier: AGPL-3.0-only
-- Owned mapping: 0 tracking, 1 registration invite. Not a Shared enum claim.
CREATE TABLE IF NOT EXISTS owned_affiliation_spells (
 id uuid PRIMARY KEY, account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 spell text NOT NULL UNIQUE CHECK(length(spell) BETWEEN 1 AND 1024), type integer NOT NULL CHECK(type IN(0,1)),
 max_usages integer CHECK(max_usages>0), skip_tests boolean NOT NULL,
 created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL, expires_at timestamptz, affected_at timestamptz, revoked_at timestamptz,
 CHECK(type=0 OR max_usages IS NOT NULL), CHECK(expires_at IS NULL OR expires_at>created_at)
);
CREATE INDEX IF NOT EXISTS owned_affiliation_spells_owner ON owned_affiliation_spells(account_id,created_at,id);
CREATE TABLE IF NOT EXISTS owned_affiliation_results (
 id uuid PRIMARY KEY,spell_id uuid NOT NULL REFERENCES owned_affiliation_spells(id),resource_identifier text NOT NULL CHECK(length(resource_identifier) BETWEEN 1 AND 8192),
 recipient_id uuid UNIQUE REFERENCES owned_authority_accounts(id),created_at timestamptz NOT NULL,
 CHECK(recipient_id IS NULL OR resource_identifier='account:'||recipient_id::text),UNIQUE(spell_id,recipient_id)
);
CREATE INDEX IF NOT EXISTS owned_affiliation_results_spell ON owned_affiliation_results(spell_id,created_at,id);
CREATE TABLE IF NOT EXISTS owned_affiliation_audit (
 id bigserial PRIMARY KEY,actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),spell_id uuid NOT NULL REFERENCES owned_affiliation_spells(id),operation text NOT NULL,occurred_at timestamptz NOT NULL
);
