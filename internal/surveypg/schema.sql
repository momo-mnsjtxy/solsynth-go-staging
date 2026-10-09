-- SPDX-License-Identifier: AGPL-3.0-only
-- Owned schema; apply after authority, accounts, posts and media. No legacy schema claim.
CREATE TABLE IF NOT EXISTS owned_surveys (
 id uuid PRIMARY KEY,publisher_id uuid NOT NULL REFERENCES owned_publishers(id),post_id uuid REFERENCES owned_text_posts(id),
 title text CHECK(length(title)<=1024),description text CHECK(length(description)<=4096),ended_at timestamptz,
 notify_subscribers boolean NOT NULL DEFAULT false,is_anonymous boolean NOT NULL,hide_results boolean NOT NULL,status integer NOT NULL CHECK(status IN(0,1,2)),
 questions jsonb NOT NULL CHECK(jsonb_typeof(questions)='array' AND jsonb_array_length(questions) BETWEEN 1 AND 100),
 attachment_ids jsonb NOT NULL DEFAULT '[]',created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,published_at timestamptz,deleted_at timestamptz,
 CHECK((status=0)=(published_at IS NULL))
);
CREATE INDEX IF NOT EXISTS owned_surveys_publisher ON owned_surveys(publisher_id,created_at DESC,id) WHERE deleted_at IS NULL;
CREATE TABLE IF NOT EXISTS owned_survey_answers (
 id uuid PRIMARY KEY,survey_id uuid NOT NULL REFERENCES owned_surveys(id),account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 answer jsonb NOT NULL CHECK(jsonb_typeof(answer)='object'),created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,
 UNIQUE(survey_id,account_id)
);
CREATE TABLE IF NOT EXISTS owned_survey_audit (
 id bigserial PRIMARY KEY,actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),survey_id uuid NOT NULL REFERENCES owned_surveys(id),
 action text NOT NULL CHECK(action IN('create','update','publish','archive','clone','delete','answer','withdraw','subscribe','unsubscribe')),occurred_at timestamptz NOT NULL
);

-- Additive upgrade for installations of the original survey projection.
ALTER TABLE owned_surveys ADD COLUMN IF NOT EXISTS notify_subscribers boolean NOT NULL DEFAULT false;
ALTER TABLE owned_survey_answers ADD COLUMN IF NOT EXISTS revision bigint NOT NULL DEFAULT 1 CHECK(revision>0);
ALTER TABLE owned_survey_audit DROP CONSTRAINT IF EXISTS owned_survey_audit_action_check;
ALTER TABLE owned_survey_audit ADD CONSTRAINT owned_survey_audit_action_check CHECK(action IN('create','update','publish','archive','clone','delete','answer','withdraw','subscribe','unsubscribe'));
CREATE TABLE IF NOT EXISTS owned_survey_subscriptions (
 id uuid PRIMARY KEY,survey_id uuid NOT NULL REFERENCES owned_surveys(id),account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,revoked_at timestamptz,
 UNIQUE(id,survey_id,account_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS owned_survey_subscription_active ON owned_survey_subscriptions(survey_id,account_id) WHERE revoked_at IS NULL;
-- Subscription incarnations survive unsubscribe so a later subscription cannot
-- revive an old queued notification. No answer text or answerer ID is projected.
-- Recipient authority ancestry is enforced through the subscription composite
-- FK. A redundant recipient-account FK here would acquire a new account lock
-- under the answer's survey lock, inverting the dispatcher's lock order.
CREATE TABLE IF NOT EXISTS owned_survey_notifications (
 id uuid PRIMARY KEY,survey_id uuid NOT NULL REFERENCES owned_surveys(id),subscription_id uuid NOT NULL,
 account_id uuid NOT NULL,answer_id uuid NOT NULL,answer_revision bigint NOT NULL CHECK(answer_revision>0),
 created_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,processed_at timestamptz,suppressed_at timestamptz,app_id varchar(128) NOT NULL,
 FOREIGN KEY(subscription_id,survey_id,account_id) REFERENCES owned_survey_subscriptions(id,survey_id,account_id),
 UNIQUE(answer_id,answer_revision,subscription_id),CHECK(expires_at>created_at),CHECK(suppressed_at IS NULL OR processed_at IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS owned_survey_notifications_pending ON owned_survey_notifications(app_id,created_at,id) WHERE processed_at IS NULL;
