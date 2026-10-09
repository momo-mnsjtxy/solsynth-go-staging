-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicitly install after canonical postpg and mediapg schemas. These are
-- references only: no copied file metadata, bytes, quota, or identity authority.
CREATE TABLE IF NOT EXISTS owned_post_media (
 post_id uuid NOT NULL REFERENCES owned_text_posts(id),
 file_id uuid NOT NULL REFERENCES owned_media_files(id),
 position smallint NOT NULL CHECK(position>=0 AND position<32),
 owner_authorized boolean NOT NULL,
 created_at timestamptz NOT NULL,
 PRIMARY KEY(post_id,file_id),
 UNIQUE(post_id,position)
);
CREATE INDEX IF NOT EXISTS owned_post_media_file ON owned_post_media(file_id,post_id);
