-- SPDX-License-Identifier: AGPL-3.0-only
-- Isolated synthetic projection for a fresh disposable integration cluster.
-- Not a production migration. Column types/nullability come from the retained
-- Sphere-owned AppDatabaseModelSnapshot.cs. Other columns, defaults, indexes
-- and FK constraints are deliberately omitted. Source publisher_id is NOT NULL
-- even though the controller includes an explicit IS NOT NULL count predicate.
CREATE SCHEMA nodeinfo_candidate;
CREATE TABLE nodeinfo_candidate.publishers (
    id uuid PRIMARY KEY,
    type integer NOT NULL,
    uri character varying(2048) NULL,
    deleted_at timestamp with time zone NULL
);
CREATE TABLE nodeinfo_candidate.posts (
    id uuid PRIMARY KEY,
    publisher_id uuid NOT NULL,
    deleted_at timestamp with time zone NULL
);
