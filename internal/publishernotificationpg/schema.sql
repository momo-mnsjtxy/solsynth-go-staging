-- SPDX-License-Identifier: AGPL-3.0-only
-- Installed after timeline, posts, livestreams and notification_v1.
ALTER TABLE owned_publisher_follows
 ADD COLUMN IF NOT EXISTS incarnation bigint NOT NULL DEFAULT 1,
 ADD COLUMN IF NOT EXISTS incarnation_started_at timestamptz NOT NULL DEFAULT clock_timestamp();
ALTER TABLE owned_publisher_follows DROP CONSTRAINT IF EXISTS owned_publisher_follow_incarnation;
ALTER TABLE owned_publisher_follows ADD CONSTRAINT owned_publisher_follow_incarnation CHECK(incarnation>0);

CREATE TABLE owned_publisher_content_notification_intents (
 id uuid NOT NULL,
 kind text NOT NULL,
 source_id uuid NOT NULL,
 source_generation bigint NOT NULL DEFAULT 0,
 post_id uuid,
 livestream_id uuid,
 publisher_id uuid NOT NULL,
 occurred_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 completed_at timestamptz,
	CONSTRAINT owned_publisher_content_intent_key PRIMARY KEY(id),
 CONSTRAINT owned_publisher_content_intent_kind CHECK(kind IN('post.published','livestream.started')),
 CONSTRAINT owned_publisher_content_intent_shape CHECK((kind='post.published' AND source_generation=0 AND post_id=source_id AND livestream_id IS NULL) OR (kind='livestream.started' AND source_generation>0 AND livestream_id=source_id AND post_id IS NULL)),
 CONSTRAINT owned_publisher_content_intent_publisher FOREIGN KEY(publisher_id) REFERENCES owned_publishers(id),
 CONSTRAINT owned_publisher_content_intent_post FOREIGN KEY(post_id) REFERENCES owned_text_posts(id),
 CONSTRAINT owned_publisher_content_intent_livestream FOREIGN KEY(livestream_id) REFERENCES owned_livestreams(id),
 CONSTRAINT owned_publisher_content_intent_source UNIQUE(kind,source_id,source_generation)
);
CREATE INDEX owned_publisher_content_notification_due ON owned_publisher_content_notification_intents(created_at,id) WHERE completed_at IS NULL;

CREATE FUNCTION owned_publisher_content_intent_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	IF TG_OP='DELETE' THEN
	 RAISE EXCEPTION 'publisher content notification intent is immutable';
	END IF;
 IF OLD.kind<>NEW.kind OR OLD.source_id<>NEW.source_id OR OLD.source_generation<>NEW.source_generation OR OLD.publisher_id<>NEW.publisher_id OR OLD.occurred_at<>NEW.occurred_at OR OLD.post_id IS DISTINCT FROM NEW.post_id OR OLD.livestream_id IS DISTINCT FROM NEW.livestream_id OR OLD.created_at<>NEW.created_at THEN
  RAISE EXCEPTION 'publisher content notification intent identity is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER owned_publisher_content_intent_immutable BEFORE UPDATE OR DELETE ON owned_publisher_content_notification_intents FOR EACH ROW EXECUTE FUNCTION owned_publisher_content_intent_guard();

CREATE TABLE owned_publisher_content_notification_receipts (
 intent_id uuid NOT NULL,
 subscription_id uuid NOT NULL,
 subscription_incarnation bigint NOT NULL,
 account_id uuid NOT NULL,
 outcome text NOT NULL,
 outbox_id uuid,
 processed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CONSTRAINT owned_publisher_content_receipt_intent FOREIGN KEY(intent_id) REFERENCES owned_publisher_content_notification_intents(id),
 CONSTRAINT owned_publisher_content_receipt_subscription FOREIGN KEY(subscription_id) REFERENCES owned_publisher_follows(id),
 CONSTRAINT owned_publisher_content_receipt_account FOREIGN KEY(account_id) REFERENCES owned_authority_accounts(id),
 CONSTRAINT owned_publisher_content_receipt_outbox FOREIGN KEY(outbox_id) REFERENCES notification_v1.outbox(id),
 CONSTRAINT owned_publisher_content_receipt_key PRIMARY KEY(intent_id,subscription_id,subscription_incarnation),
 CONSTRAINT owned_publisher_content_receipt_incarnation CHECK(subscription_incarnation>0),
 CONSTRAINT owned_publisher_content_receipt_kind CHECK(outcome IN('enqueued','suppressed')),
 CONSTRAINT owned_publisher_content_receipt_outcome CHECK((outcome='enqueued')=(outbox_id IS NOT NULL))
);
