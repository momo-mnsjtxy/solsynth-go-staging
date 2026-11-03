-- SPDX-License-Identifier: AGPL-3.0-only
-- Only a completed, session-bound sudo challenge may install this elevation.
ALTER TABLE owned_authority_accounts ADD COLUMN IF NOT EXISTS factor_policy_version bigint NOT NULL DEFAULT 1 CHECK(factor_policy_version>0);
CREATE TABLE IF NOT EXISTS owned_authority_sudo_elevations(
 session_id uuid PRIMARY KEY REFERENCES owned_authority_sessions(id),
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 factor_policy_version bigint NOT NULL CHECK(factor_policy_version>0),
 account_version bigint NOT NULL CHECK(account_version>0),session_epoch bigint NOT NULL CHECK(session_epoch>0),
 challenge_hash bytea NOT NULL UNIQUE CHECK(octet_length(challenge_hash)=32),
 auth_time timestamptz NOT NULL,expires_at timestamptz NOT NULL,
 methods jsonb NOT NULL CHECK(jsonb_typeof(methods)='array' AND jsonb_array_length(methods)=2 AND methods ? 'pwd' AND (methods ? 'otp' OR methods ? 'email' OR methods ? 'recovery') AND methods <@ '["pwd","otp","email","recovery"]'::jsonb),
 trust_points integer NOT NULL CHECK(trust_points>=2),CHECK(expires_at=auth_time+interval '5 minutes')
);
