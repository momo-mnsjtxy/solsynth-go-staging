-- SPDX-License-Identifier: AGPL-3.0-only
-- Additive owned lifecycle state. Apply explicitly after chat schema.
CREATE UNIQUE INDEX owned_chat_active_placeholder ON chat_messages(chat_room_id,sender_id) WHERE type='placeholder' AND deleted_at IS NULL;
CREATE TABLE owned_chat_placeholder_finalizations (
 message_id uuid PRIMARY KEY REFERENCES chat_messages(id),
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 finalized_at timestamptz NOT NULL
);
CREATE INDEX owned_chat_placeholder_expiry ON chat_messages(chat_room_id,id) WHERE type='placeholder' AND deleted_at IS NULL;
