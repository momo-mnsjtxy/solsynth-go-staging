-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit owned migration after canonical authority/account/social schemas.
CREATE TABLE IF NOT EXISTS owned_presence_preferences (
 account_id uuid PRIMARY KEY REFERENCES owned_authority_accounts(id),
 online_visibility text NOT NULL DEFAULT 'public' CHECK(online_visibility IN('public','friends','private')),
 last_seen_visibility text NOT NULL DEFAULT 'friends' CHECK(last_seen_visibility IN('public','friends','private')),
 status_visibility text NOT NULL DEFAULT 'public' CHECK(status_visibility IN('public','friends','private')),
 updated_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS owned_presence_last_seen (
 account_id uuid PRIMARY KEY REFERENCES owned_authority_accounts(id), last_seen_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS owned_presence_sessions (
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),session_id uuid PRIMARY KEY,
 epoch bigint NOT NULL,account_version bigint NOT NULL,last_seen_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,idle_since timestamptz,
 FOREIGN KEY(account_id,session_id) REFERENCES owned_authority_sessions(account_id,id),
 CHECK(expires_at>=last_seen_at),CHECK(expires_at<=last_seen_at+interval '120 seconds'),CHECK(idle_since IS NULL OR idle_since<=last_seen_at)
);
CREATE INDEX IF NOT EXISTS owned_presence_live ON owned_presence_sessions(account_id,expires_at);
CREATE TABLE IF NOT EXISTS owned_presence_statuses (
 id uuid PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),attitude integer NOT NULL CHECK(attitude BETWEEN -100 AND 100),
 type integer NOT NULL CHECK(type BETWEEN 0 AND 3),label text CHECK(length(label)<=1024),symbol text CHECK(length(symbol)<=128),
 app_identifier text CHECK(length(app_identifier)<=4096),is_automated boolean NOT NULL DEFAULT false,
 meta jsonb,cleared_at timestamptz,created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,
 CHECK(meta IS NULL OR jsonb_typeof(meta)='object'),CHECK(NOT is_automated OR length(app_identifier)>0)
);
CREATE INDEX IF NOT EXISTS owned_presence_status_account ON owned_presence_statuses(account_id,created_at DESC,id);
-- One durable, authenticated activity event per observed local calendar day.
CREATE TABLE IF NOT EXISTS owned_presence_activity_days (
 id uuid PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),day date NOT NULL,occurred_at timestamptz NOT NULL,
 UNIQUE(account_id,day)
);
