-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit optional migration after canonical authority and livestream schemas.
CREATE TABLE IF NOT EXISTS owned_livestream_outputs(
 id uuid PRIMARY KEY,stream_id uuid NOT NULL,generation bigint NOT NULL,
 kind text NOT NULL CHECK(kind IN('egress','hls')),
 provider text NOT NULL,endpoint text NOT NULL,room_name varchar(256) NOT NULL,
 actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 session_id uuid NOT NULL REFERENCES owned_authority_sessions(id),
 account_version bigint NOT NULL CHECK(account_version>0),authorization_epoch bigint NOT NULL CHECK(authorization_epoch>0),
 owner_id uuid NOT NULL REFERENCES owned_authority_accounts(id),owner_version bigint NOT NULL CHECK(owner_version>0),
 permission text NOT NULL CHECK(permission IN('livestreams.egress','livestreams.hls')),
 destinations jsonb NOT NULL CHECK(jsonb_typeof(destinations)='array' AND jsonb_array_length(destinations) BETWEEN 1 AND 8),
 policy_hash bytea NOT NULL CHECK(octet_length(policy_hash)=32),request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),key_hash bytea CHECK(key_hash IS NULL OR octet_length(key_hash)=32),
 playlist_name varchar(128) NOT NULL,segment_duration integer NOT NULL,layout varchar(64) NOT NULL,
 relay boolean NOT NULL,egress_id varchar(128),
 phase text NOT NULL CHECK(phase IN('attempted','active','uncertain','stopping','done')),
 reason varchar(64) NOT NULL DEFAULT '',created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,
 started_at timestamptz,finished_at timestamptz,
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 30),next_attempt_at timestamptz NOT NULL,
 FOREIGN KEY(stream_id,generation) REFERENCES owned_livestream_runs(stream_id,generation),
 CHECK((phase='done')=(finished_at IS NOT NULL)),
 CHECK(kind<>'hls' OR segment_duration BETWEEN 2 AND 30)
);
CREATE UNIQUE INDEX IF NOT EXISTS owned_livestream_output_slot ON owned_livestream_outputs(stream_id,kind) WHERE phase<>'done';
CREATE UNIQUE INDEX IF NOT EXISTS owned_livestream_output_provider_id ON owned_livestream_outputs(provider,endpoint,egress_id) WHERE egress_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS owned_livestream_output_key ON owned_livestream_outputs(stream_id,actor_id,key_hash) WHERE key_hash IS NOT NULL;
CREATE INDEX IF NOT EXISTS owned_livestream_output_due ON owned_livestream_outputs(provider,endpoint,next_attempt_at,id) WHERE phase<>'done';
CREATE TABLE IF NOT EXISTS owned_livestream_archive_imports(
 id uuid PRIMARY KEY,stream_id uuid NOT NULL,output_id uuid NOT NULL REFERENCES owned_livestream_outputs(id),
 actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),session_id uuid NOT NULL REFERENCES owned_authority_sessions(id),
 account_version bigint NOT NULL CHECK(account_version>0),authorization_epoch bigint NOT NULL CHECK(authorization_epoch>0),
 key_hash bytea NOT NULL CHECK(octet_length(key_hash)=32),request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),playlist_sha256 char(64) NOT NULL CHECK(playlist_sha256~'^[0-9a-f]{64}$'),
 state text NOT NULL CHECK(state IN('pending','processing','complete','failed')),media_id uuid REFERENCES owned_media_files(id),source_sha256 char(64) CHECK(source_sha256 IS NULL OR source_sha256~'^[0-9a-f]{64}$'),reason varchar(64) NOT NULL DEFAULT '',
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 30),next_attempt_at timestamptz NOT NULL,
 lease_token uuid,lease_expires_at timestamptz,
 created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,
 FOREIGN KEY(stream_id)REFERENCES owned_livestreams(id),UNIQUE(stream_id,actor_id,key_hash),
 CHECK((state='complete')=(media_id IS NOT NULL)),
 CHECK((state='processing')=(lease_token IS NOT NULL AND lease_expires_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS owned_livestream_archive_import_due ON owned_livestream_archive_imports(next_attempt_at,lease_expires_at,id) WHERE state IN('pending','processing');
CREATE TABLE IF NOT EXISTS owned_livestream_archive_media(
 job_id uuid PRIMARY KEY REFERENCES owned_livestream_archive_imports(id),media_id uuid NOT NULL UNIQUE REFERENCES owned_media_files(id),
 actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),source_sha256 char(64) NOT NULL CHECK(source_sha256~'^[0-9a-f]{64}$'),created_at timestamptz NOT NULL
);
