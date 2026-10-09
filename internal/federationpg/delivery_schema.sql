-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit, atomic, additive migration after canonical authority, publisher,
-- post, federation identity and quote schemas. Never installed at startup.
-- Existing rows have no dispatch intent and fail closed; no authority backfill.
ALTER TABLE owned_federation_publishers ADD COLUMN IF NOT EXISTS delivery_intent jsonb;
ALTER TABLE owned_federation_deliveries ADD COLUMN IF NOT EXISTS origin_intent jsonb;
ALTER TABLE owned_federation_deliveries ADD COLUMN IF NOT EXISTS remote_binding text;
ALTER TABLE owned_federation_deliveries ADD COLUMN IF NOT EXISTS local_fingerprint text;
ALTER TABLE owned_federation_deliveries ADD COLUMN IF NOT EXISTS max_attempts integer;
ALTER TABLE owned_federation_deliveries ADD COLUMN IF NOT EXISTS expires_at timestamptz;
ALTER TABLE owned_federation_deliveries ADD COLUMN IF NOT EXISTS peer_epoch bigint;
-- Legacy quote jobs lack an observed security generation and fail closed.
ALTER TABLE owned_federation_deliveries ADD COLUMN IF NOT EXISTS peer_security_epoch bigint CHECK(peer_security_epoch IS NULL OR peer_security_epoch>=0);
ALTER TABLE owned_federation_deliveries ADD COLUMN IF NOT EXISTS dispatch_started_at timestamptz;
-- Nullable legacy rows remain readable, but cannot acquire a dispatch lease.
DO $$BEGIN
 IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='owned_federation_deliveries'::regclass AND conname='owned_federation_delivery_bounds')THEN
  ALTER TABLE owned_federation_deliveries ADD CONSTRAINT owned_federation_delivery_bounds CHECK(
   origin_intent IS NULL OR (
    jsonb_typeof(origin_intent)='object' AND octet_length(origin_intent::text)<=32768
    AND origin_intent ?& ARRAY['requester','owner','session','version','epoch','stamp','expires']
    AND jsonb_typeof(origin_intent->'stamp')='object'
    AND max_attempts IS NOT NULL AND max_attempts BETWEEN 1 AND 12 AND attempts<=max_attempts
    AND expires_at IS NOT NULL AND peer_epoch IS NOT NULL AND peer_epoch>=0
    AND remote_binding IS NOT NULL AND remote_binding~'^[0-9a-f]{64}$'
    AND local_fingerprint IS NOT NULL AND local_fingerprint~'^[0-9a-f]{64}$'
    AND ((lease_token IS NULL)=(lease_until IS NULL))
    AND ((status='inflight')=(lease_token IS NOT NULL AND lease_until IS NOT NULL))
   )
  );
 END IF;
END$$;
CREATE TABLE IF NOT EXISTS owned_federation_delivery_epochs(
 kind text NOT NULL CHECK(kind IN('account','publisher')),entity_id uuid NOT NULL,
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),PRIMARY KEY(kind,entity_id)
);
CREATE OR REPLACE FUNCTION owned_federation_delivery_bump()RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE old_id uuid;new_id uuid;
BEGIN
 IF TG_OP<>'INSERT' THEN old_id=(to_jsonb(OLD)->>TG_ARGV[1])::uuid;END IF;
 IF TG_OP<>'DELETE' THEN new_id=(to_jsonb(NEW)->>TG_ARGV[1])::uuid;END IF;
 INSERT INTO owned_federation_delivery_epochs(kind,entity_id)
 SELECT TG_ARGV[0],id FROM(SELECT DISTINCT unnest(ARRAY[old_id,new_id]) id)ids WHERE id IS NOT NULL ORDER BY id
 ON CONFLICT(kind,entity_id)DO UPDATE SET revision=owned_federation_delivery_epochs.revision+1;
 IF TG_OP='DELETE' THEN RETURN OLD;END IF;RETURN NEW;
END$$;
CREATE OR REPLACE FUNCTION owned_federation_delivery_bump_group()RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE old_id uuid;new_id uuid;
BEGIN
 IF TG_OP<>'INSERT' THEN old_id=OLD.group_id;END IF;
 IF TG_OP<>'DELETE' THEN new_id=NEW.group_id;END IF;
 INSERT INTO owned_federation_delivery_epochs(kind,entity_id)
 SELECT 'account',account_id FROM owned_authority_group_members WHERE group_id=ANY(ARRAY[old_id,new_id]) GROUP BY account_id ORDER BY account_id
 ON CONFLICT(kind,entity_id)DO UPDATE SET revision=owned_federation_delivery_epochs.revision+1;
 IF TG_OP='DELETE' THEN RETURN OLD;END IF;RETURN NEW;
END$$;
DROP TRIGGER IF EXISTS owned_delivery_authority_grants ON owned_authority_grants;
CREATE TRIGGER owned_delivery_authority_grants AFTER INSERT OR UPDATE OR DELETE ON owned_authority_grants FOR EACH ROW EXECUTE FUNCTION owned_federation_delivery_bump('account','account_id');
DROP TRIGGER IF EXISTS owned_delivery_authority_punishments ON owned_authority_punishments;
CREATE TRIGGER owned_delivery_authority_punishments AFTER INSERT OR UPDATE OR DELETE ON owned_authority_punishments FOR EACH ROW EXECUTE FUNCTION owned_federation_delivery_bump('account','account_id');
DROP TRIGGER IF EXISTS owned_delivery_authority_group_members ON owned_authority_group_members;
CREATE TRIGGER owned_delivery_authority_group_members AFTER INSERT OR UPDATE OR DELETE ON owned_authority_group_members FOR EACH ROW EXECUTE FUNCTION owned_federation_delivery_bump('account','account_id');
DROP TRIGGER IF EXISTS owned_delivery_publisher_members ON owned_publisher_members;
CREATE TRIGGER owned_delivery_publisher_members AFTER INSERT OR UPDATE OR DELETE ON owned_publisher_members FOR EACH ROW EXECUTE FUNCTION owned_federation_delivery_bump('publisher','publisher_id');
DROP TRIGGER IF EXISTS owned_delivery_group_grants ON owned_authority_group_grants;
CREATE TRIGGER owned_delivery_group_grants AFTER INSERT OR UPDATE OR DELETE ON owned_authority_group_grants FOR EACH ROW EXECUTE FUNCTION owned_federation_delivery_bump_group();
DROP TRIGGER IF EXISTS owned_delivery_account ON owned_authority_accounts;
CREATE TRIGGER owned_delivery_account AFTER UPDATE OF version,disabled,is_superuser ON owned_authority_accounts FOR EACH ROW WHEN(NEW.version IS DISTINCT FROM OLD.version OR NEW.disabled IS DISTINCT FROM OLD.disabled OR NEW.is_superuser IS DISTINCT FROM OLD.is_superuser) EXECUTE FUNCTION owned_federation_delivery_bump('account','id');
DROP TRIGGER IF EXISTS owned_delivery_publisher ON owned_publishers;
CREATE TRIGGER owned_delivery_publisher AFTER UPDATE OF account_id,type,realm_id,deleted_at,shadowbanned_at ON owned_publishers FOR EACH ROW WHEN(NEW.account_id IS DISTINCT FROM OLD.account_id OR NEW.type IS DISTINCT FROM OLD.type OR NEW.realm_id IS DISTINCT FROM OLD.realm_id OR NEW.deleted_at IS DISTINCT FROM OLD.deleted_at OR NEW.shadowbanned_at IS DISTINCT FROM OLD.shadowbanned_at) EXECUTE FUNCTION owned_federation_delivery_bump('publisher','id');
DROP TRIGGER IF EXISTS owned_delivery_federation ON owned_federation_publishers;
CREATE TRIGGER owned_delivery_federation AFTER UPDATE OF enabled ON owned_federation_publishers FOR EACH ROW WHEN(NEW.enabled IS DISTINCT FROM OLD.enabled) EXECUTE FUNCTION owned_federation_delivery_bump('publisher','publisher_id');
DROP TRIGGER IF EXISTS owned_delivery_session ON owned_authority_sessions;
CREATE TRIGGER owned_delivery_session AFTER UPDATE OF account_id,parent_id,authorization_epoch,revoked_at,expires_at,scopes,type,client_id ON owned_authority_sessions FOR EACH ROW WHEN(NEW.account_id IS DISTINCT FROM OLD.account_id OR NEW.parent_id IS DISTINCT FROM OLD.parent_id OR NEW.authorization_epoch IS DISTINCT FROM OLD.authorization_epoch OR NEW.revoked_at IS DISTINCT FROM OLD.revoked_at OR NEW.expires_at<OLD.expires_at OR NEW.scopes IS DISTINCT FROM OLD.scopes OR NEW.type IS DISTINCT FROM OLD.type OR NEW.client_id IS DISTINCT FROM OLD.client_id) EXECUTE FUNCTION owned_federation_delivery_bump('account','account_id');
DROP TRIGGER IF EXISTS owned_delivery_session_delete ON owned_authority_sessions;
CREATE TRIGGER owned_delivery_session_delete AFTER DELETE ON owned_authority_sessions FOR EACH ROW EXECUTE FUNCTION owned_federation_delivery_bump('account','account_id');

CREATE TABLE IF NOT EXISTS owned_federation_delivery_peer_epochs(
 publisher_id uuid NOT NULL REFERENCES owned_publishers(id),actor_uri text NOT NULL,
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),PRIMARY KEY(publisher_id,actor_uri)
);
ALTER TABLE owned_federation_delivery_peer_epochs ADD COLUMN IF NOT EXISTS security_revision bigint NOT NULL DEFAULT 0 CHECK(security_revision>=0);
CREATE OR REPLACE FUNCTION owned_federation_delivery_peer_bump()RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE security_delta bigint;
BEGIN
 security_delta=CASE WHEN TG_TABLE_NAME='owned_federation_blocks' THEN 1 ELSE 0 END;
 IF TG_OP<>'INSERT' THEN
  INSERT INTO owned_federation_delivery_peer_epochs(publisher_id,actor_uri,security_revision)VALUES(OLD.publisher_id,OLD.actor_uri,security_delta)
  ON CONFLICT(publisher_id,actor_uri)DO UPDATE SET revision=owned_federation_delivery_peer_epochs.revision+1,security_revision=owned_federation_delivery_peer_epochs.security_revision+security_delta;
 END IF;
 IF TG_OP<>'DELETE' THEN
  INSERT INTO owned_federation_delivery_peer_epochs(publisher_id,actor_uri,security_revision)VALUES(NEW.publisher_id,NEW.actor_uri,security_delta)
  ON CONFLICT(publisher_id,actor_uri)DO UPDATE SET revision=owned_federation_delivery_peer_epochs.revision+1,security_revision=owned_federation_delivery_peer_epochs.security_revision+security_delta;
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD;END IF;RETURN NEW;
END$$;
DROP TRIGGER IF EXISTS owned_delivery_relationships ON owned_federation_relationships;
CREATE TRIGGER owned_delivery_relationships AFTER INSERT OR UPDATE OR DELETE ON owned_federation_relationships FOR EACH ROW EXECUTE FUNCTION owned_federation_delivery_peer_bump();
DROP TRIGGER IF EXISTS owned_delivery_blocks ON owned_federation_blocks;
CREATE TRIGGER owned_delivery_blocks AFTER INSERT OR UPDATE OR DELETE ON owned_federation_blocks FOR EACH ROW EXECUTE FUNCTION owned_federation_delivery_peer_bump();
CREATE OR REPLACE FUNCTION owned_federation_delivery_immutable()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
 IF NEW.id IS DISTINCT FROM OLD.id OR NEW.publisher_id IS DISTINCT FROM OLD.publisher_id OR NEW.actor_uri IS DISTINCT FROM OLD.actor_uri OR NEW.inbox_uri IS DISTINCT FROM OLD.inbox_uri OR NEW.activity_id IS DISTINCT FROM OLD.activity_id OR NEW.activity_type IS DISTINCT FROM OLD.activity_type OR NEW.payload IS DISTINCT FROM OLD.payload OR NEW.post_id IS DISTINCT FROM OLD.post_id OR NEW.post_updated_at IS DISTINCT FROM OLD.post_updated_at OR NEW.post_audit_id IS DISTINCT FROM OLD.post_audit_id OR NEW.origin_intent IS DISTINCT FROM OLD.origin_intent OR NEW.remote_binding IS DISTINCT FROM OLD.remote_binding OR NEW.local_fingerprint IS DISTINCT FROM OLD.local_fingerprint OR NEW.max_attempts IS DISTINCT FROM OLD.max_attempts OR NEW.expires_at IS DISTINCT FROM OLD.expires_at OR NEW.peer_security_epoch IS DISTINCT FROM OLD.peer_security_epoch OR NEW.peer_epoch IS DISTINCT FROM OLD.peer_epoch OR NEW.created_at IS DISTINCT FROM OLD.created_at OR (OLD.attempts>0 AND NEW.quote_id IS DISTINCT FROM OLD.quote_id) THEN
  RAISE EXCEPTION 'Delivery intent is immutable' USING ERRCODE='23514';
 END IF;
 IF NEW.attempts<OLD.attempts OR (OLD.status IN('sent','dead','cancelled') AND NEW IS DISTINCT FROM OLD) THEN
  RAISE EXCEPTION 'Delivery result is terminal and attempts are monotonic' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END$$;
DROP TRIGGER IF EXISTS owned_delivery_immutable ON owned_federation_deliveries;
CREATE TRIGGER owned_delivery_immutable BEFORE UPDATE ON owned_federation_deliveries FOR EACH ROW EXECUTE FUNCTION owned_federation_delivery_immutable();
