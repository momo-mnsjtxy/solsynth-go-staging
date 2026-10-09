-- SPDX-License-Identifier: AGPL-3.0-only
-- Owned projections, not a legacy migration. Explicit install only.
-- Domain audit rows are the authority. The namespaced digest gives stable UUID
-- serialization without copying records into a second ledger or inventing events.
CREATE OR REPLACE VIEW owned_account_action_history AS
 SELECT md5('owned_accounts_audit:'||id)::uuid AS id,actor_id AS account_id,action,target_id::text AS target,
 'owned_accounts_audit'::text AS source,id::text AS source_id,occurred_at AS created_at FROM owned_accounts_audit
 UNION ALL SELECT md5('owned_social_audit:'||id)::uuid,actor_id,action,target_id::text,'owned_social_audit',id::text,occurred_at FROM owned_social_audit
 UNION ALL SELECT md5('owned_progression_audit:'||id)::uuid,actor_id,action,target,'owned_progression_audit',id::text,occurred_at FROM owned_progression_audit
 UNION ALL SELECT md5('owned_authority_contact_audit:'||id)::uuid,account_id,action,contact_id::text,'owned_authority_contact_audit',id::text,occurred_at FROM owned_authority_contact_audit
 UNION ALL SELECT md5('owned_authority_factor_audit:'||id)::uuid,account_id,action,factor_id::text,'owned_authority_factor_audit',id::text,occurred_at FROM owned_authority_factor_audit;

CREATE INDEX IF NOT EXISTS owned_accounts_audit_history ON owned_accounts_audit(actor_id,occurred_at DESC,id);
CREATE INDEX IF NOT EXISTS owned_social_audit_history ON owned_social_audit(actor_id,occurred_at DESC,id);
CREATE INDEX IF NOT EXISTS owned_progression_audit_history ON owned_progression_audit(actor_id,occurred_at DESC,id);
CREATE INDEX IF NOT EXISTS owned_contact_audit_history ON owned_authority_contact_audit(account_id,occurred_at DESC,id);
CREATE INDEX IF NOT EXISTS owned_factor_audit_history ON owned_authority_factor_audit(account_id,occurred_at DESC,id);

-- Credit records are immutable and require an explicit owning policy/import
-- provenance. No grant, import, reward, or legacy balance is created on startup.
CREATE TABLE IF NOT EXISTS owned_account_credit_records (
 id uuid PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 reason_type varchar(1024) NOT NULL,reason varchar(1024) NOT NULL,
 delta double precision NOT NULL CHECK(delta>'-Infinity'::double precision AND delta<'Infinity'::double precision),
 created_at timestamptz NOT NULL,expired_at timestamptz,
 source_kind text NOT NULL CHECK(source_kind IN('owned-policy','verified-import')),
 source_ref text NOT NULL CHECK(length(source_ref) BETWEEN 1 AND 2048),
 source_record text NOT NULL CHECK(length(source_record) BETWEEN 1 AND 256),
 UNIQUE(account_id,source_kind,source_ref,source_record)
);
CREATE INDEX IF NOT EXISTS owned_account_credit_history ON owned_account_credit_records(account_id,created_at DESC,id);
CREATE OR REPLACE FUNCTION owned_credit_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION 'Credit provenance and records are immutable' USING ERRCODE='23000'; END;
$$;
DROP TRIGGER IF EXISTS owned_credit_immutable ON owned_account_credit_records;
CREATE TRIGGER owned_credit_immutable BEFORE UPDATE OR DELETE ON owned_account_credit_records FOR EACH ROW EXECUTE FUNCTION owned_credit_immutable();

-- Only established provider-link metadata belongs here. Auth sessions, OAuth
-- grants and provider credentials are not account connections. Provider linking,
-- verification and legacy import remain separate, unimplemented flows.
CREATE TABLE IF NOT EXISTS owned_account_connections (
 id uuid PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 provider varchar(128) NOT NULL CHECK(length(provider)>0),provided_identifier varchar(2048) NOT NULL CHECK(length(provided_identifier)>0),
 email varchar(320),username varchar(256),display_name varchar(256),
 is_public boolean NOT NULL DEFAULT false,last_used_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,verified_at timestamptz NOT NULL,revoked_at timestamptz,
 source_ref text NOT NULL CHECK(length(source_ref) BETWEEN 1 AND 2048),
 UNIQUE(account_id,provider,provided_identifier)
);
CREATE INDEX IF NOT EXISTS owned_account_connection_lookup ON owned_account_connections(account_id,provider,id);
