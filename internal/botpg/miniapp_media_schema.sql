-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit owned upgrade; existing public-only package links gain no private grant.
ALTER TABLE owned_bot_miniapps ADD COLUMN IF NOT EXISTS package_owner_id uuid REFERENCES owned_authority_accounts(id);
CREATE TABLE IF NOT EXISTS owned_miniapp_media (
 miniapp_id uuid NOT NULL REFERENCES owned_bot_miniapps(id),
 slot text NOT NULL CHECK(slot IN('icon','background')),
 file_id uuid NOT NULL REFERENCES owned_media_files(id),
 assigned_by uuid NOT NULL REFERENCES owned_authority_accounts(id),
 attached_at timestamptz NOT NULL,
 PRIMARY KEY(miniapp_id,slot)
);
CREATE INDEX IF NOT EXISTS owned_miniapp_media_file ON owned_miniapp_media(file_id,miniapp_id);
