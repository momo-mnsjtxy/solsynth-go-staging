-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit migration after federation identity and publisher name reservations.
CREATE TABLE IF NOT EXISTS owned_federation_server_identity(
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 actor_uri text NOT NULL CHECK(length(actor_uri)<=2048),
 key_id text NOT NULL CHECK(key_id=actor_uri||'#main-key'),
 key_fingerprint text NOT NULL CHECK(key_fingerprint~'^[0-9a-f]{64}$'),
 username text NOT NULL CHECK(length(username) BETWEEN 1 AND 128),
 created_at timestamptz NOT NULL
);
CREATE OR REPLACE FUNCTION owned_federation_server_immutable()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
 RAISE EXCEPTION 'Server actor identity is permanent' USING ERRCODE='23514';
END$$;
DROP TRIGGER IF EXISTS owned_federation_server_immutable ON owned_federation_server_identity;
CREATE TRIGGER owned_federation_server_immutable BEFORE UPDATE OR DELETE ON owned_federation_server_identity FOR EACH ROW EXECUTE FUNCTION owned_federation_server_immutable();
-- Initializing the server and claiming publisher handles share this lock, so a
-- concurrent insert cannot claim a server handle (or a retired publisher alias).
CREATE OR REPLACE FUNCTION owned_federation_server_name_guard()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
 IF TG_TABLE_NAME='owned_federation_server_identity' THEN
  PERFORM pg_advisory_xact_lock(hashtextextended('owned_federation_name:'||lower(NEW.username),0));
  IF EXISTS(SELECT 1 FROM owned_publisher_name_reservations WHERE folded_name=lower(NEW.username))THEN
   RAISE EXCEPTION 'Server handle collides with a publisher name' USING ERRCODE='23505';
  END IF;
 ELSE
  PERFORM pg_advisory_xact_lock(hashtextextended('owned_federation_name:'||NEW.folded_name,0));
  IF EXISTS(SELECT 1 FROM owned_federation_server_identity WHERE lower(username)=NEW.folded_name)THEN
   RAISE EXCEPTION 'Publisher name is reserved by the server' USING ERRCODE='23505';
  END IF;
 END IF;
 RETURN NEW;
END$$;
DROP TRIGGER IF EXISTS owned_federation_server_name_guard ON owned_federation_server_identity;
CREATE TRIGGER owned_federation_server_name_guard BEFORE INSERT ON owned_federation_server_identity FOR EACH ROW EXECUTE FUNCTION owned_federation_server_name_guard();
DROP TRIGGER IF EXISTS owned_federation_server_name_guard ON owned_publisher_name_reservations;
CREATE TRIGGER owned_federation_server_name_guard BEFORE INSERT ON owned_publisher_name_reservations FOR EACH ROW EXECUTE FUNCTION owned_federation_server_name_guard();
CREATE TABLE IF NOT EXISTS owned_federation_discoveries(
 id uuid PRIMARY KEY,
 publisher_id uuid NOT NULL REFERENCES owned_publishers(id),
 owner_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 requester_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 requester_version bigint NOT NULL CHECK(requester_version>0),
 requester_session uuid NOT NULL REFERENCES owned_authority_sessions(id),
 requester_epoch bigint NOT NULL CHECK(requester_epoch>0),
 max_attempts integer NOT NULL CHECK(max_attempts BETWEEN 1 AND 12),
 expires_at timestamptz NOT NULL,
 policy_stamp jsonb NOT NULL CHECK(jsonb_typeof(policy_stamp)='object'),
 resource text NOT NULL CHECK(length(resource) BETWEEN 1 AND 512),
 actor_uri text NOT NULL CHECK(length(actor_uri) BETWEEN 1 AND 2048),
 binding text NOT NULL CHECK(binding~'^[0-9a-f]{64}$'),
 status text NOT NULL CHECK(status IN('pending','inflight','succeeded','dead','cancelled')),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 12),
 next_attempt timestamptz NOT NULL,
 lease_token uuid,lease_until timestamptz,
 result jsonb CHECK(result IS NULL OR(jsonb_typeof(result)='object' AND octet_length(result::text)<=32768)),
 created_at timestamptz NOT NULL,finished_at timestamptz,
 UNIQUE(publisher_id,resource),
 CHECK(attempts<=max_attempts),
 CHECK((status='inflight')=(lease_token IS NOT NULL AND lease_until IS NOT NULL)),
 CHECK((status='succeeded')=(result IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS owned_federation_discovery_due ON owned_federation_discoveries(next_attempt,id)WHERE status IN('pending','inflight');
CREATE OR REPLACE FUNCTION owned_federation_discovery_identity_immutable()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
 IF NEW.id IS DISTINCT FROM OLD.id OR NEW.publisher_id IS DISTINCT FROM OLD.publisher_id OR NEW.owner_id IS DISTINCT FROM OLD.owner_id OR NEW.requester_id IS DISTINCT FROM OLD.requester_id OR NEW.requester_version IS DISTINCT FROM OLD.requester_version OR NEW.requester_session IS DISTINCT FROM OLD.requester_session OR NEW.requester_epoch IS DISTINCT FROM OLD.requester_epoch OR NEW.resource IS DISTINCT FROM OLD.resource OR NEW.actor_uri IS DISTINCT FROM OLD.actor_uri OR NEW.binding IS DISTINCT FROM OLD.binding OR NEW.created_at IS DISTINCT FROM OLD.created_at OR NEW.max_attempts IS DISTINCT FROM OLD.max_attempts OR NEW.expires_at IS DISTINCT FROM OLD.expires_at OR NEW.policy_stamp IS DISTINCT FROM OLD.policy_stamp THEN
  RAISE EXCEPTION 'Discovery identity binding is immutable' USING ERRCODE='23514';
 END IF;
 IF OLD.status IN('succeeded','dead','cancelled') AND NEW IS DISTINCT FROM OLD THEN
  RAISE EXCEPTION 'Discovery outcome is terminal' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END$$;
DROP TRIGGER IF EXISTS owned_federation_discovery_identity_immutable ON owned_federation_discoveries;
CREATE TRIGGER owned_federation_discovery_identity_immutable BEFORE UPDATE ON owned_federation_discoveries FOR EACH ROW EXECUTE FUNCTION owned_federation_discovery_identity_immutable();

-- This optional module cannot depend on optional blog/factor policy tables.
-- Retain only monotonic history, not a second permission evaluator. Revocation
-- followed by restoration, including an empty policy set, cannot revive intent.
CREATE TABLE IF NOT EXISTS owned_federation_discovery_epochs(
 kind text NOT NULL CHECK(kind IN('account','publisher')),entity_id uuid NOT NULL,
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),PRIMARY KEY(kind,entity_id)
);
CREATE OR REPLACE FUNCTION owned_federation_discovery_bump()RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE old_id uuid;new_id uuid;
BEGIN
 IF TG_OP<>'INSERT' THEN old_id=(to_jsonb(OLD)->>TG_ARGV[1])::uuid;END IF;
 IF TG_OP<>'DELETE' THEN new_id=(to_jsonb(NEW)->>TG_ARGV[1])::uuid;END IF;
 INSERT INTO owned_federation_discovery_epochs(kind,entity_id)
 SELECT TG_ARGV[0],id FROM(SELECT DISTINCT unnest(ARRAY[old_id,new_id]) id)ids WHERE id IS NOT NULL ORDER BY id
 ON CONFLICT(kind,entity_id)DO UPDATE SET revision=owned_federation_discovery_epochs.revision+1;
 IF TG_OP='DELETE' THEN RETURN OLD;END IF;RETURN NEW;
END$$;
CREATE OR REPLACE FUNCTION owned_federation_discovery_bump_group()RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE old_id uuid;new_id uuid;
BEGIN
 IF TG_OP<>'INSERT' THEN old_id=OLD.group_id;END IF;
 IF TG_OP<>'DELETE' THEN new_id=NEW.group_id;END IF;
 INSERT INTO owned_federation_discovery_epochs(kind,entity_id)
 SELECT 'account',account_id FROM owned_authority_group_members WHERE group_id=ANY(ARRAY[old_id,new_id]) GROUP BY account_id ORDER BY account_id
 ON CONFLICT(kind,entity_id)DO UPDATE SET revision=owned_federation_discovery_epochs.revision+1;
 IF TG_OP='DELETE' THEN RETURN OLD;END IF;RETURN NEW;
END$$;
DROP TRIGGER IF EXISTS owned_discovery_authority_grants ON owned_authority_grants;
CREATE TRIGGER owned_discovery_authority_grants AFTER INSERT OR UPDATE OR DELETE ON owned_authority_grants FOR EACH ROW EXECUTE FUNCTION owned_federation_discovery_bump('account','account_id');
DROP TRIGGER IF EXISTS owned_discovery_authority_punishments ON owned_authority_punishments;
CREATE TRIGGER owned_discovery_authority_punishments AFTER INSERT OR UPDATE OR DELETE ON owned_authority_punishments FOR EACH ROW EXECUTE FUNCTION owned_federation_discovery_bump('account','account_id');
DROP TRIGGER IF EXISTS owned_discovery_authority_group_members ON owned_authority_group_members;
CREATE TRIGGER owned_discovery_authority_group_members AFTER INSERT OR UPDATE OR DELETE ON owned_authority_group_members FOR EACH ROW EXECUTE FUNCTION owned_federation_discovery_bump('account','account_id');
DROP TRIGGER IF EXISTS owned_discovery_publisher_members ON owned_publisher_members;
CREATE TRIGGER owned_discovery_publisher_members AFTER INSERT OR UPDATE OR DELETE ON owned_publisher_members FOR EACH ROW EXECUTE FUNCTION owned_federation_discovery_bump('publisher','publisher_id');
DROP TRIGGER IF EXISTS owned_discovery_federation_blocks ON owned_federation_blocks;
CREATE TRIGGER owned_discovery_federation_blocks AFTER INSERT OR UPDATE OR DELETE ON owned_federation_blocks FOR EACH ROW EXECUTE FUNCTION owned_federation_discovery_bump('publisher','publisher_id');
DROP TRIGGER IF EXISTS owned_discovery_group_grants ON owned_authority_group_grants;
CREATE TRIGGER owned_discovery_group_grants AFTER INSERT OR UPDATE OR DELETE ON owned_authority_group_grants FOR EACH ROW EXECUTE FUNCTION owned_federation_discovery_bump_group();
DROP TRIGGER IF EXISTS owned_discovery_account ON owned_authority_accounts;
CREATE TRIGGER owned_discovery_account AFTER UPDATE OF version,disabled,is_superuser ON owned_authority_accounts FOR EACH ROW WHEN(NEW.version IS DISTINCT FROM OLD.version OR NEW.disabled IS DISTINCT FROM OLD.disabled OR NEW.is_superuser IS DISTINCT FROM OLD.is_superuser) EXECUTE FUNCTION owned_federation_discovery_bump('account','id');
DROP TRIGGER IF EXISTS owned_discovery_publisher ON owned_publishers;
CREATE TRIGGER owned_discovery_publisher AFTER UPDATE OF account_id,type,realm_id,deleted_at,shadowbanned_at ON owned_publishers FOR EACH ROW WHEN(NEW.account_id IS DISTINCT FROM OLD.account_id OR NEW.type IS DISTINCT FROM OLD.type OR NEW.realm_id IS DISTINCT FROM OLD.realm_id OR NEW.deleted_at IS DISTINCT FROM OLD.deleted_at OR NEW.shadowbanned_at IS DISTINCT FROM OLD.shadowbanned_at) EXECUTE FUNCTION owned_federation_discovery_bump('publisher','id');
DROP TRIGGER IF EXISTS owned_discovery_federation ON owned_federation_publishers;
CREATE TRIGGER owned_discovery_federation AFTER UPDATE OF enabled ON owned_federation_publishers FOR EACH ROW WHEN(NEW.enabled IS DISTINCT FROM OLD.enabled) EXECUTE FUNCTION owned_federation_discovery_bump('publisher','publisher_id');
DROP TRIGGER IF EXISTS owned_discovery_session ON owned_authority_sessions;
CREATE TRIGGER owned_discovery_session AFTER UPDATE OF account_id,parent_id,authorization_epoch,revoked_at,expires_at,scopes,type,client_id ON owned_authority_sessions FOR EACH ROW WHEN(NEW.account_id IS DISTINCT FROM OLD.account_id OR NEW.parent_id IS DISTINCT FROM OLD.parent_id OR NEW.authorization_epoch IS DISTINCT FROM OLD.authorization_epoch OR NEW.revoked_at IS DISTINCT FROM OLD.revoked_at OR NEW.expires_at<OLD.expires_at OR NEW.scopes IS DISTINCT FROM OLD.scopes OR NEW.type IS DISTINCT FROM OLD.type OR NEW.client_id IS DISTINCT FROM OLD.client_id) EXECUTE FUNCTION owned_federation_discovery_bump('account','account_id');
DROP TRIGGER IF EXISTS owned_discovery_session_delete ON owned_authority_sessions;
CREATE TRIGGER owned_discovery_session_delete AFTER DELETE ON owned_authority_sessions FOR EACH ROW EXECUTE FUNCTION owned_federation_discovery_bump('account','account_id');
