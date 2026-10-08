-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit additive migration after canonical posts and federation identity.
CREATE TABLE IF NOT EXISTS owned_federation_quote_authorizations(
 id uuid PRIMARY KEY,
 publisher_id uuid NOT NULL REFERENCES owned_publishers(id),
 actor_uri text NOT NULL REFERENCES owned_federation_peers(actor_uri),
 request_id text NOT NULL CHECK(length(request_id) BETWEEN 1 AND 2048),
 object_uri text NOT NULL CHECK(length(object_uri) BETWEEN 1 AND 2048),
 target_post_id uuid NOT NULL REFERENCES owned_text_posts(id),
 target_updated_at timestamptz NOT NULL,target_audit_id bigint NOT NULL,
 key_id text NOT NULL CHECK(length(key_id) BETWEEN 1 AND 2048),
 key_sha256 bytea NOT NULL CHECK(octet_length(key_sha256)=32),
 created_at timestamptz NOT NULL,consumed_at timestamptz,revoked_at timestamptz,
 UNIQUE(publisher_id,request_id),UNIQUE(publisher_id,object_uri)
);
ALTER TABLE owned_federation_deliveries ADD COLUMN IF NOT EXISTS quote_id uuid REFERENCES owned_federation_quote_authorizations(id);
