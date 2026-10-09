-- SPDX-License-Identifier: AGPL-3.0-only
-- Install explicitly after canonical apps, bots and media. No bytes or grants.
CREATE TABLE IF NOT EXISTS owned_developer_media (
 kind text NOT NULL CHECK(kind IN ('app','product','bot')),
 resource_id uuid NOT NULL,
 slot text NOT NULL CHECK(slot IN ('picture','background')),
 file_id uuid NOT NULL REFERENCES owned_media_files(id),
 uploader_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 attached_at timestamptz NOT NULL,
 PRIMARY KEY(kind,resource_id,slot)
);
CREATE INDEX IF NOT EXISTS owned_developer_media_file ON owned_developer_media(file_id);
