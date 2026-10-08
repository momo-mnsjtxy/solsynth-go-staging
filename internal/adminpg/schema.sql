-- SPDX-License-Identifier: AGPL-3.0-only
-- Apply explicitly after canonical account/social and moderation migrations.
CREATE TABLE IF NOT EXISTS owned_admin_extension_audit(
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 kind text NOT NULL CHECK(kind IN('account','publisher','realm','automod')),target_id uuid NOT NULL,
 action text NOT NULL CHECK(length(action)BETWEEN 1 AND 128),detail jsonb NOT NULL CHECK(jsonb_typeof(detail)='object'AND octet_length(detail::text)<=65536),occurred_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS owned_automod_state(singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),generation bigint NOT NULL DEFAULT 0 CHECK(generation>=0));
INSERT INTO owned_automod_state(singleton)VALUES(true)ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS owned_automod_rules(
 id uuid PRIMARY KEY,name text NOT NULL CHECK(length(name)BETWEEN 1 AND 256),description text CHECK(length(description)<=1024),
 type integer NOT NULL CHECK(type IN(1,2)),default_action integer NOT NULL CHECK(default_action IN(1,2,3)),
 pattern text NOT NULL CHECK(length(pattern)BETWEEN 1 AND 4096),is_regex boolean NOT NULL,derank_weight integer NOT NULL CHECK(derank_weight BETWEEN 0 AND 10000),
 is_enabled boolean NOT NULL,priority integer NOT NULL CHECK(priority BETWEEN -1000000 AND 1000000),expires_at timestamptz,
 created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,deleted_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS owned_automod_name ON owned_automod_rules(lower(name))WHERE deleted_at IS NULL;
