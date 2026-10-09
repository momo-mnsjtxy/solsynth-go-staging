-- SPDX-License-Identifier: AGPL-3.0-only
-- Apply after canonical authority, signin credentials and versioned app schema.
CREATE TABLE IF NOT EXISTS owned_oauth_requests(
 id uuid PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),parent_id uuid NOT NULL REFERENCES owned_authority_sessions(id),app_id uuid NOT NULL REFERENCES owned_custom_apps(id),
 state_hash bytea NOT NULL CHECK(octet_length(state_hash)=32),request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),app_epoch bigint NOT NULL,owner_id uuid REFERENCES owned_authority_accounts(id),owner_version bigint NOT NULL,
 created_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,decided_at timestamptz,
 UNIQUE(account_id,parent_id,app_id,state_hash)
);
CREATE INDEX IF NOT EXISTS owned_oauth_requests_expiry ON owned_oauth_requests(expires_at,id);
CREATE TABLE IF NOT EXISTS owned_oauth_codes(
 hash bytea PRIMARY KEY CHECK(octet_length(hash)=32),request_id uuid NOT NULL UNIQUE REFERENCES owned_oauth_requests(id) ON DELETE CASCADE,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),parent_id uuid NOT NULL REFERENCES owned_authority_sessions(id),app_id uuid NOT NULL REFERENCES owned_custom_apps(id),
 client_id text NOT NULL,redirect_uri text NOT NULL,challenge text NOT NULL,nonce text NOT NULL,scopes jsonb NOT NULL CHECK(jsonb_typeof(scopes)='array'),app_epoch bigint NOT NULL,owner_id uuid REFERENCES owned_authority_accounts(id),owner_version bigint NOT NULL,consent_version bigint NOT NULL,
 created_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,used_at timestamptz,session_id uuid REFERENCES owned_authority_sessions(id)
);
CREATE TABLE IF NOT EXISTS owned_oauth_grants(
 session_id uuid PRIMARY KEY REFERENCES owned_authority_sessions(id),account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),parent_id uuid NOT NULL REFERENCES owned_authority_sessions(id),app_id uuid NOT NULL REFERENCES owned_custom_apps(id),
 client_id text NOT NULL,app_epoch bigint NOT NULL,owner_id uuid REFERENCES owned_authority_accounts(id),owner_version bigint NOT NULL,consent_version bigint NOT NULL,secret_id uuid REFERENCES owned_app_secrets(id),secret_hash bytea,
 scopes jsonb NOT NULL CHECK(jsonb_typeof(scopes)='array'),nonce text NOT NULL,created_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,revoked_at timestamptz,
 CHECK((secret_id IS NULL AND secret_hash IS NULL) OR(secret_id IS NOT NULL AND octet_length(secret_hash)=32))
);
CREATE INDEX IF NOT EXISTS owned_oauth_grants_account_app ON owned_oauth_grants(account_id,app_id);
CREATE INDEX IF NOT EXISTS owned_oauth_grants_expiry ON owned_oauth_grants(expires_at,session_id);
CREATE INDEX IF NOT EXISTS owned_oauth_grants_revoked ON owned_oauth_grants(revoked_at,session_id) WHERE revoked_at IS NOT NULL;
CREATE TABLE IF NOT EXISTS owned_oauth_refresh(
 hash bytea PRIMARY KEY CHECK(octet_length(hash)=32),session_id uuid NOT NULL REFERENCES owned_oauth_grants(session_id) ON DELETE CASCADE,epoch bigint NOT NULL CHECK(epoch>0),created_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,used_at timestamptz
);
CREATE INDEX IF NOT EXISTS owned_oauth_refresh_session ON owned_oauth_refresh(session_id,epoch);
CREATE TABLE IF NOT EXISTS owned_oauth_audit(id bigserial PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),app_id uuid NOT NULL REFERENCES owned_custom_apps(id),action text NOT NULL,occurred_at timestamptz NOT NULL);
