-- SPDX-License-Identifier: AGPL-3.0-only
-- Receipt metadata only: canonical sessions, factors and credential assurance
-- remain the sole identity and secret authorities. Install after continuity.
ALTER TABLE owned_authority_factors ADD COLUMN IF NOT EXISTS revision bigint NOT NULL DEFAULT 1 CHECK(revision>0);
CREATE TABLE IF NOT EXISTS owned_authority_factor_setups(
 account_id uuid PRIMARY KEY REFERENCES owned_authority_accounts(id),session_id uuid NOT NULL REFERENCES owned_authority_sessions(id),
 account_version bigint NOT NULL CHECK(account_version>0),authorization_epoch bigint NOT NULL CHECK(authorization_epoch>0),
 auth_time timestamptz NOT NULL,expires_at timestamptz NOT NULL,policy_hash text NOT NULL CHECK(length(policy_hash)=64),
 recovery_id uuid,recovery_revision bigint,totp_id uuid,totp_revision bigint,
 state text NOT NULL CHECK(state IN('recovery','pending','complete','cancelled','disabled')),
 FOREIGN KEY(account_id,session_id) REFERENCES owned_authority_sessions(account_id,id),
 CHECK(expires_at=auth_time+interval '5 minutes'),
 CHECK((recovery_id IS NULL)=(recovery_revision IS NULL)),CHECK((totp_id IS NULL)=(totp_revision IS NULL)),
 CHECK(state<>'recovery' OR (recovery_id IS NOT NULL AND totp_id IS NULL)),CHECK(state<>'pending' OR totp_id IS NOT NULL)
);
CREATE TABLE IF NOT EXISTS owned_authority_pending_enrollments(
 factor_id uuid PRIMARY KEY,account_id uuid NOT NULL UNIQUE REFERENCES owned_authority_accounts(id),
 factor_revision bigint NOT NULL CHECK(factor_revision>0),account_version bigint NOT NULL CHECK(account_version>0),
 policy_hash text NOT NULL CHECK(length(policy_hash)=64),pending_until timestamptz NOT NULL,
 valid boolean NOT NULL DEFAULT true
);
CREATE INDEX IF NOT EXISTS owned_authority_pending_enrollment_account ON owned_authority_pending_enrollments(account_id);
