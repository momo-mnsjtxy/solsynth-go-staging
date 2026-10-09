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
