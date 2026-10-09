-- SPDX-License-Identifier: AGPL-3.0-only
-- Install explicitly after canonical ticket and media schemas. Links only.
CREATE TABLE IF NOT EXISTS owned_ticket_media (
 message_id uuid NOT NULL REFERENCES owned_ticket_messages(id),
 file_id uuid NOT NULL REFERENCES owned_media_files(id),
 position integer NOT NULL CHECK(position BETWEEN 0 AND 31),
 PRIMARY KEY(message_id,file_id),
 UNIQUE(message_id,position)
);
CREATE INDEX IF NOT EXISTS owned_ticket_media_file ON owned_ticket_media(file_id,message_id);
