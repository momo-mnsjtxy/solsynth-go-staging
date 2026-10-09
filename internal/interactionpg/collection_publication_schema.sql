-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit schema installation. Durable identity-only publication intent.
CREATE TABLE IF NOT EXISTS owned_collection_publications (
 id uuid PRIMARY KEY, post_id uuid NOT NULL REFERENCES owned_text_posts(id),
 publisher_id uuid NOT NULL, owner_id uuid NOT NULL, published_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(post_id,published_at)
);
-- Recipient/subscription IDs intentionally have no foreign keys: enqueue runs
-- after post locks and must never acquire a recipient account/subscription lock.
-- Canonical identities and the exact live subscription are checked by the worker.
CREATE TABLE IF NOT EXISTS owned_collection_publication_notifications (
 id uuid PRIMARY KEY, publication_id uuid NOT NULL REFERENCES owned_collection_publications(id),
 account_id uuid NOT NULL, app_id varchar(128) NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 processed_at timestamptz, suppressed_at timestamptz,
 UNIQUE(publication_id,account_id,app_id),
 CHECK(suppressed_at IS NULL OR processed_at IS NOT NULL)
);
CREATE TABLE IF NOT EXISTS owned_collection_publication_subscriptions (
 notification_id uuid NOT NULL REFERENCES owned_collection_publication_notifications(id),
 subscription_id uuid NOT NULL, collection_id uuid NOT NULL,
 PRIMARY KEY(notification_id,subscription_id)
);
CREATE INDEX IF NOT EXISTS owned_collection_publication_pending ON owned_collection_publication_notifications(app_id,created_at,id) WHERE processed_at IS NULL;
