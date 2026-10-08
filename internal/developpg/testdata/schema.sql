-- SPDX-License-Identifier: AGPL-3.0-only
-- Synthetic test projections, NOT a migration or complete production schema.
-- Columns/types from DysonNetwork-owned Develop AppDatabaseModelSnapshot.cs
-- at 4ee2d0cebb9da8284a92483fee6dce795bf6947c: SnCustomApp (453-527),
-- SnAppProduct (145-223), SnBoardWidget (280-361).
-- Required unrelated source columns/relations are deliberately omitted.
-- No status enum values or shared model/global-filter definitions are inferred.
CREATE TABLE custom_apps (
 id uuid PRIMARY KEY,
 slug varchar(1024) NOT NULL,
 name varchar(1024) NOT NULL,
 description varchar(4096),
 status integer NOT NULL,
 deleted_at timestamp with time zone
);
CREATE TABLE app_products (
 id uuid PRIMARY KEY,
 app_id uuid NOT NULL,
 deleted_at timestamp with time zone
);
CREATE TABLE board_widgets (
 id uuid PRIMARY KEY,
 app_id uuid NOT NULL,
 is_enabled boolean NOT NULL,
 deleted_at timestamp with time zone
);
