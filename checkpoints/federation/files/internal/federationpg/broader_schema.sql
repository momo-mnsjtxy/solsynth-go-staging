-- SPDX-License-Identifier: AGPL-3.0-only
-- Additive federation business schema. Apply after federation, social and post.
-- Private key bytes and fetched media bytes are never stored here.
CREATE TABLE IF NOT EXISTS owned_federation_realms(
 realm_id uuid PRIMARY KEY,enabled boolean NOT NULL DEFAULT false,
 actor_uri text NOT NULL UNIQUE CHECK(actor_uri LIKE 'https://%' AND length(actor_uri)<=2048),
 key_id text NOT NULL CHECK(key_id=actor_uri||'#main-key'),updated_at timestamptz NOT NULL
);
CREATE OR REPLACE FUNCTION owned_federation_realm_identity_immutable()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
 IF TG_OP='DELETE' OR NEW.realm_id IS DISTINCT FROM OLD.realm_id OR NEW.actor_uri IS DISTINCT FROM OLD.actor_uri OR NEW.key_id IS DISTINCT FROM OLD.key_id THEN
  RAISE EXCEPTION 'Federation realm identity is permanent' USING ERRCODE='23514';
 END IF;RETURN NEW;
END$$;
DROP TRIGGER IF EXISTS owned_federation_realm_identity_immutable ON owned_federation_realms;
CREATE TRIGGER owned_federation_realm_identity_immutable BEFORE UPDATE OR DELETE ON owned_federation_realms FOR EACH ROW EXECUTE FUNCTION owned_federation_realm_identity_immutable();
CREATE TABLE IF NOT EXISTS owned_federation_realm_relationships(
 realm_id uuid NOT NULL REFERENCES owned_federation_realms(realm_id),actor_uri text NOT NULL REFERENCES owned_federation_peers(actor_uri),
 status text NOT NULL CHECK(status IN('accepted','undone')),follow_id text NOT NULL CHECK(length(follow_id)<=2048),updated_at timestamptz NOT NULL,
 PRIMARY KEY(realm_id,actor_uri)
);
CREATE TABLE IF NOT EXISTS owned_federation_realm_inbox(
 realm_id uuid NOT NULL REFERENCES owned_federation_realms(realm_id),activity_id text NOT NULL CHECK(length(activity_id)<=2048),
 actor_uri text NOT NULL REFERENCES owned_federation_peers(actor_uri),digest bytea NOT NULL CHECK(octet_length(digest)=32),received_at timestamptz NOT NULL,
 PRIMARY KEY(realm_id,activity_id)
);
CREATE TABLE IF NOT EXISTS owned_federation_realm_outbox(
 realm_id uuid NOT NULL REFERENCES owned_federation_realms(realm_id),activity_id text NOT NULL CHECK(length(activity_id)<=2048),
 activity jsonb NOT NULL CHECK(jsonb_typeof(activity)='object' AND octet_length(activity::text)<=1048576),created_at timestamptz NOT NULL,
 PRIMARY KEY(realm_id,activity_id)
);
CREATE TABLE IF NOT EXISTS owned_federation_realm_deliveries(
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,realm_id uuid NOT NULL REFERENCES owned_federation_realms(realm_id),
 actor_uri text NOT NULL REFERENCES owned_federation_peers(actor_uri),inbox_uri text NOT NULL CHECK(length(inbox_uri)<=2048),
 activity_id text NOT NULL CHECK(length(activity_id) BETWEEN 1 AND 2048),activity_type text NOT NULL CHECK(activity_type IN('Accept','Announce')),payload bytea NOT NULL CHECK(octet_length(payload)<=1048576),
 status text NOT NULL DEFAULT 'pending' CHECK(status IN('pending','sent','dead','cancelled')),attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 12),
 next_attempt timestamptz NOT NULL,created_at timestamptz NOT NULL,finished_at timestamptz,UNIQUE(activity_id,actor_uri)
);
CREATE INDEX IF NOT EXISTS owned_federation_realm_delivery_due ON owned_federation_realm_deliveries(next_attempt,id) WHERE status='pending';
CREATE TABLE IF NOT EXISTS owned_federation_featured_posts(
 publisher_id uuid NOT NULL REFERENCES owned_publishers(id),post_id uuid NOT NULL REFERENCES owned_text_posts(id),
 position integer NOT NULL CHECK(position BETWEEN 0 AND 99),created_at timestamptz NOT NULL,
 PRIMARY KEY(publisher_id,post_id),UNIQUE(publisher_id,position)
);
CREATE TABLE IF NOT EXISTS owned_federation_moderation_rules(
 id uuid PRIMARY KEY,name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 256),description text NOT NULL DEFAULT '' CHECK(length(description)<=4096),
 kind text NOT NULL CHECK(kind IN('domain','actor','keyword')),action text NOT NULL CHECK(action IN('allow','silence','suspend','reject','media_reject')),
 pattern text NOT NULL CHECK(length(pattern) BETWEEN 1 AND 2048),is_regex boolean NOT NULL DEFAULT false,enabled boolean NOT NULL DEFAULT true,
 priority integer NOT NULL DEFAULT 0 CHECK(priority BETWEEN -100000 AND 100000),expires_at timestamptz,created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS owned_federation_moderation_active ON owned_federation_moderation_rules(priority,id) WHERE enabled;
CREATE TABLE IF NOT EXISTS owned_federation_admin_audit(
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 action text NOT NULL CHECK(length(action) BETWEEN 1 AND 128),target text NOT NULL CHECK(length(target) BETWEEN 1 AND 2048),occurred_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS owned_federation_moves(
 old_actor_uri text PRIMARY KEY REFERENCES owned_federation_peers(actor_uri),new_actor_uri text NOT NULL REFERENCES owned_federation_peers(actor_uri),
 activity_id text NOT NULL UNIQUE,completed_at timestamptz NOT NULL,CHECK(old_actor_uri<>new_actor_uri)
);
