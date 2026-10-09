-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit owned schema. Install after owned_authority_accounts. No auto-seed.
CREATE TABLE owned_media_quotas (
 account_id uuid PRIMARY KEY REFERENCES owned_authority_accounts(id),
 limit_bytes bigint NOT NULL CHECK(limit_bytes >= 0),
 used_bytes bigint NOT NULL DEFAULT 0 CHECK(used_bytes >= 0),
 reserved_bytes bigint NOT NULL DEFAULT 0 CHECK(reserved_bytes >= 0),
 CHECK(used_bytes <= limit_bytes AND reserved_bytes <= limit_bytes-used_bytes)
);
CREATE TABLE owned_media_files (
 id uuid PRIMARY KEY,
 account_id uuid NOT NULL REFERENCES owned_media_quotas(account_id),
 idempotency_key varchar(128) NOT NULL,
 name varchar(255) NOT NULL,
 mime_type varchar(255) NOT NULL,
 usage text NOT NULL DEFAULT '' CHECK(usage IN ('','post','profile_picture','ticket')),
 size bigint NOT NULL CHECK(size >= 0),
 sha256 char(64) NOT NULL CHECK(sha256 ~ '^[0-9a-f]{64}$'),
 is_public boolean NOT NULL DEFAULT false,
 state text NOT NULL CHECK(state IN ('pending','ready','deleting','deleted')),
 charge text NOT NULL CHECK(charge IN ('reserved','used','none')),
 expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 UNIQUE(account_id,idempotency_key),
 CHECK((state='pending' AND charge='reserved') OR (state='ready' AND charge='used') OR (state='deleting' AND charge IN ('reserved','used')) OR (state='deleted' AND charge='none'))
);
CREATE INDEX owned_media_cleanup ON owned_media_files(state,expires_at,id) WHERE state IN ('pending','deleting');
CREATE INDEX owned_media_owner ON owned_media_files(account_id,created_at,id);
