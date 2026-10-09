-- SPDX-License-Identifier: AGPL-3.0-only
-- Additive owned schema. Install explicitly after internal/mediapg/schema.sql.
CREATE TABLE owned_media_upload_tasks (
 id uuid PRIMARY KEY REFERENCES owned_media_files(id),
 request_sha256 char(64) NOT NULL CHECK(request_sha256 ~ '^[0-9a-f]{64}$'),
 expected_md5 char(32) NOT NULL CHECK(expected_md5 ~ '^[0-9a-f]{32}$'),
 chunk_size bigint NOT NULL CHECK(chunk_size BETWEEN 1 AND 20971520),
 chunks_count integer NOT NULL CHECK(chunks_count BETWEEN 1 AND 10000),
 status text NOT NULL CHECK(status IN ('InProgress','Processing','Completed','Failed')),
 failure_code text NOT NULL DEFAULT ''
);
CREATE TABLE owned_media_upload_chunks (
 task_id uuid NOT NULL REFERENCES owned_media_upload_tasks(id),
 chunk_index integer NOT NULL CHECK(chunk_index BETWEEN 0 AND 9999),
 size bigint NOT NULL CHECK(size BETWEEN 1 AND 20971520),
 sha256 char(64) NOT NULL CHECK(sha256 ~ '^[0-9a-f]{64}$'),
 PRIMARY KEY(task_id,chunk_index)
);
