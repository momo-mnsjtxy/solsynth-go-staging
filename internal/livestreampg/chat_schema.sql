-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit optional migration after canonical authority/account and livestream.
CREATE TABLE IF NOT EXISTS owned_livestream_chat_messages(
 id uuid PRIMARY KEY,
 stream_id uuid NOT NULL REFERENCES owned_livestreams(id),generation bigint NOT NULL,
 sender_id uuid NOT NULL REFERENCES owned_authority_accounts(id),sender_name varchar(128) NOT NULL,content varchar(4096) NOT NULL,
 timeout_until timestamptz,created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,deleted_at timestamptz,
 request_key_hash bytea,request_hash bytea NOT NULL,
 FOREIGN KEY(stream_id,generation)REFERENCES owned_livestream_runs(stream_id,generation)
);
CREATE UNIQUE INDEX IF NOT EXISTS owned_livestream_chat_request ON owned_livestream_chat_messages(stream_id,sender_id,request_key_hash) WHERE request_key_hash IS NOT NULL;
CREATE INDEX IF NOT EXISTS owned_livestream_chat_history ON owned_livestream_chat_messages(stream_id,created_at DESC,id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS owned_livestream_chat_mutes ON owned_livestream_chat_messages(stream_id,sender_id,timeout_until) WHERE timeout_until IS NOT NULL;
CREATE TABLE IF NOT EXISTS owned_livestream_chat_outbox(
 sequence bigserial PRIMARY KEY,id uuid NOT NULL UNIQUE,
 stream_id uuid NOT NULL REFERENCES owned_livestreams(id),generation bigint NOT NULL,
 actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 session_id uuid NOT NULL REFERENCES owned_authority_sessions(id),account_version bigint NOT NULL,session_epoch bigint NOT NULL,
 permission text NOT NULL CHECK(permission IN('livestreams.chat.send','livestreams.chat.moderate')),message_id uuid REFERENCES owned_livestream_chat_messages(id),
 kind text NOT NULL CHECK(kind IN('message','timeout')),payload bytea NOT NULL CHECK(octet_length(payload)<=15360),
 state text NOT NULL DEFAULT 'pending' CHECK(state IN('pending','accepted','cancelled','failed')),
 request_key_hash bytea,request_hash bytea NOT NULL,
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),created_at timestamptz NOT NULL,next_attempt_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,
 accepted_at timestamptz,reason text,
 FOREIGN KEY(stream_id,generation)REFERENCES owned_livestream_runs(stream_id,generation)
);
CREATE UNIQUE INDEX IF NOT EXISTS owned_livestream_chat_event_request ON owned_livestream_chat_outbox(stream_id,actor_id,kind,request_key_hash) WHERE request_key_hash IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS owned_livestream_chat_message_event ON owned_livestream_chat_outbox(message_id) WHERE kind='message';
CREATE INDEX IF NOT EXISTS owned_livestream_chat_due ON owned_livestream_chat_outbox(next_attempt_at,sequence) WHERE state='pending';
CREATE INDEX IF NOT EXISTS owned_livestream_chat_fifo ON owned_livestream_chat_outbox(stream_id,sequence) WHERE state='pending';
