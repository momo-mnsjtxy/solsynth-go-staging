-- SPDX-License-Identifier: AGPL-3.0-only
-- Owned text-post schema, not a legacy migration. Explicitly apply after
-- authoritypg.Schema and accountpg.Schema. No runtime auto-migration.
CREATE TABLE IF NOT EXISTS owned_text_posts (
 id uuid PRIMARY KEY,
 publisher_id uuid NOT NULL REFERENCES owned_publishers(id),
 realm_id uuid,
 title text CHECK(length(title)<=1024),description text CHECK(length(description)<=4096),
 slug text CHECK(length(slug)<=1024),content text NOT NULL CONSTRAINT owned_text_posts_content_check CHECK(length(content)<=1048576),
 visibility integer NOT NULL CHECK(visibility IN(0,1,2,3,4)),type integer NOT NULL CHECK(type IN(0,1)),language text CHECK(length(language)<=16),
 drafted_at timestamptz,published_at timestamptz,created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,
 deleted_at timestamptz,locked_at timestamptz,
 CHECK((drafted_at IS NULL)<>(published_at IS NULL))
);
-- Soft deletion reserves slugs, preventing old post links identifying a new post.
CREATE UNIQUE INDEX IF NOT EXISTS owned_text_posts_publisher_slug ON owned_text_posts(publisher_id,lower(slug)) WHERE slug IS NOT NULL;
CREATE INDEX IF NOT EXISTS owned_text_posts_listing ON owned_text_posts(published_at DESC,id) WHERE deleted_at IS NULL AND drafted_at IS NULL;
CREATE INDEX IF NOT EXISTS owned_text_posts_drafts ON owned_text_posts(publisher_id,drafted_at DESC,id) WHERE deleted_at IS NULL AND drafted_at IS NOT NULL;
CREATE TABLE IF NOT EXISTS owned_text_post_audit (
 id bigserial PRIMARY KEY,actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 post_id uuid NOT NULL REFERENCES owned_text_posts(id),action text NOT NULL CHECK(action IN('create','update','publish','delete')),
 occurred_at timestamptz NOT NULL
);

-- Upgrade the owned text-only constraint explicitly. Empty bodies are validated
-- against active authorized attachments inside the canonical post transaction.
ALTER TABLE owned_text_posts DROP CONSTRAINT IF EXISTS owned_text_posts_content_check;
ALTER TABLE owned_text_posts ADD CONSTRAINT owned_text_posts_content_check CHECK(length(content)<=1048576);
-- Nullable projection also exists for standalone hosts without a realm provider.
-- The canonical foreign key is installed by realm_schema.sql after socialpg.
ALTER TABLE owned_text_posts ADD COLUMN IF NOT EXISTS realm_id uuid;
ALTER TABLE owned_text_posts ADD COLUMN IF NOT EXISTS shadowban_reason text;
ALTER TABLE owned_text_posts ADD COLUMN IF NOT EXISTS shadowbanned_at timestamptz;

ALTER TABLE owned_text_posts ADD COLUMN IF NOT EXISTS awarded_score numeric(32,3) NOT NULL DEFAULT 0;

-- Durable local-rule evidence; ranked-feed evaluation always uses current rules.
ALTER TABLE owned_text_posts ADD COLUMN IF NOT EXISTS automod_revision bigint NOT NULL DEFAULT 0 CHECK(automod_revision>=0);
ALTER TABLE owned_text_posts ADD COLUMN IF NOT EXISTS automod_decision jsonb CHECK(automod_decision IS NULL OR(jsonb_typeof(automod_decision)='object' AND octet_length(automod_decision::text)<=2097152));
CREATE TABLE IF NOT EXISTS owned_post_automod_events(
 post_id uuid NOT NULL REFERENCES owned_text_posts(id),revision bigint NOT NULL,
 actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),transition text NOT NULL,
 decision jsonb NOT NULL CHECK(jsonb_typeof(decision)='object' AND octet_length(decision::text)<=2097152),
 occurred_at timestamptz NOT NULL,PRIMARY KEY(post_id,revision)
);


-- Owned blog support stores only server-produced, bounded link metadata.
ALTER TABLE owned_text_posts ADD COLUMN IF NOT EXISTS blog_metadata jsonb;
ALTER TABLE owned_text_posts DROP CONSTRAINT IF EXISTS owned_text_posts_type_check;
ALTER TABLE owned_text_posts ADD CONSTRAINT owned_text_posts_type_check CHECK(type IN(0,1,2));
ALTER TABLE owned_text_posts DROP CONSTRAINT IF EXISTS owned_text_posts_blog_metadata_check;
ALTER TABLE owned_text_posts ADD CONSTRAINT owned_text_posts_blog_metadata_check CHECK(blog_metadata IS NULL OR (jsonb_typeof(blog_metadata)='object' AND octet_length(blog_metadata::text)<=65536));

-- Authored Solian active controls establish 0=publisher, 1=realm. Reply mode
-- remains unsupported. This is owned storage, not the excluded Shared schema.
ALTER TABLE owned_text_posts ADD COLUMN IF NOT EXISTS pin_mode integer;
ALTER TABLE owned_text_posts DROP CONSTRAINT IF EXISTS owned_text_posts_pin_mode_check;
ALTER TABLE owned_text_posts ADD CONSTRAINT owned_text_posts_pin_mode_check CHECK(pin_mode IN(0,1,2));
ALTER TABLE owned_text_post_audit DROP CONSTRAINT IF EXISTS owned_text_post_audit_action_check;
ALTER TABLE owned_text_post_audit ADD CONSTRAINT owned_text_post_audit_action_check CHECK(action IN('create','update','publish','delete','pin','unpin'));
CREATE INDEX IF NOT EXISTS owned_text_posts_pinned ON owned_text_posts(pin_mode,publisher_id,realm_id,published_at DESC,id) WHERE deleted_at IS NULL AND pin_mode IS NOT NULL;

CREATE TABLE IF NOT EXISTS owned_post_boosts(
 id uuid PRIMARY KEY,post_id uuid NOT NULL REFERENCES owned_text_posts(id),publisher_id uuid NOT NULL REFERENCES owned_publishers(id),
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),content text CHECK(length(content)<=1024),boosted_at timestamptz NOT NULL,
 UNIQUE(post_id,publisher_id)
);
CREATE INDEX IF NOT EXISTS owned_post_boosts_post ON owned_post_boosts(post_id,boosted_at DESC,id);

-- Immutable domain receipts are the canonical source for eventually projected
-- progression. Boost receipts survive an unboost; topical actions and daily
-- featured selections remain facts bound to the server-observed publication.
CREATE TABLE IF NOT EXISTS owned_post_progression_sources(
 id uuid PRIMARY KEY,
 kind text NOT NULL CONSTRAINT owned_post_progression_source_kind CHECK(kind IN('post.boost','post.featured','post.create_topical')),
 post_id uuid NOT NULL CONSTRAINT owned_post_progression_source_post REFERENCES owned_text_posts(id),
 target_publisher_id uuid NOT NULL CONSTRAINT owned_post_progression_source_target_publisher REFERENCES owned_publishers(id),
 action_publisher_id uuid CONSTRAINT owned_post_progression_source_action_publisher REFERENCES owned_publishers(id),
 account_id uuid NOT NULL CONSTRAINT owned_post_progression_source_account REFERENCES owned_authority_accounts(id),
 published_at timestamptz NOT NULL,tag_id uuid,category_id uuid,score numeric(32,3),
 featured_day date,featured_rank smallint,occurred_at timestamptz NOT NULL,
 CONSTRAINT owned_post_progression_source_shape CHECK(
  (kind='post.boost' AND action_publisher_id IS NOT NULL AND tag_id IS NULL AND category_id IS NULL AND score IS NULL AND featured_day IS NULL AND featured_rank IS NULL)
  OR(kind='post.create_topical' AND action_publisher_id IS NULL AND tag_id IS NOT NULL AND category_id IS NOT NULL AND score IS NULL AND featured_day IS NULL AND featured_rank IS NULL)
  OR(kind='post.featured' AND action_publisher_id IS NULL AND tag_id IS NULL AND category_id IS NULL AND score IS NOT NULL AND featured_day IS NOT NULL AND featured_rank BETWEEN 1 AND 5)
 )
);
CREATE UNIQUE INDEX IF NOT EXISTS owned_post_progression_topical_once ON owned_post_progression_sources(post_id) WHERE kind='post.create_topical';
CREATE UNIQUE INDEX IF NOT EXISTS owned_post_progression_featured_day_rank ON owned_post_progression_sources(featured_day,featured_rank) WHERE kind='post.featured';
CREATE UNIQUE INDEX IF NOT EXISTS owned_post_progression_featured_day_post ON owned_post_progression_sources(featured_day,post_id) WHERE kind='post.featured';
CREATE INDEX IF NOT EXISTS owned_post_progression_due ON owned_post_progression_sources(occurred_at,kind,id);
CREATE TABLE IF NOT EXISTS owned_post_featured_days(
 day date PRIMARY KEY,completed_at timestamptz NOT NULL,
 candidate_count smallint NOT NULL CONSTRAINT owned_post_featured_day_count CHECK(candidate_count BETWEEN 0 AND 5)
);
CREATE OR REPLACE FUNCTION owned_post_progression_source_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'post progression source facts are immutable' USING ERRCODE='55000';
END $$;
DROP TRIGGER IF EXISTS owned_post_progression_source_immutable ON owned_post_progression_sources;
CREATE TRIGGER owned_post_progression_source_immutable BEFORE UPDATE OR DELETE ON owned_post_progression_sources
 FOR EACH ROW EXECUTE FUNCTION owned_post_progression_source_immutable();
ALTER TABLE owned_text_post_audit DROP CONSTRAINT IF EXISTS owned_text_post_audit_action_check;
ALTER TABLE owned_text_post_audit ADD CONSTRAINT owned_text_post_audit_action_check CHECK(action IN('create','update','publish','delete','pin','unpin','boost','unboost'));
