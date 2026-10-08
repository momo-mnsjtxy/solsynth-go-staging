-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit additive input after canonical media schema; constructors never migrate.
CREATE TABLE owned_presence_artworks (
 hash varchar(71) PRIMARY KEY CHECK(hash ~ '^sha256:[0-9a-f]{64}$'),
 file_id uuid NOT NULL UNIQUE REFERENCES owned_media_files(id),
 last_referenced_at timestamptz NOT NULL,
 retention_seconds bigint NOT NULL CHECK(retention_seconds BETWEEN 86400 AND 31536000)
);
CREATE INDEX owned_presence_artwork_expiry ON owned_presence_artworks(last_referenced_at,hash);
