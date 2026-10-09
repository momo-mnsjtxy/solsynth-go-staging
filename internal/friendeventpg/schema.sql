-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit durable local audience outbox; no external provider or inbox rows.
CREATE TABLE IF NOT EXISTS owned_friend_events(
 id uuid PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 kind text NOT NULL CHECK(kind IN('activity','status')),presence_version bigint NOT NULL CHECK(presence_version>=0),
 activity_id uuid REFERENCES owned_presence_activities(id),revision bigint NOT NULL CHECK(revision>0),
 session_id uuid NOT NULL,account_version bigint NOT NULL CHECK(account_version>0),authorization_epoch bigint NOT NULL CHECK(authorization_epoch>0),occurred_at timestamptz NOT NULL,
 FOREIGN KEY(account_id,session_id)REFERENCES owned_authority_sessions(account_id,id),UNIQUE(activity_id,revision),CHECK((kind='activity')=(activity_id IS NOT NULL))
);
CREATE TABLE IF NOT EXISTS owned_friend_audiences(
 event_id uuid NOT NULL REFERENCES owned_friend_events(id),recipient_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 forward_revision bigint NOT NULL,reverse_revision bigint NOT NULL,processed_at timestamptz,
 PRIMARY KEY(event_id,recipient_id)
);
CREATE INDEX IF NOT EXISTS owned_friend_pending ON owned_friend_audiences(event_id,recipient_id)WHERE processed_at IS NULL;

CREATE TABLE IF NOT EXISTS owned_friend_presence_versions(
 account_id uuid PRIMARY KEY REFERENCES owned_authority_accounts(id),revision bigint NOT NULL CHECK(revision>0)
);
