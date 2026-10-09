-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit owned installation AFTER authority, accounts, posts, social,
-- canonical taxonomy and interactions. No post/publisher authority duplicates.
CREATE TABLE owned_post_relations (
 post_id uuid PRIMARY KEY REFERENCES owned_text_posts(id),
 replied_post_id uuid REFERENCES owned_text_posts(id),
 forwarded_post_id uuid REFERENCES owned_text_posts(id),
 chained_post_id uuid REFERENCES owned_text_posts(id),
 CHECK(post_id<>replied_post_id AND post_id<>forwarded_post_id AND post_id<>chained_post_id),
 CHECK(chained_post_id IS NULL OR (replied_post_id IS NULL AND forwarded_post_id IS NULL))
);
CREATE INDEX owned_post_reply_parent ON owned_post_relations(replied_post_id,post_id) WHERE replied_post_id IS NOT NULL;
CREATE INDEX owned_post_forward_parent ON owned_post_relations(forwarded_post_id,post_id) WHERE forwarded_post_id IS NOT NULL;
CREATE INDEX owned_post_chain_parent ON owned_post_relations(chained_post_id,post_id) WHERE chained_post_id IS NOT NULL;
-- Explicit deployment policy, separate from access grants. Defaults to open
-- following. It never grants access to private/friends posts.
CREATE TABLE owned_publisher_follow_policy (
 publisher_id uuid PRIMARY KEY REFERENCES owned_publishers(id),requires_approval boolean NOT NULL
);
CREATE TABLE owned_publisher_follows (
 id uuid PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 publisher_id uuid NOT NULL REFERENCES owned_publishers(id),state text NOT NULL CHECK(state IN('pending','accepted','rejected')),
 notify boolean NOT NULL DEFAULT true,reason text CHECK(length(reason)<=1024),
 created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,ended_at timestamptz,
 UNIQUE(account_id,publisher_id)
);
CREATE INDEX owned_publisher_follow_requests ON owned_publisher_follows(publisher_id,created_at,id) WHERE state='pending' AND ended_at IS NULL;
CREATE TABLE owned_timeline_interests (
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),kind text NOT NULL CHECK(kind IN('publisher','collection','tag','category')),
 reference_id uuid NOT NULL,score double precision NOT NULL CHECK(score BETWEEN -100 AND 100),
 interaction_count bigint NOT NULL CHECK(interaction_count>=0),last_interacted_at timestamptz NOT NULL,last_signal_type text NOT NULL CHECK(length(last_signal_type)<=128),
 PRIMARY KEY(account_id,kind,reference_id)
);
CREATE TABLE owned_timeline_preferences (
 id uuid PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),kind text NOT NULL CHECK(kind IN('publisher','realm','account')),
 reference_id uuid NOT NULL,reason text CHECK(length(reason)<=1024),applied_at timestamptz NOT NULL,created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,
 UNIQUE(account_id,kind,reference_id)
);
CREATE TABLE owned_timeline_audit (
 id bigserial PRIMARY KEY,actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),action text NOT NULL,
 target_id uuid,occurred_at timestamptz NOT NULL
);
CREATE TABLE owned_post_watch_preferences (
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),source text NOT NULL CHECK(source IN('bookmark','reaction','reply')),
 notify_reactions boolean NOT NULL,notify_replies boolean NOT NULL,notify_chains boolean NOT NULL,notify_forwards boolean NOT NULL,notify_edits boolean NOT NULL,
 updated_at timestamptz NOT NULL,PRIMARY KEY(account_id,source)
);
