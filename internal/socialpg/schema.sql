-- SPDX-License-Identifier: AGPL-3.0-only
-- Independent owned schema. Explicit installation after authoritypg and accountpg.
CREATE TABLE IF NOT EXISTS owned_social_relationships (
 id uuid PRIMARY KEY, account_id uuid NOT NULL REFERENCES owned_authority_accounts(id), related_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 status integer NOT NULL CHECK(status IN(-100,100,200)),alias text CHECK(length(alias)<=128),
 created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,
 CHECK(account_id<>related_id),UNIQUE(account_id,related_id)
);
CREATE TABLE IF NOT EXISTS owned_social_requests (
 id uuid PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),related_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 created_at timestamptz NOT NULL,CHECK(account_id<>related_id),UNIQUE(account_id,related_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS owned_social_requests_pair ON owned_social_requests(least(account_id,related_id),greatest(account_id,related_id));
CREATE TABLE IF NOT EXISTS owned_realms (
 id uuid PRIMARY KEY,slug text NOT NULL CHECK(length(slug) BETWEEN 1 AND 256),name text NOT NULL CHECK(length(name) BETWEEN 1 AND 1024),description text NOT NULL DEFAULT '' CHECK(length(description)<=4096),
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),is_public boolean NOT NULL DEFAULT false,is_community boolean NOT NULL DEFAULT false,
 boost_level integer NOT NULL DEFAULT 0 CHECK(boost_level>=0),created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,deleted_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS owned_realms_slug ON owned_realms(lower(slug));
CREATE TABLE IF NOT EXISTS owned_realm_members (
 id uuid PRIMARY KEY,realm_id uuid NOT NULL REFERENCES owned_realms(id),account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),role integer NOT NULL CHECK(role IN(0,50,100)),
 invited_by uuid REFERENCES owned_authority_accounts(id),joined_at timestamptz,leave_at timestamptz,created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,
 UNIQUE(realm_id,account_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS owned_realm_owner ON owned_realm_members(realm_id) WHERE role=100 AND leave_at IS NULL;
CREATE TABLE IF NOT EXISTS owned_realm_quota(account_id uuid PRIMARY KEY REFERENCES owned_authority_accounts(id),total bigint NOT NULL CHECK(total>=0),starts_at timestamptz NOT NULL,expires_at timestamptz);
CREATE TABLE IF NOT EXISTS owned_social_audit(id bigserial PRIMARY KEY,actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),action text NOT NULL,target_id uuid NOT NULL,occurred_at timestamptz NOT NULL);
