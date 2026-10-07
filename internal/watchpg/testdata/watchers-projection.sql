-- SPDX-License-Identifier: AGPL-3.0-only
-- SYNTHETIC PROJECTION ONLY, for a fresh disposable database.
-- This is NOT the full upstream schema and is NOT a deployment migration.
-- Column names/types/nullability below are observed in the licensed pinned
-- AppDatabaseModelSnapshot.cs. Many production columns, defaults, foreign keys,
-- indexes and constraints are intentionally absent from this test projection.
CREATE TABLE publishers (
    id uuid PRIMARY KEY,
    account_id uuid NULL,
    deleted_at timestamp with time zone NULL
);
CREATE TABLE posts (
    id uuid PRIMARY KEY,
    publisher_id uuid NOT NULL,
    replied_post_id uuid NULL,
    chained_post_id uuid NULL,
    deleted_at timestamp with time zone NULL
);
CREATE TABLE post_bookmarks (
    id uuid PRIMARY KEY,
    post_id uuid NOT NULL,
    account_id uuid NOT NULL,
    deleted_at timestamp with time zone NULL
);
CREATE TABLE post_reactions (
    id uuid PRIMARY KEY,
    post_id uuid NOT NULL,
    account_id uuid NULL,
    deleted_at timestamp with time zone NULL
);
