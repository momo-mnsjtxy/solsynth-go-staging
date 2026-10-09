-- SPDX-License-Identifier: AGPL-3.0-only
-- Independently owned canonical file associations; requires chatpg and mediapg.
CREATE TABLE IF NOT EXISTS owned_chat_media (
 message_id uuid NOT NULL REFERENCES chat_messages(id),
 file_id uuid NOT NULL REFERENCES owned_media_files(id),
 position integer NOT NULL CHECK(position>=0 AND position<32),
 owner_authorized boolean NOT NULL,
 created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY(message_id,file_id),
 UNIQUE(message_id,position)
);
CREATE INDEX IF NOT EXISTS owned_chat_media_file ON owned_chat_media(file_id,message_id);
