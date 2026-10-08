-- SPDX-License-Identifier: AGPL-3.0-only
-- Owned text-post schema, not a legacy migration. Explicitly apply after
-- authoritypg.Schema and accountpg.Schema. No runtime auto-migration.
CREATE TABLE IF NOT EXISTS owned_text_posts (
 id uuid PRIMARY KEY,
 publisher_id uuid NOT NULL REFERENCES owned_publishers(id),
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
