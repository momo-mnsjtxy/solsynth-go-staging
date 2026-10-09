-- SPDX-License-Identifier: AGPL-3.0-only
-- Owned extensions; explicit migration, never installed at server startup.
-- Shapes derive from licensed ChatController, ChatPinService and Messager's
-- authored EF snapshot. No excluded shared/protocol implementation is imported.
CREATE TABLE IF NOT EXISTS owned_chat_groups (
 id uuid PRIMARY KEY,
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 name varchar(256) NOT NULL CHECK(length(btrim(name))>0),
 color varchar(32), icon varchar(64), sort_order integer NOT NULL,
 created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
 deleted_at timestamptz,
 UNIQUE(id,account_id)
);
CREATE INDEX IF NOT EXISTS owned_chat_groups_order ON owned_chat_groups(account_id,sort_order,created_at,id) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS owned_chat_member_account ON chat_members(id,account_id);
CREATE TABLE IF NOT EXISTS owned_chat_group_members (
 member_id uuid PRIMARY KEY REFERENCES chat_members(id),
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 group_id uuid NOT NULL,
 updated_at timestamptz NOT NULL,
 FOREIGN KEY(group_id,account_id) REFERENCES owned_chat_groups(id,account_id),
 FOREIGN KEY(member_id,account_id) REFERENCES chat_members(id,account_id)
);
CREATE TABLE IF NOT EXISTS owned_chat_pins (
 id uuid PRIMARY KEY,
 room_id uuid NOT NULL REFERENCES chat_rooms(id),
 message_id uuid NOT NULL REFERENCES chat_messages(id),
 pinned_by_member_id uuid NOT NULL REFERENCES chat_members(id),
 expires_at timestamptz,
 created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
 deleted_at timestamptz
);
-- A tombstone does not block a subsequent explicit pin of the same message.
CREATE UNIQUE INDEX IF NOT EXISTS owned_chat_pins_live ON owned_chat_pins(room_id,message_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS owned_chat_pins_page ON owned_chat_pins(room_id,created_at DESC,id DESC) WHERE deleted_at IS NULL;
CREATE TABLE IF NOT EXISTS owned_chat_read_receipts (
 member_id uuid PRIMARY KEY REFERENCES chat_members(id),
 room_id uuid NOT NULL REFERENCES chat_rooms(id),
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 last_read_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS owned_chat_read_events (
 id uuid PRIMARY KEY,
 member_id uuid NOT NULL REFERENCES chat_members(id),
 room_id uuid NOT NULL REFERENCES chat_rooms(id),
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 last_read_at timestamptz NOT NULL,
 excluded_device_id varchar(256) NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS owned_chat_read_events_order ON owned_chat_read_events(created_at,id);

-- Owned connection and subscription lifecycle, not a legacy Redis/key contract.
-- connection IDs are random public handles, never bearer/device credentials.
CREATE TABLE IF NOT EXISTS owned_chat_connections (
 id uuid PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 session_id uuid NOT NULL,account_version bigint NOT NULL CHECK(account_version>0),
 authorization_epoch bigint NOT NULL CHECK(authorization_epoch>0),mls_device_id varchar(256) NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,closed_at timestamptz,
 FOREIGN KEY(account_id,session_id) REFERENCES owned_authority_sessions(account_id,id)
);
CREATE INDEX IF NOT EXISTS owned_chat_connections_account ON owned_chat_connections(account_id,expires_at);
CREATE TABLE IF NOT EXISTS owned_chat_subscriptions (
 connection_id uuid NOT NULL REFERENCES owned_chat_connections(id),member_id uuid NOT NULL REFERENCES chat_members(id),
 joined_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,PRIMARY KEY(connection_id,member_id)
);
CREATE INDEX IF NOT EXISTS owned_chat_subscriptions_member ON owned_chat_subscriptions(member_id,expires_at);
