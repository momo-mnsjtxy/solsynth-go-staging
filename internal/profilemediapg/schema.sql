-- SPDX-License-Identifier: AGPL-3.0-only
-- Install after canonical accounts, media and social schemas. References only;
-- canonical ownership, upload state and byte storage remain in owned_media_files.
CREATE TABLE IF NOT EXISTS owned_account_profile_media(
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 slot text NOT NULL CHECK(slot IN('picture','background')),
 file_id uuid NOT NULL REFERENCES owned_media_files(id),
 attached_at timestamptz NOT NULL,
 PRIMARY KEY(account_id,slot)
);
CREATE INDEX IF NOT EXISTS owned_account_profile_media_file ON owned_account_profile_media(file_id,account_id);
