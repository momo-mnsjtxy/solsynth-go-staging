-- SPDX-License-Identifier: AGPL-3.0-only
-- Synthetic projected schema only, NOT a production migration.
-- Types/relations from licensed Develop AppDatabaseModelSnapshot.cs and
-- Develop Models/MiniApp.cs at 4ee2d0cebb9da8284a92483fee6dce795bf6947c.
-- Unrelated required columns omitted; no complete global filter parity implied.
CREATE TABLE developers (
 id uuid PRIMARY KEY,
 publisher_id uuid NOT NULL
);
CREATE TABLE dev_projects (
 id uuid PRIMARY KEY,
 developer_id uuid NOT NULL REFERENCES developers(id),
 deleted_at timestamp with time zone
);
CREATE TABLE mini_apps (
 id uuid PRIMARY KEY,
 slug varchar(1024) NOT NULL,
 stage integer NOT NULL,
 project_id uuid NOT NULL REFERENCES dev_projects(id),
 package_storage_key varchar(512),
 package_url varchar(2048),
 download_count bigint NOT NULL DEFAULT 0,
 deleted_at timestamp with time zone
);
