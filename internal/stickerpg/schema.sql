-- SPDX-License-Identifier: AGPL-3.0-only
-- Canonical publisher/media references; no asset bytes or inherited schema claims.
CREATE TABLE IF NOT EXISTS owned_sticker_packs (
 id uuid PRIMARY KEY,publisher_id uuid NOT NULL REFERENCES owned_publishers(id),
 name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 1024),description text NOT NULL CHECK(length(description)<=4096),
 prefix text NOT NULL CHECK(prefix ~ '^[a-zA-Z0-9]{1,128}$'),icon_id uuid REFERENCES owned_media_files(id),
 created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,deleted_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS owned_sticker_pack_prefix ON owned_sticker_packs(lower(prefix));
CREATE TABLE IF NOT EXISTS owned_stickers (
 id uuid PRIMARY KEY,pack_id uuid NOT NULL REFERENCES owned_sticker_packs(id),slug text NOT NULL CHECK(slug ~ '^[a-zA-Z0-9]{1,128}$'),
 name text NOT NULL CHECK(length(name)<=1024),image_id uuid NOT NULL REFERENCES owned_media_files(id),
 size integer NOT NULL CHECK(size BETWEEN 0 AND 3),mode integer NOT NULL CHECK(mode IN(0,1)),ordering integer NOT NULL,
 created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,deleted_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS owned_sticker_slug ON owned_stickers(pack_id,lower(slug));
CREATE INDEX IF NOT EXISTS owned_sticker_content ON owned_stickers(pack_id,ordering,id) WHERE deleted_at IS NULL;
CREATE TABLE IF NOT EXISTS owned_sticker_ownerships (
 id uuid PRIMARY KEY,pack_id uuid NOT NULL REFERENCES owned_sticker_packs(id),account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 ordering integer NOT NULL DEFAULT 0,created_at timestamptz NOT NULL,UNIQUE(pack_id,account_id)
);
CREATE TABLE IF NOT EXISTS owned_sticker_audit (
 id bigserial PRIMARY KEY,actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),pack_id uuid NOT NULL REFERENCES owned_sticker_packs(id),
 action text NOT NULL,occurred_at timestamptz NOT NULL
);
