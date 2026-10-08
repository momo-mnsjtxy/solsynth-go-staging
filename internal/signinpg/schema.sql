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
