-- SPDX-License-Identifier: AGPL-3.0-only
-- Owned support-case and examination records. Apply explicitly after canonical
-- authority and signin schemas. No runtime migration or permission seed.
CREATE TABLE IF NOT EXISTS owned_ticket_cases (
 id uuid PRIMARY KEY,creator_id uuid NOT NULL REFERENCES owned_authority_accounts(id),assignee_id uuid REFERENCES owned_authority_accounts(id),
 title text NOT NULL CHECK(length(title) BETWEEN 3 AND 256),type integer NOT NULL CHECK(type BETWEEN 0 AND 3),priority integer NOT NULL CHECK(priority BETWEEN 0 AND 3),status integer NOT NULL DEFAULT 0 CHECK(status BETWEEN 0 AND 3),
 resources jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(resources)='array' AND jsonb_array_length(resources)<=32),
 created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,resolved_at timestamptz,deleted_at timestamptz,
 request_key text,request_hash char(64),UNIQUE(creator_id,request_key)
);
CREATE INDEX IF NOT EXISTS owned_ticket_creator ON owned_ticket_cases(creator_id,created_at DESC,id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS owned_ticket_assignee ON owned_ticket_cases(assignee_id,created_at DESC,id) WHERE deleted_at IS NULL;
CREATE TABLE IF NOT EXISTS owned_ticket_messages (
 id uuid PRIMARY KEY,ticket_id uuid NOT NULL REFERENCES owned_ticket_cases(id),sender_id uuid NOT NULL REFERENCES owned_authority_accounts(id),content text NOT NULL CHECK(length(content) BETWEEN 1 AND 16384),created_at timestamptz NOT NULL,request_key text,request_hash char(64),UNIQUE(sender_id,ticket_id,request_key)
);
CREATE INDEX IF NOT EXISTS owned_ticket_message_order ON owned_ticket_messages(ticket_id,created_at,id);
CREATE TABLE IF NOT EXISTS owned_ticket_on_call(id uuid PRIMARY KEY,account_id uuid NOT NULL UNIQUE REFERENCES owned_authority_accounts(id),created_at timestamptz NOT NULL);
CREATE TABLE IF NOT EXISTS owned_exam_groups(id uuid PRIMARY KEY,key text NOT NULL UNIQUE CHECK(length(key) BETWEEN 1 AND 128),data jsonb NOT NULL CHECK(jsonb_typeof(data)='object'));
CREATE TABLE IF NOT EXISTS owned_exam_questions(id uuid PRIMARY KEY,group_id uuid NOT NULL REFERENCES owned_exam_groups(id),sort_order integer NOT NULL,data jsonb NOT NULL CHECK(jsonb_typeof(data)='object'));
CREATE INDEX IF NOT EXISTS owned_exam_question_group ON owned_exam_questions(group_id,sort_order,id);
CREATE TABLE IF NOT EXISTS owned_exams(id uuid PRIMARY KEY,key text NOT NULL UNIQUE CHECK(length(key) BETWEEN 1 AND 128),data jsonb NOT NULL CHECK(jsonb_typeof(data)='object'),is_published boolean NOT NULL,is_listed boolean NOT NULL,is_archived boolean NOT NULL DEFAULT false,created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL);
CREATE TABLE IF NOT EXISTS owned_exam_group_links(test_id uuid NOT NULL REFERENCES owned_exams(id),group_id uuid NOT NULL REFERENCES owned_exam_groups(id),sort_order integer NOT NULL,PRIMARY KEY(test_id,group_id));
CREATE TABLE IF NOT EXISTS owned_exam_trials(id uuid PRIMARY KEY,key text NOT NULL UNIQUE CHECK(length(key) BETWEEN 1 AND 128),test_id uuid NOT NULL REFERENCES owned_exams(id),data jsonb NOT NULL CHECK(jsonb_typeof(data)='object'),is_published boolean NOT NULL);
CREATE TABLE IF NOT EXISTS owned_exam_attempts (
 id uuid PRIMARY KEY,test_id uuid NOT NULL REFERENCES owned_exams(id),account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),is_trial boolean NOT NULL,trial_id uuid REFERENCES owned_exam_trials(id),status integer NOT NULL CHECK(status BETWEEN 0 AND 4),
 started_at timestamptz NOT NULL,deadline_at timestamptz,submitted_at timestamptz,reviewed_at timestamptz,score double precision CHECK(score BETWEEN -100 AND 100),snapshot jsonb NOT NULL CHECK(jsonb_typeof(snapshot)='object'),request_key text,request_hash char(64),submission_hash char(64),UNIQUE(account_id,request_key)
);
CREATE UNIQUE INDEX IF NOT EXISTS owned_exam_active_attempt ON owned_exam_attempts(account_id,test_id,is_trial,COALESCE(trial_id,'00000000-0000-0000-0000-000000000000'::uuid)) WHERE status=0;
CREATE INDEX IF NOT EXISTS owned_exam_attempt_account ON owned_exam_attempts(account_id,test_id,started_at DESC,id);
CREATE TABLE IF NOT EXISTS owned_exam_answers(id uuid PRIMARY KEY,attempt_id uuid NOT NULL REFERENCES owned_exam_attempts(id),question_id uuid NOT NULL,value jsonb NOT NULL CHECK(jsonb_typeof(value)='object'),is_correct boolean,awarded_points double precision,review_note text CHECK(length(review_note)<=4096),reviewed_at timestamptz,reviewed_by_id uuid REFERENCES owned_authority_accounts(id),UNIQUE(attempt_id,question_id));
-- Receipts and pending effects are not replacement identity, permission, or XP
-- authorities. Only explicitly allowlisted canonical group membership is applied.
CREATE TABLE IF NOT EXISTS owned_exam_claims(account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),test_id uuid NOT NULL REFERENCES owned_exams(id),attempt_id uuid NOT NULL REFERENCES owned_exam_attempts(id),group_key text NOT NULL,group_id uuid REFERENCES owned_authority_groups(id),state text NOT NULL CHECK(state IN('pending','granted')),created_at timestamptz NOT NULL,PRIMARY KEY(account_id,test_id,group_key));
CREATE TABLE IF NOT EXISTS owned_exam_rewards(account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),test_id uuid NOT NULL REFERENCES owned_exams(id),attempt_id uuid NOT NULL REFERENCES owned_exam_attempts(id),amount bigint NOT NULL CHECK(amount>0),state text NOT NULL DEFAULT 'pending' CHECK(state IN('pending','applied')),created_at timestamptz NOT NULL,PRIMARY KEY(account_id,test_id));
CREATE TABLE IF NOT EXISTS owned_exam_activation_requests(account_id uuid PRIMARY KEY REFERENCES owned_authority_accounts(id),state text NOT NULL DEFAULT 'pending' CHECK(state='pending'),created_at timestamptz NOT NULL);
CREATE TABLE IF NOT EXISTS owned_ticket_exam_audit(id bigserial PRIMARY KEY,actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),operation text NOT NULL,resource_id uuid,occurred_at timestamptz NOT NULL);
