-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit owned schema. Install after owned_authority_accounts. No auto-seed.
CREATE TABLE owned_media_quotas (
 account_id uuid PRIMARY KEY REFERENCES owned_authority_accounts(id),
 limit_bytes bigint NOT NULL CHECK(limit_bytes >= 0),
 base_limit_bytes bigint CHECK(base_limit_bytes>=0),
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
CREATE TABLE owned_media_pools(id uuid PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),name text NOT NULL CHECK(length(name) BETWEEN 1 AND 255),description text NOT NULL DEFAULT '' CHECK(length(description)<=4096),is_public boolean NOT NULL DEFAULT false,allow_encryption boolean NOT NULL DEFAULT false,max_file_bytes bigint NOT NULL CHECK(max_file_bytes>0));
CREATE TABLE owned_media_folders(id uuid PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),name text NOT NULL CHECK(length(name) BETWEEN 1 AND 255),parent_id uuid REFERENCES owned_media_folders(id),created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,deleted_at timestamptz,CHECK(parent_id IS DISTINCT FROM id));
CREATE TABLE owned_media_bundles(id uuid PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),name text NOT NULL CHECK(length(name)<=255),description text NOT NULL CHECK(length(description)<=8192),is_public boolean NOT NULL DEFAULT false,passcode_salt bytea,passcode_hash bytea,expired_at timestamptz,created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,deleted_at timestamptz,CHECK((passcode_salt IS NULL)=(passcode_hash IS NULL)));
CREATE TABLE owned_media_bundle_attempts(account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),bundle_id uuid NOT NULL REFERENCES owned_media_bundles(id) ON DELETE CASCADE,window_start timestamptz NOT NULL,attempts integer NOT NULL CHECK(attempts BETWEEN 1 AND 10),PRIMARY KEY(account_id,bundle_id));
CREATE TABLE owned_media_details(file_id uuid PRIMARY KEY REFERENCES owned_media_files(id),options jsonb NOT NULL DEFAULT '{}',options_sha256 char(64) NOT NULL CHECK(options_sha256 ~ '^[0-9a-f]{64}$'),parent_id uuid REFERENCES owned_media_folders(id),pool_id uuid REFERENCES owned_media_pools(id),bundle_id uuid REFERENCES owned_media_bundles(id),object_key uuid NOT NULL,indexed boolean NOT NULL DEFAULT true,user_meta jsonb NOT NULL DEFAULT '{}',sensitive_marks jsonb NOT NULL DEFAULT '[]',expired_at timestamptz);
CREATE INDEX owned_media_object_references ON owned_media_details(object_key,pool_id);
CREATE INDEX owned_media_ready_expiry ON owned_media_details(expired_at,file_id) WHERE expired_at IS NOT NULL;
CREATE TABLE owned_media_compositions(id uuid PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),image_id uuid NOT NULL REFERENCES owned_media_files(id),video_id uuid NOT NULL REFERENCES owned_media_files(id),created_at timestamptz NOT NULL,deleted_at timestamptz,CHECK(image_id<>video_id),UNIQUE(account_id,image_id,video_id));
CREATE TABLE owned_media_quota_records(id uuid PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_media_quotas(account_id),source_key text NOT NULL CHECK(length(source_key) BETWEEN 1 AND 128),name text NOT NULL CHECK(length(name) BETWEEN 1 AND 255),description text NOT NULL CHECK(length(description)<=4096),quota_bytes bigint NOT NULL CHECK(quota_bytes BETWEEN 1 AND 1099511627776),expired_at timestamptz,created_at timestamptz NOT NULL,UNIQUE(account_id,source_key));
