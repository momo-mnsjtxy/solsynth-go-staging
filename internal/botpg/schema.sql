-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit owned migration after canonical authority/account/app schemas.
-- No account, credential, permission grant, or quota is seeded by this migration.
CREATE UNIQUE INDEX IF NOT EXISTS owned_custom_apps_project_identity ON owned_custom_apps(id,project_id);
CREATE TABLE IF NOT EXISTS owned_bot_quotas (
 publisher_id uuid PRIMARY KEY REFERENCES owned_publishers(id),bots_total integer NOT NULL CHECK(bots_total BETWEEN 0 AND 10000),miniapps_total integer NOT NULL CHECK(miniapps_total BETWEEN 0 AND 10000),starts_at timestamptz NOT NULL,expires_at timestamptz
);
CREATE TABLE IF NOT EXISTS owned_bots (
 id uuid PRIMARY KEY,account_id uuid NOT NULL UNIQUE REFERENCES owned_authority_accounts(id),app_id uuid NOT NULL,project_id uuid NOT NULL,
 slug text NOT NULL CHECK(length(slug)BETWEEN 1 AND 128),is_active boolean NOT NULL DEFAULT true,
 details jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(details)='object'),controlled_disabled_version bigint,
 created_by uuid NOT NULL REFERENCES owned_authority_accounts(id),created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,deleted_at timestamptz,
 FOREIGN KEY(app_id,project_id) REFERENCES owned_custom_apps(id,project_id),CHECK(account_id<>id)
);
CREATE UNIQUE INDEX IF NOT EXISTS owned_bots_project_slug ON owned_bots(project_id,lower(slug));
CREATE INDEX IF NOT EXISTS owned_bots_app ON owned_bots(app_id,created_at,id);
CREATE TABLE IF NOT EXISTS owned_bot_chat (
 bot_id uuid PRIMARY KEY REFERENCES owned_bots(id),commands jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(commands)='array' AND jsonb_array_length(commands)<=100),
 auto_approve_dm boolean NOT NULL DEFAULT true,auto_approve_group_chat boolean NOT NULL DEFAULT false,support_chat boolean NOT NULL DEFAULT true,
 subscribed_events jsonb NOT NULL DEFAULT '["messages.new"]' CHECK(jsonb_typeof(subscribed_events)='array' AND jsonb_array_length(subscribed_events)<=32),
 created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS owned_bot_miniapps (
 id uuid PRIMARY KEY,app_id uuid NOT NULL,project_id uuid NOT NULL,slug text NOT NULL CHECK(length(slug)BETWEEN 2 AND 256),plugin_id text CHECK(length(plugin_id)<=256),
 stage integer NOT NULL CHECK(stage IN(0,1,2)),manifest jsonb,package_file_id uuid REFERENCES owned_media_files(id),package_fingerprint text,download_count bigint NOT NULL DEFAULT 0 CHECK(download_count>=0),created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,deleted_at timestamptz,
 FOREIGN KEY(app_id,project_id)REFERENCES owned_custom_apps(id,project_id),CHECK(manifest IS NULL OR jsonb_typeof(manifest)='object')
);
CREATE UNIQUE INDEX IF NOT EXISTS owned_bot_miniapps_slug ON owned_bot_miniapps(lower(slug));
CREATE UNIQUE INDEX IF NOT EXISTS owned_bot_miniapps_plugin ON owned_bot_miniapps(plugin_id)WHERE plugin_id IS NOT NULL;
CREATE TABLE IF NOT EXISTS owned_bot_audit(id bigserial PRIMARY KEY,actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),app_id uuid NOT NULL REFERENCES owned_custom_apps(id),action text NOT NULL,target_id uuid NOT NULL,occurred_at timestamptz NOT NULL);
