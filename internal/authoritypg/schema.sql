-- SPDX-License-Identifier: AGPL-3.0-only
-- Independently authored PostgreSQL schema, not a legacy schema/import.
CREATE TABLE IF NOT EXISTS owned_authority_accounts (
 id uuid PRIMARY KEY, name text NOT NULL UNIQUE, nick text NOT NULL,
 language text NOT NULL DEFAULT '', region text NOT NULL DEFAULT '',
 is_superuser boolean NOT NULL DEFAULT false, disabled boolean NOT NULL DEFAULT false,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0), created_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS owned_authority_sessions (
 id uuid PRIMARY KEY, account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 parent_id uuid, label text NOT NULL, type integer NOT NULL CHECK(type IN(0,1)), client_id text NOT NULL,
 created_at timestamptz NOT NULL, last_granted_at timestamptz NOT NULL, category text NOT NULL,
 trusted boolean NOT NULL DEFAULT false, epoch bigint NOT NULL CHECK(epoch>0), account_version bigint NOT NULL CHECK(account_version>0),
 expires_at timestamptz NOT NULL, revoked_at timestamptz, scopes jsonb NOT NULL CHECK(jsonb_typeof(scopes)='array'),
 refresh_hash bytea UNIQUE, previous_hash bytea UNIQUE, previous_until timestamptz, sealed_pair bytea,
 CHECK((type=0 AND client_id='') OR (type=1 AND client_id<>'')),
 CHECK(refresh_hash IS NULL OR octet_length(refresh_hash)=32), CHECK(previous_hash IS NULL OR octet_length(previous_hash)=32),
 UNIQUE(account_id,id), FOREIGN KEY(account_id,parent_id) REFERENCES owned_authority_sessions(account_id,id)
);
CREATE INDEX IF NOT EXISTS owned_authority_sessions_account ON owned_authority_sessions(account_id,created_at,id);
CREATE TABLE IF NOT EXISTS owned_authority_grants (
 id bigserial PRIMARY KEY, account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 pattern text NOT NULL, deny boolean NOT NULL DEFAULT false, starts_at timestamptz, expires_at timestamptz
);
CREATE TABLE IF NOT EXISTS owned_authority_groups (id uuid PRIMARY KEY, name text NOT NULL UNIQUE);
CREATE TABLE IF NOT EXISTS owned_authority_group_members (account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),group_id uuid NOT NULL REFERENCES owned_authority_groups(id),PRIMARY KEY(account_id,group_id));
CREATE TABLE IF NOT EXISTS owned_authority_group_grants (id bigserial PRIMARY KEY,group_id uuid NOT NULL REFERENCES owned_authority_groups(id),pattern text NOT NULL,deny boolean NOT NULL DEFAULT false,starts_at timestamptz,expires_at timestamptz);
CREATE TABLE IF NOT EXISTS owned_authority_punishments (
 id bigserial PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 kind text NOT NULL CHECK(kind IN('block_login','disable_account','permission_modification')), pattern text NOT NULL DEFAULT '*',starts_at timestamptz,expires_at timestamptz
);
CREATE TABLE IF NOT EXISTS owned_authority_codes (
 hash bytea PRIMARY KEY CHECK(octet_length(hash)=32),account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 account_version bigint NOT NULL CHECK(account_version>0),expires_at timestamptz NOT NULL,used boolean NOT NULL DEFAULT false,label text NOT NULL,scopes jsonb NOT NULL CHECK(jsonb_typeof(scopes)='array')
);
