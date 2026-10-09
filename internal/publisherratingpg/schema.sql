-- SPDX-License-Identifier: AGPL-3.0-only
CREATE TABLE IF NOT EXISTS owned_publisher_rating_records(
 id uuid PRIMARY KEY,publisher_id uuid NOT NULL REFERENCES owned_publishers(id),period_start timestamptz NOT NULL,
 policy text NOT NULL CHECK(policy='reaction_only_v1'),delta bigint NOT NULL CHECK(delta BETWEEN -1000000 AND 1000000),
 positive_count bigint NOT NULL CHECK(positive_count BETWEEN 0 AND 1000000),negative_count bigint NOT NULL CHECK(negative_count BETWEEN 0 AND 1000000),
 created_at timestamptz NOT NULL,UNIQUE(publisher_id,period_start),CHECK(delta=positive_count-negative_count)
);

ALTER TABLE owned_publisher_rating_records ADD COLUMN IF NOT EXISTS awarded_score numeric(32,3) NOT NULL DEFAULT 0;
ALTER TABLE owned_publisher_rating_records DROP CONSTRAINT IF EXISTS owned_publisher_rating_records_policy_check;
ALTER TABLE owned_publisher_rating_records ADD CONSTRAINT owned_publisher_rating_records_policy_check CHECK(policy IN('reaction_only_v1','reaction_and_paid_awards_v1'));
ALTER TABLE owned_publisher_rating_records DROP CONSTRAINT IF EXISTS owned_publisher_rating_records_check;
ALTER TABLE owned_publisher_rating_records ADD CONSTRAINT owned_publisher_rating_records_check CHECK((policy='reaction_only_v1' AND awarded_score=0 AND delta=positive_count-negative_count)OR(policy='reaction_and_paid_awards_v1' AND delta=trunc(positive_count-negative_count+awarded_score/10)));
