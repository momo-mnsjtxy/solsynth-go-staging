-- SPDX-License-Identifier: AGPL-3.0-only
-- Fresh owned schema, applied explicitly after canonical authority, accounts,
-- posts, and owned taxonomy. No runtime migration or alternate post authority.
CREATE TABLE owned_post_reactions (
 id uuid PRIMARY KEY,post_id uuid NOT NULL REFERENCES owned_text_posts(id),
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),symbol varchar(256) NOT NULL,
 attitude integer NOT NULL CHECK(attitude IN(0,1,2)),created_at timestamptz NOT NULL,
 UNIQUE(post_id,account_id,symbol)
);
CREATE INDEX owned_post_reactions_list ON owned_post_reactions(post_id,symbol,created_at DESC,id);
CREATE TABLE owned_post_bookmarks (
 id uuid PRIMARY KEY,post_id uuid NOT NULL REFERENCES owned_text_posts(id),
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),created_at timestamptz NOT NULL,
 UNIQUE(post_id,account_id)
);
CREATE INDEX owned_post_bookmarks_list ON owned_post_bookmarks(account_id,created_at DESC,id);
CREATE TABLE owned_post_collections (
 id uuid PRIMARY KEY,publisher_id uuid NOT NULL REFERENCES owned_publishers(id),
 slug varchar(128) NOT NULL CHECK(length(btrim(slug))>0),name varchar(256),description varchar(4096),
	background_file_id uuid REFERENCES owned_media_files(id),icon_file_id uuid REFERENCES owned_media_files(id),
 created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,deleted_at timestamptz
);
-- Deleted slugs remain reserved, so bookmarks cannot resolve to a new collection.
CREATE UNIQUE INDEX owned_post_collections_slug ON owned_post_collections(publisher_id,lower(slug));
CREATE TABLE owned_post_collection_items (
 collection_id uuid NOT NULL REFERENCES owned_post_collections(id),
 post_id uuid NOT NULL REFERENCES owned_text_posts(id),created_at timestamptz NOT NULL,
 PRIMARY KEY(collection_id,post_id)
);
CREATE TABLE owned_post_interaction_audit (
 id bigserial PRIMARY KEY,actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 post_id uuid REFERENCES owned_text_posts(id),collection_id uuid REFERENCES owned_post_collections(id),
 action text NOT NULL CHECK(action IN('react','unreact','bookmark','unbookmark','collection.create','collection.update','collection.delete','collection.add','collection.remove','collection.subscribe','collection.unsubscribe','post.associate')),
 occurred_at timestamptz NOT NULL,CHECK(post_id IS NOT NULL OR collection_id IS NOT NULL)
);

-- Collection subscriptions share the authored taxonomy subscription identity.
-- A fresh live incarnation is created after unsubscribe; historical rows remain.
ALTER TABLE owned_taxonomy.post_category_subscriptions ADD CONSTRAINT owned_collection_subscription_target FOREIGN KEY(collection_id) REFERENCES owned_post_collections(id);
ALTER TABLE owned_taxonomy.post_category_subscriptions ADD CONSTRAINT owned_collection_subscription_kind CHECK(collection_id IS NULL OR(category_id IS NULL AND tag_id IS NULL));
CREATE UNIQUE INDEX owned_collection_subscription_live ON owned_taxonomy.post_category_subscriptions(account_id,collection_id) WHERE collection_id IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX owned_collection_subscriptions_target ON owned_taxonomy.post_category_subscriptions(collection_id,account_id) WHERE collection_id IS NOT NULL AND deleted_at IS NULL;
