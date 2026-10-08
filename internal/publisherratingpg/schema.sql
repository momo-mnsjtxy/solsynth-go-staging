-- SPDX-License-Identifier: AGPL-3.0-only
CREATE TABLE IF NOT EXISTS owned_publisher_rating_records(
 id uuid PRIMARY KEY,publisher_id uuid NOT NULL REFERENCES owned_publishers(id),period_start timestamptz NOT NULL,
 policy text NOT NULL CHECK(policy='reaction_only_v1'),delta bigint NOT NULL CHECK(delta BETWEEN -1000000 AND 1000000),
 positive_count bigint NOT NULL CHECK(positive_count BETWEEN 0 AND 1000000),negative_count bigint NOT NULL CHECK(negative_count BETWEEN 0 AND 1000000),
 created_at timestamptz NOT NULL,UNIQUE(publisher_id,period_start),CHECK(delta=positive_count-negative_count)
);
