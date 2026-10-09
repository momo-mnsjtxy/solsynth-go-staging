-- SPDX-License-Identifier: AGPL-3.0-only
-- Canonical post/publisher columns come from their additive schema migrations.
-- Install after authority, account, post, social and post-realm schemas.
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='owned_publishers'::regclass AND conname='owned_publisher_shadowban_valid')THEN
  ALTER TABLE owned_publishers ADD CONSTRAINT owned_publisher_shadowban_valid CHECK(
   (shadowban_reason IS NULL AND shadowbanned_at IS NULL)OR
   (shadowban_reason IS NOT NULL AND shadowban_reason IN('spam','advertising','harassment','hate_speech','misinformation','illegal','other')AND shadowbanned_at IS NOT NULL));
 END IF;
 IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='owned_text_posts'::regclass AND conname='owned_post_shadowban_valid')THEN
  ALTER TABLE owned_text_posts ADD CONSTRAINT owned_post_shadowban_valid CHECK(
   (shadowban_reason IS NULL AND shadowbanned_at IS NULL)OR
   (shadowban_reason IS NOT NULL AND shadowban_reason IN('spam','advertising','harassment','hate_speech','misinformation','illegal','other')AND shadowbanned_at IS NOT NULL));
 END IF;
END;$$;
CREATE TABLE IF NOT EXISTS owned_content_moderation_audit(
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 kind text NOT NULL CHECK(kind IN('post','publisher','realm')),
 target_id uuid NOT NULL,action text NOT NULL CHECK(length(action)BETWEEN 1 AND 128),
 detail jsonb NOT NULL CHECK(jsonb_typeof(detail)='object'AND octet_length(detail::text)<=32768),
 occurred_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS owned_content_moderation_target ON owned_content_moderation_audit(kind,target_id,id);
-- Same-transaction notification intent; projection uses the existing local
-- notification outbox through an explicit idempotent worker. No external send.
CREATE TABLE IF NOT EXISTS owned_content_moderation_notifications(
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 realm_id uuid NOT NULL REFERENCES owned_realms(id),post_id uuid NOT NULL REFERENCES owned_text_posts(id),
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),reason text CHECK(length(reason)<=4096),
 created_at timestamptz NOT NULL,projected_at timestamptz,app_id text CHECK(length(app_id) BETWEEN 1 AND 128),
 UNIQUE(realm_id,post_id)
);
