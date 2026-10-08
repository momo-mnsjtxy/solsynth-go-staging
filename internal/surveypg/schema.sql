-- SPDX-License-Identifier: AGPL-3.0-only
-- Owned schema; apply after authority, accounts, posts and media. No legacy schema claim.
CREATE TABLE IF NOT EXISTS owned_surveys (
 id uuid PRIMARY KEY,publisher_id uuid NOT NULL REFERENCES owned_publishers(id),post_id uuid REFERENCES owned_text_posts(id),
 title text CHECK(length(title)<=1024),description text CHECK(length(description)<=4096),ended_at timestamptz,
 is_anonymous boolean NOT NULL,hide_results boolean NOT NULL,status integer NOT NULL CHECK(status IN(0,1,2)),
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
 action text NOT NULL CHECK(action IN('create','update','publish','archive','clone','delete','answer','withdraw')),occurred_at timestamptz NOT NULL
);
