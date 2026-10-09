-- SPDX-License-Identifier: AGPL-3.0-only
-- Independent owned schema. Apply after authoritypg.Schema; never auto-applied.
CREATE UNIQUE INDEX IF NOT EXISTS owned_authority_accounts_folded_name ON owned_authority_accounts(lower(name));
CREATE TABLE IF NOT EXISTS owned_accounts_profiles(
 account_id uuid PRIMARY KEY REFERENCES owned_authority_accounts(id), id uuid NOT NULL UNIQUE,
 bio text NOT NULL DEFAULT '' CHECK(length(bio)<=4096),pronouns text NOT NULL DEFAULT '' CHECK(length(pronouns)<=128),
 location text NOT NULL DEFAULT '' CHECK(length(location)<=256),time_zone text NOT NULL DEFAULT '' CHECK(length(time_zone)<=128),
 links jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(links)='array'), updated_at timestamptz NOT NULL
);
ALTER TABLE owned_accounts_profiles ADD COLUMN IF NOT EXISTS first_name text NOT NULL DEFAULT '' CHECK(length(first_name)<=128);
ALTER TABLE owned_accounts_profiles ADD COLUMN IF NOT EXISTS middle_name text NOT NULL DEFAULT '' CHECK(length(middle_name)<=128);
ALTER TABLE owned_accounts_profiles ADD COLUMN IF NOT EXISTS last_name text NOT NULL DEFAULT '' CHECK(length(last_name)<=128);
ALTER TABLE owned_accounts_profiles ADD COLUMN IF NOT EXISTS gender text NOT NULL DEFAULT '' CHECK(length(gender)<=128);
ALTER TABLE owned_accounts_profiles ADD COLUMN IF NOT EXISTS birthday timestamptz;
CREATE TABLE IF NOT EXISTS owned_publishers(
 id uuid PRIMARY KEY,name text NOT NULL CHECK(length(name) BETWEEN 1 AND 256),nick text NOT NULL CHECK(length(nick) BETWEEN 1 AND 256),bio text NOT NULL DEFAULT '' CHECK(length(bio)<=4096),
 type integer NOT NULL CHECK(type IN(0,1)),account_id uuid REFERENCES owned_authority_accounts(id),realm_id uuid,
 created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,deleted_at timestamptz,
 CHECK((type=0 AND account_id IS NOT NULL AND realm_id IS NULL) OR(type=1 AND account_id IS NULL AND realm_id IS NOT NULL))
);
-- Names remain reserved across soft deletion to prevent identity takeover.
CREATE UNIQUE INDEX IF NOT EXISTS owned_publishers_folded_name ON owned_publishers(lower(name));
CREATE TABLE IF NOT EXISTS owned_publisher_members(
 id uuid PRIMARY KEY,publisher_id uuid NOT NULL REFERENCES owned_publishers(id),account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 role integer NOT NULL CHECK(role IN(0,50,100,200)),joined_at timestamptz,created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,revoked_at timestamptz,
 UNIQUE(publisher_id,account_id)
);
CREATE INDEX IF NOT EXISTS owned_publisher_members_account ON owned_publisher_members(account_id,publisher_id) WHERE revoked_at IS NULL;
CREATE TABLE IF NOT EXISTS owned_publishing_settings(account_id uuid PRIMARY KEY REFERENCES owned_authority_accounts(id),default_publisher_id uuid NOT NULL REFERENCES owned_publishers(id));
CREATE TABLE IF NOT EXISTS owned_publisher_quota(account_id uuid PRIMARY KEY REFERENCES owned_authority_accounts(id),total bigint NOT NULL CHECK(total>=0),starts_at timestamptz NOT NULL,expires_at timestamptz);
CREATE TABLE IF NOT EXISTS owned_publisher_entitlements(publisher_id uuid PRIMARY KEY REFERENCES owned_publishers(id),protected_tag_total bigint NOT NULL CHECK(protected_tag_total>=0),level integer NOT NULL CHECK(level>=0),perk_level integer NOT NULL CHECK(perk_level>=0),starts_at timestamptz NOT NULL,expires_at timestamptz);
CREATE TABLE IF NOT EXISTS owned_accounts_audit(id bigserial PRIMARY KEY,actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),action text NOT NULL,target_id uuid NOT NULL,occurred_at timestamptz NOT NULL);
ALTER TABLE owned_publishers ADD COLUMN IF NOT EXISTS shadowban_reason text;
ALTER TABLE owned_publishers ADD COLUMN IF NOT EXISTS shadowbanned_at timestamptz;

ALTER TABLE owned_accounts_profiles ADD COLUMN IF NOT EXISTS verification jsonb CHECK(verification IS NULL OR(jsonb_typeof(verification)='object' AND octet_length(verification::text)<=65536));

ALTER TABLE owned_publishers ADD COLUMN IF NOT EXISTS verification jsonb CHECK(verification IS NULL OR(jsonb_typeof(verification)='object' AND octet_length(verification::text)<=65536));

-- Posting keeps the established internal column name. New nullable choices are
-- additive; repeat installation never rewrites an existing choice or identity.
ALTER TABLE owned_publishing_settings ALTER COLUMN default_publisher_id DROP NOT NULL;
ALTER TABLE owned_publishing_settings ADD COLUMN IF NOT EXISTS id uuid NOT NULL DEFAULT gen_random_uuid();
CREATE UNIQUE INDEX IF NOT EXISTS owned_publishing_settings_id ON owned_publishing_settings(id);
ALTER TABLE owned_publishing_settings ADD COLUMN IF NOT EXISTS default_reply_publisher_id uuid REFERENCES owned_publishers(id);
ALTER TABLE owned_publishing_settings ADD COLUMN IF NOT EXISTS default_fediverse_publisher_id uuid REFERENCES owned_publishers(id);
ALTER TABLE owned_publishing_settings ADD COLUMN IF NOT EXISTS created_at timestamptz NOT NULL DEFAULT clock_timestamp();
ALTER TABLE owned_publishing_settings ADD COLUMN IF NOT EXISTS updated_at timestamptz;
