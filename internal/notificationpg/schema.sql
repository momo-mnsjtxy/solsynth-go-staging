-- SPDX-License-Identifier: AGPL-3.0-only
-- Owned inbox/outbox/replay schema. Explicit install after authority schema;
-- optional projectors additionally require walletpg and chatpg schemas.
CREATE SCHEMA IF NOT EXISTS notification_v1;
CREATE TABLE IF NOT EXISTS notification_v1.streams (
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id), app_id varchar(128) NOT NULL,
 sequence bigint NOT NULL DEFAULT 0 CHECK(sequence>=0), floor bigint NOT NULL DEFAULT 0 CHECK(floor>=0 AND floor<=sequence),
 PRIMARY KEY(account_id,app_id)
);
CREATE TABLE IF NOT EXISTS notification_v1.inbox (
 id uuid PRIMARY KEY, account_id uuid NOT NULL REFERENCES owned_authority_accounts(id), app_id varchar(128) NOT NULL,
 source_key varchar(256) NOT NULL, topic varchar(128) NOT NULL, title varchar(1024) NOT NULL,
 subtitle varchar(1024) NOT NULL, content varchar(8192) NOT NULL, meta jsonb NOT NULL CHECK(jsonb_typeof(meta)='object'),
 priority integer NOT NULL, viewed_at timestamptz, dismissed_at timestamptz,
 created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
 UNIQUE(account_id,app_id,source_key)
);
CREATE INDEX IF NOT EXISTS notification_inbox_page ON notification_v1.inbox(account_id,app_id,created_at DESC,id DESC) WHERE dismissed_at IS NULL;
CREATE INDEX IF NOT EXISTS notification_inbox_unread ON notification_v1.inbox(account_id,app_id) WHERE dismissed_at IS NULL AND viewed_at IS NULL;
CREATE TABLE IF NOT EXISTS notification_v1.outbox (
 id uuid PRIMARY KEY, account_id uuid NOT NULL REFERENCES owned_authority_accounts(id), app_id varchar(128) NOT NULL,
 source_key varchar(256) NOT NULL, fingerprint bytea NOT NULL CHECK(octet_length(fingerprint)=32),
 command jsonb NOT NULL CHECK(octet_length(command::text)<=65536), created_at timestamptz NOT NULL,
 processed_at timestamptz, UNIQUE(account_id,app_id,source_key)
);
CREATE INDEX IF NOT EXISTS notification_outbox_pending ON notification_v1.outbox(created_at,id) WHERE processed_at IS NULL;
CREATE TABLE IF NOT EXISTS notification_v1.events (
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id), app_id varchar(128) NOT NULL,
 sequence bigint NOT NULL CHECK(sequence>0), source_key varchar(256) NOT NULL,
 type varchar(128) NOT NULL, data jsonb NOT NULL CHECK(octet_length(data::text)<=65536),
 audience varchar(16) NOT NULL CHECK(audience IN('account','notification','chat')), audience_id uuid,
 created_at timestamptz NOT NULL,
 PRIMARY KEY(account_id,app_id,sequence), UNIQUE(account_id,app_id,source_key),
 FOREIGN KEY(account_id,app_id) REFERENCES notification_v1.streams(account_id,app_id),
 CHECK(audience='account' OR audience_id IS NOT NULL)
);
CREATE TABLE IF NOT EXISTS notification_v1.receipts (
 source varchar(32) NOT NULL, source_id uuid NOT NULL, app_id varchar(128) NOT NULL, processed_at timestamptz NOT NULL,
 PRIMARY KEY(source,source_id,app_id)
);
CREATE TABLE IF NOT EXISTS notification_v1.deliveries (
 notification_id uuid PRIMARY KEY REFERENCES notification_v1.inbox(id),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0), next_attempt_at timestamptz NOT NULL,
 lease_id uuid, lease_until timestamptz, delivered_at timestamptz, suppressed_at timestamptz,
 last_error varchar(64), CHECK((lease_id IS NULL)=(lease_until IS NULL))
);
CREATE INDEX IF NOT EXISTS notification_delivery_pending ON notification_v1.deliveries(next_attempt_at) WHERE delivered_at IS NULL AND suppressed_at IS NULL;
