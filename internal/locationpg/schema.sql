-- SPDX-License-Identifier: AGPL-3.0-only
CREATE TABLE owned_location_geocode_limits(account_id uuid PRIMARY KEY REFERENCES owned_authority_accounts(id),last_at timestamptz NOT NULL);
-- Owned POINT-only schema after authority/account/social schemas. No PostGIS/provider dependency.
CREATE TABLE IF NOT EXISTS owned_meets (
 id uuid PRIMARY KEY,host_id uuid NOT NULL REFERENCES owned_authority_accounts(id),status integer NOT NULL CHECK(status BETWEEN 0 AND 3),visibility integer NOT NULL CHECK(visibility BETWEEN 0 AND 2),
 notes text CHECK(length(notes)<=8192),location_name text CHECK(length(location_name)<=256),location_address text CHECK(length(location_address)<=1024),image_id uuid,
 longitude double precision,latitude double precision,metadata jsonb NOT NULL DEFAULT '{}',expires_at timestamptz NOT NULL,completed_at timestamptz,created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,deleted_at timestamptz,
 CHECK((longitude IS NULL)=(latitude IS NULL)),CHECK(longitude BETWEEN -180 AND 180),CHECK(latitude BETWEEN -90 AND 90)
);
CREATE TABLE IF NOT EXISTS owned_meet_participants(id uuid PRIMARY KEY,meet_id uuid NOT NULL REFERENCES owned_meets(id),account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),joined_at timestamptz NOT NULL,UNIQUE(meet_id,account_id));
CREATE TABLE IF NOT EXISTS owned_location_pins(
 id uuid PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),session_id uuid NOT NULL REFERENCES owned_authority_sessions(id),meet_id uuid REFERENCES owned_meets(id),
 visibility integer NOT NULL CHECK(visibility BETWEEN 0 AND 2),status integer NOT NULL CHECK(status BETWEEN 0 AND 2),location_name text CHECK(length(location_name)<=256),location_address text CHECK(length(location_address)<=1024),longitude double precision,latitude double precision,
 metadata jsonb NOT NULL DEFAULT '{}',keep_on_disconnect boolean NOT NULL,last_heartbeat_at timestamptz NOT NULL,expires_at timestamptz,created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,deleted_at timestamptz,
 CHECK((longitude IS NULL)=(latitude IS NULL)),CHECK(longitude BETWEEN -180 AND 180),CHECK(latitude BETWEEN -90 AND 90)
);
CREATE UNIQUE INDEX IF NOT EXISTS owned_location_pin_active ON owned_location_pins(account_id,COALESCE(meet_id,'00000000-0000-0000-0000-000000000000'::uuid)) WHERE status=0 AND deleted_at IS NULL;
CREATE TABLE IF NOT EXISTS owned_nfc_tags(id uuid PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),uid text NOT NULL UNIQUE CHECK(uid ~ '^[A-F0-9]{8,32}$'),label text CHECK(length(label)<=64),is_active boolean NOT NULL,created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,deleted_at timestamptz);
CREATE TABLE IF NOT EXISTS owned_location_audit(id bigserial PRIMARY KEY,actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),resource_id uuid NOT NULL,action text NOT NULL,occurred_at timestamptz NOT NULL);
CREATE TABLE IF NOT EXISTS owned_nearby_tokens(
 token_hash char(64) PRIMARY KEY CHECK(token_hash ~ '^[0-9a-f]{64}$'),account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),session_id uuid NOT NULL REFERENCES owned_authority_sessions(id),device_id text NOT NULL CHECK(length(device_id) BETWEEN 1 AND 256),slot bigint NOT NULL CHECK(slot>=0),valid_from timestamptz NOT NULL,valid_to timestamptz NOT NULL CHECK(valid_to>valid_from),friend_only boolean NOT NULL,capabilities integer NOT NULL CHECK(capabilities BETWEEN 0 AND 65535)
);
CREATE INDEX IF NOT EXISTS owned_nearby_expiry ON owned_nearby_tokens(valid_to);
CREATE INDEX IF NOT EXISTS owned_nearby_owner ON owned_nearby_tokens(account_id,device_id,valid_to);
CREATE TABLE IF NOT EXISTS owned_nearby_limits(account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),action text NOT NULL CHECK(action IN('issue','resolve')),last_at timestamptz NOT NULL,PRIMARY KEY(account_id,action));
CREATE TABLE IF NOT EXISTS owned_encrypted_nfc_tags(id uuid PRIMARY KEY,uid text NOT NULL UNIQUE CHECK(uid ~ '^[A-F0-9]{14}$'),account_id uuid REFERENCES owned_authority_accounts(id),key_ciphertext bytea NOT NULL CHECK(octet_length(key_ciphertext)=60),mac_profile text NOT NULL CHECK(mac_profile IN('empty','encrypted-file')),label text CHECK(length(label)<=64),is_active boolean NOT NULL,counter integer NOT NULL DEFAULT -1 CHECK(counter BETWEEN -1 AND 16777215),last_seen_at timestamptz,locked_at timestamptz,lock_proof_sha256 text CHECK(lock_proof_sha256 IS NULL OR lock_proof_sha256~'^[0-9a-f]{64}$'),created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,deleted_at timestamptz);
