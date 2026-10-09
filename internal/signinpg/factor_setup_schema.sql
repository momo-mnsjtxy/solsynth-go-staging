-- SPDX-License-Identifier: AGPL-3.0-only
-- Receipt metadata only: canonical sessions, factors and credential assurance
-- remain the sole identity and secret authorities. Install after continuity.
ALTER TABLE owned_authority_factors ADD COLUMN IF NOT EXISTS revision bigint NOT NULL DEFAULT 1 CHECK(revision>0);
CREATE TABLE IF NOT EXISTS owned_authority_factor_setups(
 account_id uuid PRIMARY KEY REFERENCES owned_authority_accounts(id),session_id uuid NOT NULL REFERENCES owned_authority_sessions(id),
 account_version bigint NOT NULL CHECK(account_version>0),authorization_epoch bigint NOT NULL CHECK(authorization_epoch>0),
 auth_time timestamptz NOT NULL,expires_at timestamptz NOT NULL,policy_hash text NOT NULL CHECK(length(policy_hash)=64),
 recovery_id uuid,recovery_revision bigint,totp_id uuid,totp_revision bigint,
 state text NOT NULL CHECK(state IN('recovery','pending','complete','cancelled','disabled')),
 FOREIGN KEY(account_id,session_id) REFERENCES owned_authority_sessions(account_id,id),
 CHECK(expires_at=auth_time+interval '5 minutes'),
 CHECK((recovery_id IS NULL)=(recovery_revision IS NULL)),CHECK((totp_id IS NULL)=(totp_revision IS NULL)),
 CHECK(state<>'recovery' OR (recovery_id IS NOT NULL AND totp_id IS NULL)),CHECK(state<>'pending' OR totp_id IS NOT NULL)
);
CREATE TABLE IF NOT EXISTS owned_authority_pending_enrollments(
 factor_id uuid PRIMARY KEY,account_id uuid NOT NULL UNIQUE REFERENCES owned_authority_accounts(id),
 factor_revision bigint NOT NULL CHECK(factor_revision>0),account_version bigint NOT NULL CHECK(account_version>0),
 policy_hash text NOT NULL CHECK(length(policy_hash)=64),pending_until timestamptz NOT NULL,
 valid boolean NOT NULL DEFAULT true
);
CREATE INDEX IF NOT EXISTS owned_authority_pending_enrollment_account ON owned_authority_pending_enrollments(account_id);

-- Durable policy history belongs to the existing canonical owners. Surviving
-- row IDs/xmin cannot witness an empty -> insert -> delete cycle. These versions
-- are transactional: rollback restores both the version and setup provenance.
ALTER TABLE owned_authority_accounts ADD COLUMN IF NOT EXISTS factor_policy_version bigint NOT NULL DEFAULT 1 CHECK(factor_policy_version>0);
ALTER TABLE owned_authority_groups ADD COLUMN IF NOT EXISTS factor_policy_version bigint NOT NULL DEFAULT 1 CHECK(factor_policy_version>0);

CREATE OR REPLACE FUNCTION owned_authority_cancel_factor_policy(affected uuid) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
 UPDATE owned_authority_accounts SET factor_policy_version=factor_policy_version+1 WHERE id=affected;
 UPDATE owned_authority_factor_setups SET state='cancelled'
  WHERE account_id=affected AND state IN('recovery','pending','disabled');
 UPDATE owned_authority_pending_enrollments SET valid=false WHERE account_id=affected AND valid;
END;
$$;

-- SaveAccount permits trusted security-flag writes without requiring a version
-- bump. Remember disable/re-enable and superuser grant/revoke even when their
-- final values equal the pending enrollment's original account projection.
CREATE OR REPLACE FUNCTION owned_authority_account_factor_policy_changed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.disabled,NEW.is_superuser) IS DISTINCT FROM (OLD.disabled,OLD.is_superuser) THEN
  PERFORM owned_authority_cancel_factor_policy(NEW.id);
 END IF;
 RETURN NULL;
END;
$$;
DROP TRIGGER IF EXISTS owned_factor_policy_account_security ON owned_authority_accounts;
CREATE TRIGGER owned_factor_policy_account_security AFTER UPDATE OF disabled,is_superuser ON owned_authority_accounts FOR EACH ROW EXECUTE FUNCTION owned_authority_account_factor_policy_changed();

-- AFTER ROW avoids invalidation for an INSERT ... ON CONFLICT DO NOTHING and
-- catches every canonical writer, including trusted provisioning and exams.
-- Ordinary writers must retain their account-before-dependent-row lock order.
CREATE OR REPLACE FUNCTION owned_authority_factor_policy_changed() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
 old_account uuid;
 new_account uuid;
 affected uuid;
BEGIN
 IF TG_OP='UPDATE' THEN
  IF TG_TABLE_NAME='owned_authority_factors' THEN
   -- Successful TOTP login only advances replay protection. It must not cancel
   -- a pending enrollment or make the original five-minute receipt slide.
   IF (NEW.id,NEW.account_id,NEW.type,NEW.enabled_at,NEW.secret,NEW.pending_until,NEW.revision)
      IS NOT DISTINCT FROM
      (OLD.id,OLD.account_id,OLD.type,OLD.enabled_at,OLD.secret,OLD.pending_until,OLD.revision)
      AND NEW.last_counter>=OLD.last_counter THEN
    RETURN NULL;
   END IF;
  ELSIF NEW IS NOT DISTINCT FROM OLD THEN
   RETURN NULL;
  END IF;
 END IF;
 IF TG_OP<>'INSERT' THEN old_account:=OLD.account_id; END IF;
 IF TG_OP<>'DELETE' THEN new_account:=NEW.account_id; END IF;
 FOR affected IN SELECT DISTINCT id FROM unnest(ARRAY[old_account,new_account]) ids(id) WHERE id IS NOT NULL ORDER BY id LOOP
  PERFORM owned_authority_cancel_factor_policy(affected);
 END LOOP;
 RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION owned_authority_group_factor_policy_changed() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
 old_group uuid;
 new_group uuid;
 affected uuid;
BEGIN
 IF TG_OP='UPDATE' THEN
  IF NEW IS NOT DISTINCT FROM OLD THEN RETURN NULL; END IF;
 END IF;
 IF TG_OP<>'INSERT' THEN old_group:=OLD.group_id; END IF;
 IF TG_OP<>'DELETE' THEN new_group:=NEW.group_id; END IF;
 -- A group version also covers a concurrent joining member not yet visible to
 -- the member query below. Only fingerprints for this group's members use it.
 FOR affected IN SELECT DISTINCT id FROM unnest(ARRAY[old_group,new_group]) ids(id) WHERE id IS NOT NULL ORDER BY id LOOP
  UPDATE owned_authority_groups SET factor_policy_version=factor_policy_version+1 WHERE id=affected;
 END LOOP;
 FOR affected IN SELECT DISTINCT account_id FROM owned_authority_group_members
  WHERE group_id=old_group OR group_id=new_group ORDER BY account_id LOOP
  PERFORM owned_authority_cancel_factor_policy(affected);
 END LOOP;
 RETURN NULL;
END;
$$;

DROP TRIGGER IF EXISTS owned_factor_policy_grants ON owned_authority_grants;
CREATE TRIGGER owned_factor_policy_grants AFTER INSERT OR UPDATE OR DELETE ON owned_authority_grants FOR EACH ROW EXECUTE FUNCTION owned_authority_factor_policy_changed();
DROP TRIGGER IF EXISTS owned_factor_policy_punishments ON owned_authority_punishments;
CREATE TRIGGER owned_factor_policy_punishments AFTER INSERT OR UPDATE OR DELETE ON owned_authority_punishments FOR EACH ROW EXECUTE FUNCTION owned_authority_factor_policy_changed();
DROP TRIGGER IF EXISTS owned_factor_policy_members ON owned_authority_group_members;
CREATE TRIGGER owned_factor_policy_members AFTER INSERT OR UPDATE OR DELETE ON owned_authority_group_members FOR EACH ROW EXECUTE FUNCTION owned_authority_factor_policy_changed();
DROP TRIGGER IF EXISTS owned_factor_policy_group_grants ON owned_authority_group_grants;
CREATE TRIGGER owned_factor_policy_group_grants AFTER INSERT OR UPDATE OR DELETE ON owned_authority_group_grants FOR EACH ROW EXECUTE FUNCTION owned_authority_group_factor_policy_changed();
DROP TRIGGER IF EXISTS owned_factor_policy_credentials ON owned_authority_credentials;
CREATE TRIGGER owned_factor_policy_credentials AFTER INSERT OR UPDATE OR DELETE ON owned_authority_credentials FOR EACH ROW EXECUTE FUNCTION owned_authority_factor_policy_changed();
DROP TRIGGER IF EXISTS owned_factor_policy_factors ON owned_authority_factors;
CREATE TRIGGER owned_factor_policy_factors AFTER INSERT OR UPDATE OR DELETE ON owned_authority_factors FOR EACH ROW EXECUTE FUNCTION owned_authority_factor_policy_changed();
