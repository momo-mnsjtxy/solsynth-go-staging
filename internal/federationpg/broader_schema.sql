-- SPDX-License-Identifier: AGPL-3.0-only
-- Additive federation business schema. Apply after federation, social and post.
-- Private key bytes and fetched media bytes are never stored here.
CREATE TABLE IF NOT EXISTS owned_federation_realms(
 realm_id uuid PRIMARY KEY CONSTRAINT owned_federation_realms_realm_fk REFERENCES owned_realms(id),enabled boolean NOT NULL DEFAULT false,
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
 status text NOT NULL CHECK(status IN('accepted','undone')),follow_id text NOT NULL CHECK(length(follow_id) BETWEEN 1 AND 2048),updated_at timestamptz NOT NULL,
 PRIMARY KEY(realm_id,actor_uri)
);
CREATE TABLE IF NOT EXISTS owned_federation_realm_inbox(
 realm_id uuid NOT NULL REFERENCES owned_federation_realms(realm_id),activity_id text NOT NULL CHECK(length(activity_id) BETWEEN 1 AND 2048),
 actor_uri text NOT NULL REFERENCES owned_federation_peers(actor_uri),digest bytea NOT NULL CHECK(octet_length(digest)=32),received_at timestamptz NOT NULL,
 PRIMARY KEY(realm_id,activity_id)
);
CREATE TABLE IF NOT EXISTS owned_federation_realm_outbox(
 realm_id uuid NOT NULL REFERENCES owned_federation_realms(realm_id),activity_id text NOT NULL CHECK(length(activity_id) BETWEEN 1 AND 2048),
 activity jsonb NOT NULL CHECK(jsonb_typeof(activity)='object' AND octet_length(activity::text)<=1048576),created_at timestamptz NOT NULL,
 PRIMARY KEY(realm_id,activity_id)
);
ALTER TABLE owned_federation_realm_outbox ADD COLUMN IF NOT EXISTS post_id uuid REFERENCES owned_text_posts(id);
ALTER TABLE owned_federation_realm_outbox ADD COLUMN IF NOT EXISTS withdrawn_at timestamptz;
CREATE TABLE IF NOT EXISTS owned_federation_realm_deliveries(
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,realm_id uuid NOT NULL REFERENCES owned_federation_realms(realm_id),
 actor_uri text NOT NULL REFERENCES owned_federation_peers(actor_uri) CHECK(length(actor_uri) BETWEEN 1 AND 2048),inbox_uri text NOT NULL CHECK(length(inbox_uri) BETWEEN 1 AND 2048),
 activity_id text NOT NULL CHECK(length(activity_id) BETWEEN 1 AND 2048),activity_type text NOT NULL CONSTRAINT owned_federation_realm_delivery_type CHECK(activity_type IN('Accept','Announce','Create','Update','Delete','Undo')),payload bytea NOT NULL CONSTRAINT owned_federation_realm_delivery_payload CHECK(octet_length(payload) BETWEEN 2 AND 1048576 AND jsonb_typeof(convert_from(payload,'UTF8')::jsonb)='object' AND convert_from(payload,'UTF8')::jsonb->>'id'=activity_id AND convert_from(payload,'UTF8')::jsonb->>'type'=activity_type AND length(convert_from(payload,'UTF8')::jsonb->>'actor') BETWEEN 1 AND 2048),
 post_id uuid REFERENCES owned_text_posts(id),post_updated_at timestamptz,
 status text NOT NULL DEFAULT 'pending' CHECK(status IN('pending','inflight','sent','dead','cancelled')),attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 12),
 remote_binding text NOT NULL CHECK(remote_binding~'^[0-9a-f]{64}$'),local_fingerprint text NOT NULL CHECK(local_fingerprint~'^[0-9a-f]{64}$'),
 max_attempts integer NOT NULL CHECK(max_attempts BETWEEN 1 AND 12),expires_at timestamptz NOT NULL,
 lease_token uuid,lease_until timestamptz,dispatch_started_at timestamptz,
 next_attempt timestamptz NOT NULL,created_at timestamptz NOT NULL,finished_at timestamptz,UNIQUE(activity_id,actor_uri),
 CONSTRAINT owned_federation_realm_delivery_bounds CHECK(attempts<=max_attempts AND expires_at>created_at),CONSTRAINT owned_federation_realm_delivery_lease CHECK((status='inflight')=(lease_token IS NOT NULL AND lease_until IS NOT NULL)),
 CONSTRAINT owned_federation_realm_delivery_terminal CHECK((status IN('sent','dead','cancelled'))=(finished_at IS NOT NULL)),
 CHECK((post_id IS NULL)=(post_updated_at IS NULL))
);
DO $$BEGIN
 IF EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='owned_federation_realm_deliveries'::regclass AND conname='owned_federation_realm_deliveries_activity_type_check')THEN
  ALTER TABLE owned_federation_realm_deliveries DROP CONSTRAINT owned_federation_realm_deliveries_activity_type_check;
 END IF;
 IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='owned_federation_realm_deliveries'::regclass AND conname='owned_federation_realm_delivery_type')THEN
  ALTER TABLE owned_federation_realm_deliveries ADD CONSTRAINT owned_federation_realm_delivery_type CHECK(activity_type IN('Accept','Announce','Create','Update','Delete','Undo'));
 END IF;
END$$;
CREATE INDEX IF NOT EXISTS owned_federation_realm_delivery_due ON owned_federation_realm_deliveries(next_attempt,id) WHERE status IN('pending','inflight');
CREATE OR REPLACE FUNCTION owned_federation_realm_delivery_immutable()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
 IF NEW.id IS DISTINCT FROM OLD.id OR NEW.realm_id IS DISTINCT FROM OLD.realm_id OR NEW.actor_uri IS DISTINCT FROM OLD.actor_uri OR NEW.inbox_uri IS DISTINCT FROM OLD.inbox_uri OR NEW.activity_id IS DISTINCT FROM OLD.activity_id OR NEW.activity_type IS DISTINCT FROM OLD.activity_type OR NEW.payload IS DISTINCT FROM OLD.payload OR NEW.post_id IS DISTINCT FROM OLD.post_id OR NEW.post_updated_at IS DISTINCT FROM OLD.post_updated_at OR NEW.remote_binding IS DISTINCT FROM OLD.remote_binding OR NEW.local_fingerprint IS DISTINCT FROM OLD.local_fingerprint OR NEW.max_attempts IS DISTINCT FROM OLD.max_attempts OR NEW.expires_at IS DISTINCT FROM OLD.expires_at OR NEW.created_at IS DISTINCT FROM OLD.created_at OR NEW.attempts<OLD.attempts OR OLD.status IN('sent','dead','cancelled') AND NEW IS DISTINCT FROM OLD THEN
  RAISE EXCEPTION 'Realm delivery identity/outcome is immutable' USING ERRCODE='23514';
 END IF;RETURN NEW;
END$$;
DROP TRIGGER IF EXISTS owned_federation_realm_delivery_immutable ON owned_federation_realm_deliveries;
CREATE TRIGGER owned_federation_realm_delivery_immutable BEFORE UPDATE ON owned_federation_realm_deliveries FOR EACH ROW EXECUTE FUNCTION owned_federation_realm_delivery_immutable();
CREATE TABLE IF NOT EXISTS owned_federation_realm_post_state(
 post_id uuid PRIMARY KEY REFERENCES owned_text_posts(id),realm_id uuid NOT NULL REFERENCES owned_federation_realms(realm_id),
 source_updated_at timestamptz NOT NULL,public boolean NOT NULL,revision bigint NOT NULL CHECK(revision>0),last_activity_id text,last_activity_type text
);
ALTER TABLE owned_federation_realm_post_state ADD COLUMN IF NOT EXISTS last_activity_id text;
ALTER TABLE owned_federation_realm_post_state ADD COLUMN IF NOT EXISTS last_activity_type text;
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
CREATE TABLE IF NOT EXISTS owned_federation_key_commands(
 id uuid PRIMARY KEY,requester_id uuid NOT NULL REFERENCES owned_authority_accounts(id),target_id uuid NOT NULL REFERENCES owned_publishers(id),
 target_actor_uri text NOT NULL CHECK(length(target_actor_uri) BETWEEN 1 AND 2048),target_key_id text NOT NULL CHECK(length(target_key_id) BETWEEN 1 AND 2048),
 action text NOT NULL CHECK(action IN('ensure','rotate')),expected_fingerprint text NOT NULL CHECK(expected_fingerprint='' OR expected_fingerprint~'^[0-9a-f]{64}$'),
 result_fingerprint text CHECK(result_fingerprint IS NULL OR result_fingerprint~'^[0-9a-f]{64}$'),
 requester_version bigint NOT NULL CHECK(requester_version>0),requester_session uuid NOT NULL REFERENCES owned_authority_sessions(id),requester_epoch bigint NOT NULL CHECK(requester_epoch>0),
 status text NOT NULL CHECK(status IN('pending','inflight','succeeded','cancelled','dead')),attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 12),
 lease_token uuid,lease_until timestamptz,next_attempt timestamptz NOT NULL,created_at timestamptz NOT NULL,finished_at timestamptz,
 CONSTRAINT owned_federation_key_command_lease CHECK((status='inflight')=(lease_token IS NOT NULL AND lease_until IS NOT NULL)),CONSTRAINT owned_federation_key_command_terminal CHECK((status IN('succeeded','cancelled','dead'))=(finished_at IS NOT NULL))
);
DO $$BEGIN
 IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='owned_federation_key_commands'::regclass AND conname='owned_federation_key_command_lease')THEN
  ALTER TABLE owned_federation_key_commands ADD CONSTRAINT owned_federation_key_command_lease CHECK((status='inflight')=(lease_token IS NOT NULL AND lease_until IS NOT NULL));
 END IF;
 IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='owned_federation_key_commands'::regclass AND conname='owned_federation_key_command_terminal')THEN
  ALTER TABLE owned_federation_key_commands ADD CONSTRAINT owned_federation_key_command_terminal CHECK((status IN('succeeded','cancelled','dead'))=(finished_at IS NOT NULL));
 END IF;
END$$;
CREATE INDEX IF NOT EXISTS owned_federation_key_command_due ON owned_federation_key_commands(next_attempt,id)WHERE status IN('pending','inflight');
CREATE OR REPLACE FUNCTION owned_federation_key_command_immutable()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
 IF NEW.id IS DISTINCT FROM OLD.id OR NEW.requester_id IS DISTINCT FROM OLD.requester_id OR NEW.target_id IS DISTINCT FROM OLD.target_id OR NEW.target_actor_uri IS DISTINCT FROM OLD.target_actor_uri OR NEW.target_key_id IS DISTINCT FROM OLD.target_key_id OR NEW.action IS DISTINCT FROM OLD.action OR NEW.expected_fingerprint IS DISTINCT FROM OLD.expected_fingerprint OR NEW.requester_version IS DISTINCT FROM OLD.requester_version OR NEW.requester_session IS DISTINCT FROM OLD.requester_session OR NEW.requester_epoch IS DISTINCT FROM OLD.requester_epoch OR NEW.created_at IS DISTINCT FROM OLD.created_at OR NEW.attempts<OLD.attempts OR OLD.status IN('succeeded','cancelled','dead') AND NEW IS DISTINCT FROM OLD THEN
  RAISE EXCEPTION 'Key command identity/outcome is immutable' USING ERRCODE='23514';
 END IF;RETURN NEW;
END$$;
DROP TRIGGER IF EXISTS owned_federation_key_command_immutable ON owned_federation_key_commands;
CREATE TRIGGER owned_federation_key_command_immutable BEFORE UPDATE ON owned_federation_key_commands FOR EACH ROW EXECUTE FUNCTION owned_federation_key_command_immutable();
CREATE TABLE IF NOT EXISTS owned_federation_moves(
 old_actor_uri text PRIMARY KEY CONSTRAINT owned_federation_moves_old_peer_fk REFERENCES owned_federation_peers(actor_uri),new_actor_uri text NOT NULL CONSTRAINT owned_federation_moves_new_peer_fk REFERENCES owned_federation_peers(actor_uri),
 activity_id text NOT NULL UNIQUE CHECK(length(activity_id) BETWEEN 1 AND 2048),old_digest bytea NOT NULL CHECK(octet_length(old_digest)=32),
 old_incarnation text NOT NULL CHECK(length(old_incarnation) BETWEEN 1 AND 2048),old_fingerprint text NOT NULL CHECK(old_fingerprint~'^[0-9a-f]{64}$'),
 new_incarnation text NOT NULL CHECK(length(new_incarnation) BETWEEN 1 AND 2048),new_fingerprint text NOT NULL CHECK(new_fingerprint~'^[0-9a-f]{64}$'),
 discovery_id uuid NOT NULL,completed_at timestamptz NOT NULL,CHECK(old_actor_uri<>new_actor_uri)
);
CREATE TABLE IF NOT EXISTS owned_federation_actor_assertions(
 publisher_id uuid NOT NULL REFERENCES owned_publishers(id),actor_uri text NOT NULL CHECK(length(actor_uri) BETWEEN 1 AND 2048),
 key_id text NOT NULL CHECK(length(key_id) BETWEEN 1 AND 2048),key_fingerprint text NOT NULL CHECK(key_fingerprint~'^[0-9a-f]{64}$'),
 remote_binding text NOT NULL CHECK(remote_binding~'^[0-9a-f]{64}$'),aliases jsonb NOT NULL CHECK(jsonb_typeof(aliases)='array' AND jsonb_array_length(aliases)<=16),
 discovery_id uuid NOT NULL,observed_at timestamptz NOT NULL,PRIMARY KEY(publisher_id,actor_uri)
);
CREATE TABLE IF NOT EXISTS owned_federation_move_applications(
 old_actor_uri text NOT NULL CONSTRAINT owned_federation_move_applications_move_fk REFERENCES owned_federation_moves(old_actor_uri),publisher_id uuid NOT NULL CONSTRAINT owned_federation_move_applications_publisher_fk REFERENCES owned_publishers(id),
 applied_at timestamptz NOT NULL,PRIMARY KEY(old_actor_uri,publisher_id)
);
