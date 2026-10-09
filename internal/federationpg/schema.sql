-- SPDX-License-Identifier: AGPL-3.0-only
-- Independent federation adapter schema. Explicitly apply after authoritypg,
-- accountpg, postpg. Never auto-applied. Local identities/posts remain canonical.
CREATE TABLE IF NOT EXISTS owned_federation_publishers(
 publisher_id uuid PRIMARY KEY REFERENCES owned_publishers(id),enabled boolean NOT NULL DEFAULT false,
 manual boolean NOT NULL DEFAULT true,updated_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS owned_federation_peers(
 actor_uri text PRIMARY KEY CHECK(length(actor_uri)<=2048),inbox_uri text NOT NULL CHECK(length(inbox_uri)<=2048),updated_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS owned_federation_relationships(
 publisher_id uuid NOT NULL REFERENCES owned_publishers(id),actor_uri text NOT NULL REFERENCES owned_federation_peers(actor_uri),
 direction text NOT NULL CHECK(direction IN('in','out')),status text NOT NULL CHECK(status IN('pending','accepted','rejected','undone')),
 follow_id text NOT NULL CHECK(length(follow_id)<=2048),updated_at timestamptz NOT NULL,
 PRIMARY KEY(publisher_id,actor_uri,direction)
);
CREATE TABLE IF NOT EXISTS owned_federation_blocks(
 publisher_id uuid NOT NULL REFERENCES owned_publishers(id),actor_uri text NOT NULL REFERENCES owned_federation_peers(actor_uri),created_at timestamptz NOT NULL,
 PRIMARY KEY(publisher_id,actor_uri)
);
CREATE TABLE IF NOT EXISTS owned_federation_inbox(
 publisher_id uuid NOT NULL REFERENCES owned_publishers(id),activity_id text NOT NULL CHECK(length(activity_id)<=2048),
 actor_uri text NOT NULL REFERENCES owned_federation_peers(actor_uri),digest bytea NOT NULL CHECK(octet_length(digest)=32),received_at timestamptz NOT NULL,
 PRIMARY KEY(publisher_id,activity_id)
);
-- Remote public-object cache, isolated per local recipient. It is never a local
-- publisher/account/post and never enters the canonical local feed implicitly.
CREATE TABLE IF NOT EXISTS owned_federation_objects(
 publisher_id uuid NOT NULL REFERENCES owned_publishers(id),object_id text NOT NULL CHECK(length(object_id)<=2048),
 actor_uri text NOT NULL REFERENCES owned_federation_peers(actor_uri),object jsonb NOT NULL,
 deleted boolean NOT NULL DEFAULT false,updated_at timestamptz NOT NULL,
 PRIMARY KEY(publisher_id,object_id)
);
CREATE TABLE IF NOT EXISTS owned_federation_post_state(
 post_id uuid PRIMARY KEY REFERENCES owned_text_posts(id),source_updated_at timestamptz NOT NULL,source_audit_id bigint NOT NULL DEFAULT 0,
 public boolean NOT NULL,revision bigint NOT NULL DEFAULT 1 CHECK(revision>0)
);
CREATE TABLE IF NOT EXISTS owned_federation_deliveries(
 id bigserial PRIMARY KEY,publisher_id uuid NOT NULL REFERENCES owned_publishers(id),
 actor_uri text NOT NULL REFERENCES owned_federation_peers(actor_uri),inbox_uri text NOT NULL CHECK(length(inbox_uri)<=2048),
 activity_id text NOT NULL CHECK(length(activity_id)<=2048),activity_type text NOT NULL,
 payload bytea NOT NULL CHECK(octet_length(payload)<=1048576),post_id uuid REFERENCES owned_text_posts(id),post_updated_at timestamptz,post_audit_id bigint,
 status text NOT NULL DEFAULT 'pending' CHECK(status IN('pending','inflight','sent','dead','cancelled')),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),next_attempt timestamptz NOT NULL,
 lease_token uuid,lease_until timestamptz,created_at timestamptz NOT NULL,finished_at timestamptz,
 UNIQUE(activity_id,actor_uri),CHECK((post_id IS NULL)=(post_updated_at IS NULL)),CHECK((post_id IS NULL)=(post_audit_id IS NULL))
);
CREATE INDEX IF NOT EXISTS owned_federation_deliveries_due ON owned_federation_deliveries(next_attempt,id) WHERE status IN('pending','inflight');
CREATE TABLE IF NOT EXISTS owned_federation_audit(
 id bigserial PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),publisher_id uuid NOT NULL REFERENCES owned_publishers(id),
 action text NOT NULL,occurred_at timestamptz NOT NULL
);

-- Canonical audit lookup for bounded public-post reconciliation.
CREATE INDEX IF NOT EXISTS owned_federation_post_audit_lookup ON owned_text_post_audit(post_id,id);
