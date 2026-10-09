-- SPDX-License-Identifier: AGPL-3.0-only
-- Disposable, SYNTHETIC projection fixture only. Never a production migration.
-- Table/column types pinned in ../schema-evidence.json; omitted unrelated fields
-- and explicit fixture defaults do NOT establish shared model defaults.
CREATE TABLE chat_rooms (
 id uuid PRIMARY KEY,
 is_public boolean NOT NULL,
 encryption_mode integer NOT NULL,
 deleted_at timestamptz
);
CREATE TABLE chat_members (
 id uuid PRIMARY KEY,
 chat_room_id uuid NOT NULL REFERENCES chat_rooms(id),
 account_id uuid NOT NULL,
 notify integer NOT NULL DEFAULT -2147483648,
 last_read_at timestamptz,
 joined_at timestamptz,
 leave_at timestamptz,
 deleted_at timestamptz,
 UNIQUE(chat_room_id,account_id)
);
CREATE TABLE chat_messages (
 id uuid PRIMARY KEY,
 chat_room_id uuid NOT NULL REFERENCES chat_rooms(id),
 sender_id uuid NOT NULL REFERENCES chat_members(id),
 type varchar(1024) NOT NULL,
 room_sequence bigint NOT NULL,
 content varchar(4096),
 client_message_id varchar(128),
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 deleted_at timestamptz,
 edited_at timestamptz,
 meta jsonb,
 members_mentioned jsonb,
 attachments jsonb NOT NULL DEFAULT '[]',
 encryption_meta jsonb,
 reactions_count jsonb NOT NULL DEFAULT '{}',
 replied_message_id uuid REFERENCES chat_messages(id),
 forwarded_message_id uuid REFERENCES chat_messages(id),
 thread_id uuid,
 UNIQUE(chat_room_id,room_sequence)
);
CREATE TABLE chat_reactions (
 id uuid PRIMARY KEY,
 message_id uuid NOT NULL REFERENCES chat_messages(id),
 sender_id uuid NOT NULL REFERENCES chat_members(id),
 symbol varchar(256) NOT NULL,
 deleted_at timestamptz
);
CREATE INDEX messager_fixture_threads ON chat_messages(thread_id) WHERE deleted_at IS NULL;
CREATE INDEX messager_fixture_reactions ON chat_reactions(message_id);
