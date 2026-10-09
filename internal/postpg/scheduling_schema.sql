-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit install after canonical post, authority-session, publisher and realm schemas.
-- Pending posts remain canonical drafts until the guarded due transition commits.
CREATE TABLE IF NOT EXISTS owned_post_schedules (
 id bigserial PRIMARY KEY,
 post_id uuid NOT NULL REFERENCES owned_text_posts(id),
 actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 publisher_id uuid NOT NULL REFERENCES owned_publishers(id),
 session_id uuid NOT NULL REFERENCES owned_authority_sessions(id),
 account_version bigint NOT NULL CHECK(account_version>0),
 authorization_epoch bigint NOT NULL CHECK(authorization_epoch>0),
 due_at timestamptz NOT NULL,
 state text NOT NULL CHECK(state IN('pending','published','cancelled','blocked')),
 failure text,
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 post_audit_id bigint NOT NULL,
 policy_stamp jsonb NOT NULL CHECK(jsonb_typeof(policy_stamp)='object'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 finished_at timestamptz,
 CHECK((state='pending')=(finished_at IS NULL)),
 CHECK((state='blocked')=(failure IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS owned_post_schedules_pending ON owned_post_schedules(post_id) WHERE state='pending';
CREATE INDEX IF NOT EXISTS owned_post_schedules_due ON owned_post_schedules(due_at,id) WHERE state='pending';
CREATE TABLE IF NOT EXISTS owned_post_schedule_events (
 id bigserial PRIMARY KEY,
 schedule_id bigint NOT NULL REFERENCES owned_post_schedules(id),
 revision bigint NOT NULL CHECK(revision>0),
 post_id uuid NOT NULL REFERENCES owned_text_posts(id),
 actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 kind text NOT NULL CHECK(kind IN('scheduled','edited','published','cancelled','blocked')),
 occurred_at timestamptz NOT NULL,
 UNIQUE(schedule_id,revision,kind)
);
