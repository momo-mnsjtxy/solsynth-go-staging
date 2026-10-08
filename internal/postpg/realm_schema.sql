-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit migration after postpg/schema.sql and socialpg/schema.sql.
-- Realm deletion is a tombstone; never null the association or publish its posts.
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='owned_text_posts'::regclass AND conname='owned_text_posts_realm_fk') THEN
  ALTER TABLE owned_text_posts ADD CONSTRAINT owned_text_posts_realm_fk FOREIGN KEY(realm_id) REFERENCES owned_realms(id);
 END IF;
END; $$;
CREATE INDEX IF NOT EXISTS owned_text_posts_realm_feed ON owned_text_posts(realm_id,published_at DESC,id) WHERE deleted_at IS NULL AND drafted_at IS NULL;
-- The authored moderation flow forbids reattaching a removed post to the same
-- realm. Retain the canonical association history independently of soft deletion.
CREATE TABLE IF NOT EXISTS owned_realm_post_moderation (
 realm_id uuid NOT NULL REFERENCES owned_realms(id),post_id uuid NOT NULL REFERENCES owned_text_posts(id),
 moderator_id uuid NOT NULL REFERENCES owned_authority_accounts(id),reason text CHECK(length(reason)<=4096),
 created_at timestamptz NOT NULL,PRIMARY KEY(realm_id,post_id)
);
