-- SPDX-License-Identifier: AGPL-3.0-only
-- Owned credentials extend canonical authority accounts; no duplicate identity.
CREATE TABLE IF NOT EXISTS owned_authority_credentials (
 account_id uuid PRIMARY KEY REFERENCES owned_authority_accounts(id),
 email text NOT NULL UNIQUE CHECK(email=lower(email)), verified_at timestamptz
);
CREATE TABLE IF NOT EXISTS owned_authority_factors (
 id uuid PRIMARY KEY, account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 type integer NOT NULL CHECK(type IN(0,3,5)), created_at timestamptz NOT NULL,
 enabled_at timestamptz, secret bytea NOT NULL, last_counter bigint NOT NULL DEFAULT -1,
 pending_until timestamptz, UNIQUE(account_id,type), CHECK(octet_length(secret)>0)
);
CREATE TABLE IF NOT EXISTS owned_authority_challenges (
 hash bytea PRIMARY KEY CHECK(octet_length(hash)=32), account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 account_version bigint NOT NULL CHECK(account_version>0), purpose text NOT NULL CHECK(purpose IN('login','verify_email','reset_password')),
 created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, expires_at timestamptz NOT NULL,
 device_id text NOT NULL DEFAULT '', device_name text NOT NULL DEFAULT '', platform integer NOT NULL DEFAULT 0,
 ip_address text NOT NULL DEFAULT '', user_agent text NOT NULL DEFAULT '', failed_attempts integer NOT NULL DEFAULT 0 CHECK(failed_attempts>=0),
 password_ok boolean NOT NULL DEFAULT false, second_ok boolean NOT NULL DEFAULT false,
 require_second boolean NOT NULL DEFAULT false, completed boolean NOT NULL DEFAULT false,
 blacklist jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(blacklist)='array')
);
CREATE INDEX IF NOT EXISTS owned_authority_challenges_account ON owned_authority_challenges(account_id,purpose,expires_at);
CREATE TABLE IF NOT EXISTS owned_authority_attempts (
 bucket bytea PRIMARY KEY CHECK(octet_length(bucket)=32), attempts integer NOT NULL CHECK(attempts>0), window_end timestamptz NOT NULL
);
-- Primary contact metadata references credentials without duplicating its email
-- or verification state. Only secondary contacts carry their own content.
CREATE TABLE IF NOT EXISTS owned_authority_contacts (
 id uuid PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),type integer NOT NULL CHECK(type BETWEEN 0 AND 2),
 content text,credential_account_id uuid UNIQUE REFERENCES owned_authority_credentials(account_id),verified_at timestamptz,is_public boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,deleted_at timestamptz,
 CHECK((credential_account_id IS NOT NULL AND credential_account_id=account_id AND type=0 AND content IS NULL AND verified_at IS NULL AND deleted_at IS NULL)OR(credential_account_id IS NULL AND content IS NOT NULL AND length(content) BETWEEN 1 AND 2048)),
 CHECK(type=0 OR verified_at IS NULL),CHECK(type<>0 OR content IS NULL OR content=lower(content))
);
CREATE UNIQUE INDEX IF NOT EXISTS owned_contact_account_content ON owned_authority_contacts(account_id,type,content)WHERE deleted_at IS NULL AND content IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS owned_contact_verified_email ON owned_authority_contacts(content)WHERE type=0 AND verified_at IS NOT NULL AND deleted_at IS NULL;
INSERT INTO owned_authority_contacts(id,account_id,type,credential_account_id,created_at,updated_at)
 SELECT c.account_id,c.account_id,0,c.account_id,a.created_at,a.created_at FROM owned_authority_credentials c JOIN owned_authority_accounts a ON a.id=c.account_id
 WHERE NOT EXISTS(SELECT 1 FROM owned_authority_contacts m WHERE m.credential_account_id=c.account_id) ON CONFLICT DO NOTHING;
ALTER TABLE owned_authority_challenges ADD COLUMN IF NOT EXISTS contact_id uuid REFERENCES owned_authority_contacts(id);
ALTER TABLE owned_authority_challenges DROP CONSTRAINT IF EXISTS owned_authority_challenges_purpose_check;
ALTER TABLE owned_authority_challenges ADD CONSTRAINT owned_authority_challenges_purpose_check CHECK(purpose IN('login','verify_email','reset_password','verify_contact'));
CREATE TABLE IF NOT EXISTS owned_authority_contact_audit(id bigserial PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),contact_id uuid NOT NULL REFERENCES owned_authority_contacts(id),action text NOT NULL,occurred_at timestamptz NOT NULL);
