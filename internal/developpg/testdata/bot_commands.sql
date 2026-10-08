-- SPDX-License-Identifier: AGPL-3.0-only
-- Synthetic disposable projection of Develop-owned migration snapshot.
-- Requires packages.sql's dev_projects relation; not a production migration.
CREATE TABLE bot_accounts (
 id uuid PRIMARY KEY,
 slug varchar(1024) NOT NULL,
 is_active boolean NOT NULL,
 project_id uuid NOT NULL REFERENCES dev_projects(id),
 deleted_at timestamp with time zone
);
CREATE TABLE bot_chat_configs (
 id uuid PRIMARY KEY,
 commands jsonb NOT NULL,
 deleted_at timestamp with time zone
);
