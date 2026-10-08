-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit installation after canonical chat and media; constructors do no DDL.
CREATE TABLE IF NOT EXISTS owned_chat_room_media (
 room_id uuid NOT NULL REFERENCES chat_rooms(id),
 slot text NOT NULL CHECK(slot IN('picture','background')),
 file_id uuid NOT NULL REFERENCES owned_media_files(id),
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 attached_at timestamptz NOT NULL,
 PRIMARY KEY(room_id,slot)
);
CREATE INDEX IF NOT EXISTS owned_chat_room_media_file ON owned_chat_room_media(file_id,room_id);
