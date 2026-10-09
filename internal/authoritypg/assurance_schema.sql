-- SPDX-License-Identifier: AGPL-3.0-only
CREATE TABLE IF NOT EXISTS owned_authority_code_evidence(
 code_hash bytea PRIMARY KEY REFERENCES owned_authority_codes(hash),auth_time timestamptz NOT NULL,
 methods jsonb NOT NULL CHECK(jsonb_typeof(methods)='array' AND jsonb_array_length(methods) BETWEEN 1 AND 2 AND methods ? 'pwd' AND methods <@ '["pwd","otp","recovery"]'::jsonb AND NOT(methods ? 'otp' AND methods ? 'recovery'))
);
CREATE TABLE IF NOT EXISTS owned_authority_credential_assurances(
 session_id uuid PRIMARY KEY REFERENCES owned_authority_sessions(id),account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 code_hash bytea NOT NULL UNIQUE REFERENCES owned_authority_code_evidence(code_hash),account_version bigint NOT NULL CHECK(account_version>0),session_epoch bigint NOT NULL CHECK(session_epoch>0),
 auth_time timestamptz NOT NULL,expires_at timestamptz NOT NULL,methods jsonb NOT NULL CHECK(jsonb_typeof(methods)='array'),
 consumed_at timestamptz,action text,target_id uuid,policy_version bigint,config_id text,
 CHECK(expires_at=auth_time+interval '5 minutes'),
 CHECK((consumed_at IS NULL AND action IS NULL AND target_id IS NULL AND policy_version IS NULL AND config_id IS NULL)OR(consumed_at IS NOT NULL AND action IS NOT NULL AND action IN('create','enable','disable','delete') AND target_id IS NOT NULL AND policy_version IS NOT NULL AND policy_version>=0 AND config_id IS NOT NULL AND length(config_id)<=64))
);
CREATE INDEX IF NOT EXISTS owned_authority_assurance_account ON owned_authority_credential_assurances(account_id,expires_at);
