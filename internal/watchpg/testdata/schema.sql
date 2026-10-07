-- SPDX-License-Identifier: AGPL-3.0-only
-- Disposable-test schema translated from licensed migration 20260927043440.
-- Do not run on an existing deployment. There is deliberately no DROP, ALTER,
-- IF NOT EXISTS, seed, or automatic startup migration in the adapter.
CREATE TABLE post_watch_preferences (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    source integer NOT NULL,
    notify_reactions boolean NOT NULL,
    notify_replies boolean NOT NULL,
    notify_chains boolean NOT NULL,
    notify_forwards boolean NOT NULL,
    notify_edits boolean NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone NULL,
    CONSTRAINT pk_post_watch_preferences PRIMARY KEY (id)
);
CREATE UNIQUE INDEX ix_post_watch_preferences_account_id_source_deleted_at
    ON post_watch_preferences (account_id, source, deleted_at);
