-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit owned tables, installed after canonical authority/presence/media.
CREATE TABLE IF NOT EXISTS owned_activity_catalog (
 id uuid PRIMARY KEY, account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 provider text NOT NULL DEFAULT '', catalog_key varchar(1024) NOT NULL,
 name text, document jsonb NOT NULL CHECK(jsonb_typeof(document)='object'),
 visibility integer NOT NULL CHECK(visibility BETWEEN 0 AND 2),
 total_seconds bigint NOT NULL DEFAULT 0 CHECK(total_seconds>=0),
 session_count bigint NOT NULL DEFAULT 0 CHECK(session_count>=0),last_active_at timestamptz,
 created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,deleted_at timestamptz,
 UNIQUE(account_id,id),UNIQUE(account_id,provider,catalog_key)
);
CREATE TABLE IF NOT EXISTS owned_presence_activities (
 id uuid PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 manual_id varchar(4096),catalog_id uuid,document jsonb NOT NULL CHECK(jsonb_typeof(document)='object'),
 visibility integer NOT NULL CHECK(visibility BETWEEN 0 AND 2),
 session_id uuid NOT NULL,account_version bigint NOT NULL,session_epoch bigint NOT NULL,
 lease_minutes integer NOT NULL CHECK(lease_minutes BETWEEN 1 AND 60),lease_expires_at timestamptz NOT NULL,
 started_at timestamptz,ended_at timestamptz,created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,deleted_at timestamptz,
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 FOREIGN KEY(account_id,session_id) REFERENCES owned_authority_sessions(account_id,id),
 FOREIGN KEY(account_id,catalog_id) REFERENCES owned_activity_catalog(account_id,id),
 CHECK(ended_at IS NULL OR started_at IS NOT NULL AND ended_at>=started_at),
 CHECK(lease_expires_at<=updated_at+interval '60 minutes')
);
CREATE INDEX IF NOT EXISTS owned_activity_manual ON owned_presence_activities(account_id,manual_id,lease_expires_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS owned_activity_history ON owned_presence_activities(account_id,created_at DESC,id);
CREATE TABLE IF NOT EXISTS owned_activity_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 activity_id uuid NOT NULL REFERENCES owned_presence_activities(id),revision bigint NOT NULL,
 action text NOT NULL CHECK(action IN('set','start','update','end','delete')),
 occurred_at timestamptz NOT NULL,UNIQUE(activity_id,revision)
);
