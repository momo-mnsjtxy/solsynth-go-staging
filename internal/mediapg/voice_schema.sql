-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit additive schema after canonical chat and media tables.
-- Clip IDs are owned canonical media IDs, never remote locations.
CREATE TABLE owned_chat_voice_clips (
 id uuid PRIMARY KEY REFERENCES owned_media_files(id),
 room_id uuid NOT NULL REFERENCES chat_rooms(id),
 sender_id uuid NOT NULL REFERENCES chat_members(id),
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 sender_joined_at timestamptz NOT NULL,
 message_id uuid UNIQUE REFERENCES chat_messages(id),
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 duration_ms integer CHECK(duration_ms BETWEEN 0 AND 86400000),
 created_at timestamptz NOT NULL,
 pending_expires_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 CHECK(expires_at>created_at)
);
CREATE INDEX owned_chat_voice_expiry ON owned_chat_voice_clips(expires_at,id);
CREATE INDEX owned_chat_voice_pending ON owned_chat_voice_clips(pending_expires_at,id) WHERE message_id IS NULL;
