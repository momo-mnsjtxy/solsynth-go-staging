-- SPDX-License-Identifier: AGPL-3.0-only
-- Owned text-room runtime schema. Explicit migration input; never run at startup.
-- Requires internal/authoritypg/schema.sql. Shares canonical chat tables with
-- messagerpg read/sync/unread; existing projection rows stay read-only here.
CREATE TABLE IF NOT EXISTS chat_rooms (
 id uuid PRIMARY KEY,
 is_public boolean NOT NULL,
 encryption_mode integer NOT NULL,
 deleted_at timestamptz
);
CREATE TABLE IF NOT EXISTS chat_members (
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
CREATE TABLE IF NOT EXISTS chat_messages (
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
CREATE TABLE IF NOT EXISTS chat_reactions (
 id uuid PRIMARY KEY,
 message_id uuid NOT NULL REFERENCES chat_messages(id),
 sender_id uuid NOT NULL REFERENCES chat_members(id),
 symbol varchar(256) NOT NULL,
 deleted_at timestamptz
);
CREATE INDEX IF NOT EXISTS messager_fixture_threads ON chat_messages(thread_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS messager_fixture_reactions ON chat_reactions(message_id);

ALTER TABLE chat_rooms ADD COLUMN IF NOT EXISTS owned_owner_id uuid REFERENCES owned_authority_accounts(id);
ALTER TABLE chat_rooms ADD COLUMN IF NOT EXISTS owned_name varchar(1024) NOT NULL DEFAULT '';
ALTER TABLE chat_rooms ADD COLUMN IF NOT EXISTS owned_description varchar(4096) NOT NULL DEFAULT '';
ALTER TABLE chat_rooms ADD COLUMN IF NOT EXISTS owned_is_community boolean NOT NULL DEFAULT false;
ALTER TABLE chat_rooms ADD COLUMN IF NOT EXISTS owned_read_receipts_public boolean NOT NULL DEFAULT false;
ALTER TABLE chat_rooms ADD COLUMN IF NOT EXISTS owned_created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP;
ALTER TABLE chat_rooms ADD COLUMN IF NOT EXISTS owned_updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP;
ALTER TABLE chat_rooms ADD COLUMN IF NOT EXISTS owned_next_sequence bigint NOT NULL DEFAULT 0 CHECK(owned_next_sequence>=0);
ALTER TABLE chat_rooms ADD COLUMN IF NOT EXISTS owned_relation_guard boolean NOT NULL DEFAULT false;
ALTER TABLE chat_rooms ADD COLUMN IF NOT EXISTS owned_type integer NOT NULL DEFAULT 0 CHECK(owned_type IN(0,1));
ALTER TABLE chat_members ADD COLUMN IF NOT EXISTS owned_banned boolean NOT NULL DEFAULT false;
ALTER TABLE chat_members ADD COLUMN IF NOT EXISTS owned_updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP;
ALTER TABLE chat_members ADD COLUMN IF NOT EXISTS owned_confirmed_at timestamptz;
ALTER TABLE chat_members ADD COLUMN IF NOT EXISTS owned_timeout_until timestamptz;
ALTER TABLE chat_members ADD COLUMN IF NOT EXISTS owned_timeout_reason text CHECK(length(owned_timeout_reason)<=4096);
ALTER TABLE chat_members ADD COLUMN IF NOT EXISTS owned_timeout_sender uuid REFERENCES chat_members(id);
ALTER TABLE chat_members ADD COLUMN IF NOT EXISTS owned_timeout_since timestamptz;
ALTER TABLE chat_messages ADD COLUMN IF NOT EXISTS owned_request_hash bytea;
CREATE UNIQUE INDEX IF NOT EXISTS owned_chat_text_idempotency ON chat_messages(chat_room_id,sender_id,client_message_id)
 WHERE type='text' AND client_message_id IS NOT NULL AND owned_request_hash IS NOT NULL;
CREATE INDEX IF NOT EXISTS owned_chat_joined ON chat_members(account_id,chat_room_id) WHERE joined_at IS NOT NULL AND leave_at IS NULL AND deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS owned_chat_invites ON chat_members(account_id,chat_room_id) WHERE joined_at IS NULL AND leave_at IS NULL AND deleted_at IS NULL;

-- Attitude is an opaque client integer; no shared enum mapping is inferred.
ALTER TABLE chat_reactions ADD COLUMN IF NOT EXISTS owned_attitude integer NOT NULL DEFAULT 0;
ALTER TABLE chat_reactions ADD COLUMN IF NOT EXISTS owned_created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP;
ALTER TABLE chat_reactions ADD COLUMN IF NOT EXISTS owned_updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP;
CREATE INDEX IF NOT EXISTS owned_chat_reaction_lookup ON chat_reactions(message_id,sender_id,symbol) WHERE deleted_at IS NULL;

-- Keep the original send request identity when a caller replaces its edit
-- correlation ID. Existing rows continue to resolve by their original column.
ALTER TABLE chat_messages ADD COLUMN IF NOT EXISTS owned_creation_client_id varchar(128);
CREATE UNIQUE INDEX IF NOT EXISTS owned_chat_creation_idempotency
 ON chat_messages(chat_room_id,sender_id,coalesce(owned_creation_client_id,client_message_id))
 WHERE type='text' AND owned_request_hash IS NOT NULL;

-- Server-authored explicit redirect snapshots; ordinary metadata is not proof.
CREATE TABLE IF NOT EXISTS owned_chat_redirects (
 message_id uuid PRIMARY KEY REFERENCES chat_messages(id),actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 destination_room_id uuid NOT NULL REFERENCES chat_rooms(id),destination_member_id uuid NOT NULL REFERENCES chat_members(id),destination_joined_at timestamptz NOT NULL,
 source_room_id uuid NOT NULL REFERENCES chat_rooms(id),source_member_id uuid NOT NULL REFERENCES chat_members(id),source_joined_at timestamptz NOT NULL,
 source_ids jsonb NOT NULL CHECK(jsonb_typeof(source_ids)='array'),snapshot jsonb NOT NULL CHECK(jsonb_typeof(snapshot)='object'),
 request_key text,request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),created_at timestamptz NOT NULL,
 UNIQUE(actor_id,destination_room_id,request_key)
);

-- Source attribution and the explicit copy cannot be rewritten by later edits.
CREATE OR REPLACE FUNCTION owned_chat_redirects_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'chat redirect receipt is immutable'; END; $$;
DROP TRIGGER IF EXISTS owned_chat_redirects_immutable ON owned_chat_redirects;
CREATE TRIGGER owned_chat_redirects_immutable BEFORE UPDATE ON owned_chat_redirects
 FOR EACH ROW EXECUTE FUNCTION owned_chat_redirects_immutable();

-- Authored shareable room slug and member nickname continuation. The scope key
-- tracks the canonical room realm association in the same room transaction.
ALTER TABLE chat_rooms ADD COLUMN IF NOT EXISTS owned_slug varchar(128);
ALTER TABLE chat_rooms ADD COLUMN IF NOT EXISTS owned_slug_realm_id uuid;
DO $$ BEGIN IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='chat_rooms'::regclass AND conname='owned_chat_slug_shape') THEN ALTER TABLE chat_rooms ADD CONSTRAINT owned_chat_slug_shape CHECK(owned_slug IS NULL OR owned_slug ~ '^[a-z0-9]([a-z0-9_.-]*[a-z0-9])?$'); END IF; END $$;
CREATE UNIQUE INDEX IF NOT EXISTS owned_chat_owner_slug ON chat_rooms(owned_owner_id,owned_slug) WHERE owned_slug IS NOT NULL AND owned_slug_realm_id IS NULL AND deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS owned_chat_realm_slug ON chat_rooms(owned_slug_realm_id,owned_slug) WHERE owned_slug IS NOT NULL AND owned_slug_realm_id IS NOT NULL AND deleted_at IS NULL;
ALTER TABLE chat_members ADD COLUMN IF NOT EXISTS owned_nick varchar(1024);
CREATE TABLE IF NOT EXISTS owned_chat_detail_audit(
 id bigserial PRIMARY KEY, actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 room_id uuid NOT NULL REFERENCES chat_rooms(id), member_id uuid REFERENCES chat_members(id),
 message_id uuid REFERENCES chat_messages(id), action text NOT NULL, reason varchar(4096), occurred_at timestamptz NOT NULL
);
