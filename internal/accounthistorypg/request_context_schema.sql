-- SPDX-License-Identifier: AGPL-3.0-only
-- Associated HTTP facts for newly committed canonical audit records only.
CREATE TABLE IF NOT EXISTS owned_account_action_context (
 source text NOT NULL CHECK(source IN('owned_accounts_audit','owned_social_audit','owned_progression_audit','owned_authority_contact_audit','owned_authority_factor_audit')),
 source_id text NOT NULL CHECK(source_id~'^[1-9][0-9]{0,18}$'),account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),session_id uuid NOT NULL,
 ip_address inet,user_agent varchar(512),created_at timestamptz NOT NULL,
 PRIMARY KEY(source,source_id),FOREIGN KEY(account_id,session_id)REFERENCES owned_authority_sessions(account_id,id)
);
CREATE OR REPLACE FUNCTION owned_action_context_immutable()RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION 'Request audit facts are immutable' USING ERRCODE='23000';END;
$$;
DROP TRIGGER IF EXISTS owned_action_context_immutable ON owned_account_action_context;
CREATE TRIGGER owned_action_context_immutable BEFORE UPDATE OR DELETE ON owned_account_action_context FOR EACH ROW EXECUTE FUNCTION owned_action_context_immutable();
