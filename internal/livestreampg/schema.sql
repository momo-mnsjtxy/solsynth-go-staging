-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit migration after authority, account, social and canonical media.
CREATE TABLE IF NOT EXISTS owned_livestreams(
 id uuid PRIMARY KEY,
 publisher_id uuid NOT NULL REFERENCES owned_publishers(id),
 title varchar(256),description varchar(4096),slug varchar(128),
 type integer NOT NULL CHECK(type IN(0,1)),visibility integer NOT NULL CHECK(visibility IN(0,1,2)),status integer NOT NULL DEFAULT 0 CHECK(status BETWEEN 0 AND 3),
 room_name varchar(256) NOT NULL,
 thumbnail_id uuid REFERENCES owned_media_files(id),metadata jsonb CHECK(octet_length(metadata::text)<=65536),
 generation bigint NOT NULL DEFAULT 0 CHECK(generation>=0),operation_state text NOT NULL DEFAULT 'ready' CHECK(operation_state IN('ready','starting','active','ending','deleted')),
 started_at timestamptz,ended_at timestamptz,total_duration_seconds bigint NOT NULL DEFAULT 0 CHECK(total_duration_seconds>=0),
 viewer_count integer NOT NULL DEFAULT 0 CHECK(viewer_count>=0),peak_viewer_count integer NOT NULL DEFAULT 0 CHECK(peak_viewer_count>=0),
 created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,deleted_at timestamptz
);
CREATE INDEX IF NOT EXISTS owned_livestream_public ON owned_livestreams(status,visibility,created_at DESC,id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS owned_livestream_publisher ON owned_livestreams(publisher_id,created_at DESC,id) WHERE deleted_at IS NULL;
CREATE TABLE IF NOT EXISTS owned_livestream_create_keys(
 actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),key_hash bytea NOT NULL,request_hash bytea NOT NULL,stream_id uuid NOT NULL REFERENCES owned_livestreams(id),
 PRIMARY KEY(actor_id,key_hash)
);
CREATE TABLE IF NOT EXISTS owned_livestream_runs(
 stream_id uuid NOT NULL REFERENCES owned_livestreams(id),generation bigint NOT NULL CHECK(generation>0),
 room_name varchar(256) NOT NULL UNIQUE,provider text NOT NULL,endpoint text NOT NULL,
 actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),session_id uuid NOT NULL REFERENCES owned_authority_sessions(id),account_version bigint NOT NULL,session_epoch bigint NOT NULL,
 owner_id uuid NOT NULL REFERENCES owned_authority_accounts(id),owner_version bigint NOT NULL CHECK(owner_version>0),
 permission text NOT NULL DEFAULT 'livestreams.start' CHECK(permission='livestreams.start'),
 mode integer NOT NULL CHECK(mode IN(0,1,2)),identity varchar(128) NOT NULL,ingress_name varchar(256) NOT NULL,participant_name varchar(128) NOT NULL,transcoding boolean NOT NULL,
 request_hash bytea NOT NULL,phase text NOT NULL CHECK(phase IN('attempted','active','ending','done')),
 ingress_id varchar(256),created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),next_attempt_at timestamptz NOT NULL,
 PRIMARY KEY(stream_id,generation)
);
CREATE INDEX IF NOT EXISTS owned_livestream_runs_due ON owned_livestream_runs(provider,endpoint,next_attempt_at,stream_id,generation) WHERE phase<>'done';
CREATE TABLE IF NOT EXISTS owned_livestream_ingresses(
 stream_id uuid NOT NULL,generation bigint NOT NULL,ingress_id varchar(256) NOT NULL,
 deleted_at timestamptz,PRIMARY KEY(stream_id,generation,ingress_id),FOREIGN KEY(stream_id,generation)REFERENCES owned_livestream_runs(stream_id,generation)
);
CREATE TABLE IF NOT EXISTS owned_livestream_admissions(
 stream_id uuid NOT NULL,generation bigint NOT NULL,session_id uuid NOT NULL REFERENCES owned_authority_sessions(id),actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),tool boolean NOT NULL,publishing boolean NOT NULL,permission text NOT NULL CHECK(permission IN('livestreams.view','livestreams.start')),
 identity uuid NOT NULL UNIQUE,account_version bigint NOT NULL,session_epoch bigint NOT NULL,
 state text NOT NULL CHECK(state IN('admitted','removing','removed')),issued_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,
 PRIMARY KEY(stream_id,generation,session_id,tool,publishing),FOREIGN KEY(stream_id,generation) REFERENCES owned_livestream_runs(stream_id,generation)
);
CREATE TABLE IF NOT EXISTS owned_livestream_audit(
 id bigserial PRIMARY KEY,stream_id uuid NOT NULL REFERENCES owned_livestreams(id),actor_id uuid REFERENCES owned_authority_accounts(id),action text NOT NULL,occurred_at timestamptz NOT NULL
);
