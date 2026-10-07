-- SPDX-License-Identifier: AGPL-3.0-only
-- Synthetic fixture matching the licensed DysonNetwork PostWatch table contract.
-- This is test setup only, not a production migration.
CREATE TABLE post_watch_preferences (
 id uuid NOT NULL,
 account_id uuid NOT NULL,
 source integer NOT NULL,
 notify_reactions boolean NOT NULL,
 notify_replies boolean NOT NULL,
 notify_chains boolean NOT NULL,
 notify_forwards boolean NOT NULL,
 notify_edits boolean NOT NULL,
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 deleted_at timestamptz NULL,
 CONSTRAINT pk_post_watch_preferences PRIMARY KEY (id)
);
CREATE UNIQUE INDEX ix_post_watch_preferences_account_id_source_deleted_at
 ON post_watch_preferences(account_id, source, deleted_at);
