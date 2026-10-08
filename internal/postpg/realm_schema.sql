-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit migration after postpg/schema.sql and socialpg/schema.sql.
-- Realm deletion is a tombstone; never null the association or publish its posts.
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='owned_text_posts'::regclass AND conname='owned_text_posts_realm_fk') THEN
  ALTER TABLE owned_text_posts ADD CONSTRAINT owned_text_posts_realm_fk FOREIGN KEY(realm_id) REFERENCES owned_realms(id);
 END IF;
END; $$;
CREATE INDEX IF NOT EXISTS owned_text_posts_realm_feed ON owned_text_posts(realm_id,published_at DESC,id) WHERE deleted_at IS NULL AND drafted_at IS NULL;
