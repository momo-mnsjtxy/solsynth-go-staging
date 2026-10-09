-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit migration after authority, developer apps, bots and canonical chat.
CREATE TABLE IF NOT EXISTS owned_bot_enrollments (
 bot_id uuid NOT NULL REFERENCES owned_bots(id),room_id uuid NOT NULL REFERENCES chat_rooms(id),
 adapter text NOT NULL CHECK(length(adapter) BETWEEN 1 AND 64),adapter_binding text NOT NULL CHECK(length(adapter_binding) BETWEEN 1 AND 128),generation bigint NOT NULL CHECK(generation>0),active boolean NOT NULL,
 issuer_id uuid NOT NULL REFERENCES owned_authority_accounts(id),session_id uuid NOT NULL REFERENCES owned_authority_sessions(id),
 account_version bigint NOT NULL,session_epoch bigint NOT NULL,owner_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 owner_version bigint NOT NULL,issuer_joined_at timestamptz NOT NULL,issuer_room_joined_at timestamptz NOT NULL,bot_version bigint NOT NULL,joined_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,
 PRIMARY KEY(bot_id,room_id)
);
CREATE TABLE IF NOT EXISTS owned_bot_message_outbox (
 id uuid PRIMARY KEY,sequence bigserial UNIQUE,bot_id uuid NOT NULL,room_id uuid NOT NULL,
 generation bigint NOT NULL,adapter text NOT NULL,actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 session_id uuid NOT NULL REFERENCES owned_authority_sessions(id),account_version bigint NOT NULL,session_epoch bigint NOT NULL,
 message_id uuid NOT NULL REFERENCES chat_messages(id),message_updated_at timestamptz NOT NULL,actor_joined_at timestamptz NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN('pending','accepted','cancelled','failed')),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),reason text,
 lease_owner uuid,lease_until timestamptz,
 CHECK((lease_owner IS NULL)=(lease_until IS NULL)),
 created_at timestamptz NOT NULL,next_attempt_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,accepted_at timestamptz,
 UNIQUE(bot_id,message_id),FOREIGN KEY(bot_id,room_id)REFERENCES owned_bot_enrollments(bot_id,room_id)
);
CREATE INDEX IF NOT EXISTS owned_bot_message_due ON owned_bot_message_outbox(next_attempt_at,sequence)WHERE state='pending';
