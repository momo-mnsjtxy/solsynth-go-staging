-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit owned runtime taxonomy projection, adapted from the licensed domain
-- schema evidence documented in internal/taxonomypg/schema-evidence.json.
-- Install AFTER owned_accounts schema. No startup migration or legacy import.
-- Canonical publisher identity lives ONLY in owned_publishers.
CREATE SCHEMA owned_taxonomy;
CREATE TABLE owned_taxonomy.posts (
 id uuid PRIMARY KEY,
 created_at timestamptz NOT NULL,
 drafted_at timestamptz NULL,
 visibility integer NOT NULL,
 deleted_at timestamptz NULL
);
CREATE TABLE owned_taxonomy.post_categories (
 id uuid PRIMARY KEY,
 slug varchar(128) NOT NULL,
 name varchar(256) NULL,
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 deleted_at timestamptz NULL
);
CREATE TABLE owned_taxonomy.post_tags (
 id uuid PRIMARY KEY,
 slug varchar(128) NOT NULL,
 name varchar(256) NULL,
 description varchar(4096) NULL,
 owner_publisher_id uuid NULL REFERENCES owned_publishers(id) ON DELETE SET NULL,
 is_protected boolean NOT NULL,
 is_event boolean NOT NULL,
 event_ends_at timestamptz NULL,
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 deleted_at timestamptz NULL
);
CREATE TABLE owned_taxonomy.post_category_links (
 categories_id uuid NOT NULL REFERENCES owned_taxonomy.post_categories(id) ON DELETE CASCADE,
 posts_id uuid NOT NULL REFERENCES owned_taxonomy.posts(id) ON DELETE CASCADE,
 PRIMARY KEY(categories_id,posts_id)
);
CREATE TABLE owned_taxonomy.post_tag_links (
 posts_id uuid NOT NULL REFERENCES owned_taxonomy.posts(id) ON DELETE CASCADE,
 tags_id uuid NOT NULL REFERENCES owned_taxonomy.post_tags(id) ON DELETE CASCADE,
 PRIMARY KEY(posts_id,tags_id)
);
CREATE TABLE owned_taxonomy.post_category_subscriptions (
 id uuid PRIMARY KEY,
 account_id uuid NOT NULL,
 category_id uuid NULL REFERENCES owned_taxonomy.post_categories(id),
 tag_id uuid NULL REFERENCES owned_taxonomy.post_tags(id),
 collection_id uuid NULL,
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 deleted_at timestamptz NULL
);
-- Independently designed local audit ledger; not an upstream shared schema.
CREATE TABLE owned_taxonomy.actions (
 sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 actor_id uuid NOT NULL,
 is_admin boolean NOT NULL,
 resource_kind text NOT NULL,
 resource_id uuid NOT NULL,
 operation text NOT NULL,
 occurred_at timestamptz NOT NULL
);
