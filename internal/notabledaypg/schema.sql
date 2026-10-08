-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit owned schema after authority and social schemas. No runtime seeding,
-- notifications, external calendar connector, or duplicate identity authority.
CREATE TABLE IF NOT EXISTS owned_notable_days (
 id uuid PRIMARY KEY,definition jsonb NOT NULL CHECK(jsonb_typeof(definition)='object' AND octet_length(definition::text)<=65536),
 region varchar(8) NOT NULL,start_date timestamptz NOT NULL,end_date timestamptz NOT NULL,is_recurring boolean NOT NULL,display_order integer,
 created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,deleted_at timestamptz,CHECK(end_date>start_date)
);
CREATE INDEX IF NOT EXISTS owned_notable_region ON owned_notable_days(lower(region),start_date,id)WHERE deleted_at IS NULL;
CREATE TABLE IF NOT EXISTS owned_calendar_events (
 id uuid PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),definition jsonb NOT NULL CHECK(jsonb_typeof(definition)='object' AND octet_length(definition::text)<=65536),
 start_time timestamptz NOT NULL,end_time timestamptz NOT NULL,visibility integer NOT NULL CHECK(visibility IN(0,100,200)),is_recurring boolean NOT NULL,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,deleted_at timestamptz,
 request_key text,request_hash char(64),UNIQUE(account_id,request_key),CHECK(end_time>start_time)
);
CREATE INDEX IF NOT EXISTS owned_calendar_owner_time ON owned_calendar_events(account_id,start_time,id)WHERE deleted_at IS NULL;
CREATE TABLE IF NOT EXISTS owned_calendar_subscriptions (
 id uuid PRIMARY KEY,subscriber_id uuid NOT NULL REFERENCES owned_authority_accounts(id),target_account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),created_at timestamptz NOT NULL,
 UNIQUE(subscriber_id,target_account_id),CHECK(subscriber_id<>target_account_id)
);
CREATE INDEX IF NOT EXISTS owned_calendar_subscribers ON owned_calendar_subscriptions(target_account_id,subscriber_id);
CREATE TABLE IF NOT EXISTS owned_calendar_audit(id bigserial PRIMARY KEY,actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),operation text NOT NULL,resource_id uuid NOT NULL,occurred_at timestamptz NOT NULL);
