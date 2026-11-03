-- SPDX-License-Identifier: AGPL-3.0-only
ALTER TABLE owned_authority_challenges ADD COLUMN IF NOT EXISTS sudo_session_id uuid REFERENCES owned_authority_sessions(id);
ALTER TABLE owned_authority_challenges ADD COLUMN IF NOT EXISTS sudo_session_epoch bigint;
ALTER TABLE owned_authority_challenges ADD COLUMN IF NOT EXISTS sudo_policy_version bigint;
ALTER TABLE owned_authority_challenges ADD COLUMN IF NOT EXISTS sudo_email boolean NOT NULL DEFAULT false;
ALTER TABLE owned_authority_challenges DROP CONSTRAINT IF EXISTS owned_authority_challenges_purpose_check;
ALTER TABLE owned_authority_challenges ADD CONSTRAINT owned_authority_challenges_purpose_check CHECK(purpose IN('login','verify_email','reset_password','verify_contact','sudo'));
ALTER TABLE owned_authority_challenges DROP CONSTRAINT IF EXISTS owned_authority_challenges_sudo_binding;
ALTER TABLE owned_authority_challenges ADD CONSTRAINT owned_authority_challenges_sudo_binding CHECK((purpose='sudo' AND sudo_session_id IS NOT NULL AND sudo_session_epoch IS NOT NULL AND sudo_session_epoch>0 AND sudo_policy_version IS NOT NULL AND sudo_policy_version>0) OR(purpose<>'sudo' AND sudo_session_id IS NULL AND sudo_session_epoch IS NULL AND sudo_policy_version IS NULL AND NOT sudo_email));
CREATE TABLE IF NOT EXISTS owned_authority_sudo_email_codes(
 challenge_hash bytea PRIMARY KEY REFERENCES owned_authority_challenges(hash),
 code_hash bytea NOT NULL CHECK(octet_length(code_hash)=32),expires_at timestamptz NOT NULL,used boolean NOT NULL DEFAULT false
);
