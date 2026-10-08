-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit migration after authoritypg, chatpg and notificationpg. No startup DDL.
CREATE SCHEMA IF NOT EXISTS call_v1;
CREATE TABLE IF NOT EXISTS call_v1.calls (
 id uuid PRIMARY KEY, room_id uuid NOT NULL REFERENCES chat_rooms(id),
 sender_id uuid NOT NULL REFERENCES chat_members(id), provider varchar(32) NOT NULL, provider_endpoint varchar(2048) NOT NULL,
 session_id varchar(128) NOT NULL UNIQUE, state varchar(16) NOT NULL CHECK(state IN('preparing','active','ending','ended')),
 created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL, expires_at timestamptz NOT NULL,
 ended_at timestamptz, CHECK((state='ended')=(ended_at IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS call_one_ongoing ON call_v1.calls(room_id) WHERE ended_at IS NULL;
CREATE TABLE IF NOT EXISTS call_v1.members (
 call_id uuid NOT NULL REFERENCES call_v1.calls(id), member_id uuid NOT NULL REFERENCES chat_members(id),
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id), tool varchar(32) NOT NULL,
 identity uuid NOT NULL UNIQUE, session_id uuid NOT NULL REFERENCES owned_authority_sessions(id),
 session_epoch bigint NOT NULL, account_version bigint NOT NULL,
 state varchar(16) NOT NULL CHECK(state IN('admitted','removing','left')),
 admitted_at timestamptz NOT NULL, token_expires_at timestamptz NOT NULL, revoked_at timestamptz,
 ban_until timestamptz, PRIMARY KEY(call_id,member_id,tool)
);
CREATE TABLE IF NOT EXISTS call_v1.events (
 sequence bigserial PRIMARY KEY, id uuid NOT NULL UNIQUE,
 call_id uuid NOT NULL REFERENCES call_v1.calls(id), room_id uuid NOT NULL REFERENCES chat_rooms(id),
 target_id uuid REFERENCES owned_authority_accounts(id), type varchar(64) NOT NULL,
 payload jsonb NOT NULL, created_at timestamptz NOT NULL, projected_at timestamptz
);
CREATE INDEX IF NOT EXISTS call_events_pending ON call_v1.events(sequence) WHERE projected_at IS NULL;
CREATE TABLE IF NOT EXISTS call_v1.timeouts (
 member_id uuid PRIMARY KEY REFERENCES chat_members(id), timeout_until timestamptz NOT NULL,
 actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id), reason varchar(1024) NOT NULL
);
CREATE TABLE IF NOT EXISTS call_v1.invites (
 call_id uuid NOT NULL REFERENCES call_v1.calls(id), actor_id uuid NOT NULL, target_id uuid NOT NULL,
 sent_at timestamptz NOT NULL, PRIMARY KEY(call_id,actor_id,target_id)
);
-- Canonical removal invalidates admission in the same local transaction. Remote
-- completion is asynchronous and is never represented as completed by this trigger.
CREATE OR REPLACE FUNCTION call_v1.member_changed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.joined_at IS NULL OR NEW.leave_at IS NOT NULL OR NEW.deleted_at IS NOT NULL OR NEW.owned_banned THEN
  UPDATE call_v1.members SET state='removing' WHERE member_id=NEW.id AND state='admitted';
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS call_member_changed ON chat_members;
CREATE TRIGGER call_member_changed AFTER UPDATE OF joined_at,leave_at,deleted_at,owned_banned ON chat_members FOR EACH ROW EXECUTE FUNCTION call_v1.member_changed();
CREATE OR REPLACE FUNCTION call_v1.room_changed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.deleted_at IS NOT NULL OR NEW.encryption_mode IS DISTINCT FROM OLD.encryption_mode THEN
  UPDATE call_v1.calls SET state='ending',updated_at=clock_timestamp() WHERE room_id=NEW.id AND ended_at IS NULL;
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS call_room_changed ON chat_rooms;
CREATE TRIGGER call_room_changed AFTER UPDATE OF deleted_at,encryption_mode ON chat_rooms FOR EACH ROW EXECUTE FUNCTION call_v1.room_changed();
