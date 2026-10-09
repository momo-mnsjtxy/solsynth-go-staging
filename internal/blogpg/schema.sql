-- SPDX-License-Identifier: AGPL-3.0-only
-- Apply atomically after authority continuity/account/social/post schemas.
-- Explicit owned migration only; runtime constructors never install it.
CREATE TABLE IF NOT EXISTS owned_publisher_domains (
 id uuid PRIMARY KEY,publisher_id uuid NOT NULL REFERENCES owned_publishers(id),
 domain varchar(253) NOT NULL CHECK(domain=lower(domain)),status integer NOT NULL CHECK(status IN(0,1,2,3)),
 verified_at timestamptz,last_checked_at timestamptz,failed_attempts integer NOT NULL DEFAULT 0 CHECK(failed_attempts>=0),
 last_error varchar(4096),verified_name text,revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,deleted_at timestamptz,
 CHECK(status<>1 OR (verified_at IS NOT NULL AND verified_name IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS owned_publisher_domains_live ON owned_publisher_domains(publisher_id,domain) WHERE deleted_at IS NULL;
CREATE TABLE IF NOT EXISTS owned_publisher_domain_audit (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),domain_id uuid NOT NULL REFERENCES owned_publisher_domains(id),
 action text NOT NULL CHECK(action IN('add','recheck','verified','failed','delete','revoked')),
 revision bigint NOT NULL,occurred_at timestamptz NOT NULL
);

-- Original-proof admission for bounded pending verification recovery. No bearer
-- tokens or provider credentials are stored.
CREATE TABLE IF NOT EXISTS owned_publisher_domain_intents (
 domain_id uuid PRIMARY KEY REFERENCES owned_publisher_domains(id),
 actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),session_id uuid NOT NULL,
 account_version bigint NOT NULL CHECK(account_version>0),authorization_epoch bigint NOT NULL CHECK(authorization_epoch>0),
 revision bigint NOT NULL CHECK(revision>0)
);

-- A refreshed session cannot extend an existing verification admission. Defaults
-- invalidate admissions from an earlier schema rather than manufacture consent.
ALTER TABLE owned_publisher_domain_intents ADD COLUMN IF NOT EXISTS expires_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00';
ALTER TABLE owned_publisher_domain_intents ADD COLUMN IF NOT EXISTS policy_stamp jsonb NOT NULL DEFAULT '{}'::jsonb;

-- Proof belongs to the exact publisher identity, including its ownership. Any
-- change (even one later reversed) requires an explicit new domain recheck.
CREATE OR REPLACE FUNCTION owned_publisher_domains_revoke_identity() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
 IF NEW.name IS DISTINCT FROM OLD.name OR NEW.type IS DISTINCT FROM OLD.type OR NEW.account_id IS DISTINCT FROM OLD.account_id OR NEW.realm_id IS DISTINCT FROM OLD.realm_id THEN
  UPDATE owned_publisher_domains SET status=3,revision=revision+1,verified_name=NULL,updated_at=clock_timestamp(),last_error='Publisher identity changed' WHERE publisher_id=NEW.id AND deleted_at IS NULL;
 END IF;
 RETURN NEW;
END$$;
DROP TRIGGER IF EXISTS owned_publisher_domains_revoke_identity ON owned_publishers;
CREATE TRIGGER owned_publisher_domains_revoke_identity AFTER UPDATE OF name,type,account_id,realm_id ON owned_publishers FOR EACH ROW EXECUTE FUNCTION owned_publisher_domains_revoke_identity();

-- A monotonic, non-secret policy generation fences transient revoke/restore
-- changes across unlocked provider work and durable pending recovery.
CREATE TABLE IF NOT EXISTS owned_publisher_blog_authority_epochs (
 account_id uuid PRIMARY KEY REFERENCES owned_authority_accounts(id),revision bigint NOT NULL DEFAULT 1 CHECK(revision>0)
);
CREATE OR REPLACE FUNCTION owned_publisher_blog_bump_actor() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE old_actor uuid; new_actor uuid;
BEGIN
 IF TG_OP<>'INSERT' THEN old_actor=(to_jsonb(OLD)->>TG_ARGV[0])::uuid; END IF;
 IF TG_OP<>'DELETE' THEN new_actor=(to_jsonb(NEW)->>TG_ARGV[0])::uuid; END IF;
 INSERT INTO owned_publisher_blog_authority_epochs(account_id)
 SELECT DISTINCT actor FROM unnest(ARRAY[old_actor,new_actor]) actor WHERE actor IS NOT NULL
 ON CONFLICT(account_id)DO UPDATE SET revision=owned_publisher_blog_authority_epochs.revision+1;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW;
END$$;
CREATE OR REPLACE FUNCTION owned_publisher_blog_bump_group() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE old_group uuid; new_group uuid;
BEGIN
 IF TG_OP<>'INSERT' THEN old_group=OLD.group_id; END IF;
 IF TG_OP<>'DELETE' THEN new_group=NEW.group_id; END IF;
 INSERT INTO owned_publisher_blog_authority_epochs(account_id)
 SELECT DISTINCT account_id FROM owned_authority_group_members WHERE group_id=ANY(ARRAY[old_group,new_group])
 ON CONFLICT(account_id)DO UPDATE SET revision=owned_publisher_blog_authority_epochs.revision+1;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW;
END$$;
DROP TRIGGER IF EXISTS owned_publisher_blog_grants ON owned_authority_grants;
CREATE TRIGGER owned_publisher_blog_grants AFTER INSERT OR UPDATE OR DELETE ON owned_authority_grants FOR EACH ROW EXECUTE FUNCTION owned_publisher_blog_bump_actor('account_id');
DROP TRIGGER IF EXISTS owned_publisher_blog_punishments ON owned_authority_punishments;
CREATE TRIGGER owned_publisher_blog_punishments AFTER INSERT OR UPDATE OR DELETE ON owned_authority_punishments FOR EACH ROW EXECUTE FUNCTION owned_publisher_blog_bump_actor('account_id');
DROP TRIGGER IF EXISTS owned_publisher_blog_group_members ON owned_authority_group_members;
CREATE TRIGGER owned_publisher_blog_group_members AFTER INSERT OR UPDATE OR DELETE ON owned_authority_group_members FOR EACH ROW EXECUTE FUNCTION owned_publisher_blog_bump_actor('account_id');
DROP TRIGGER IF EXISTS owned_publisher_blog_group_grants ON owned_authority_group_grants;
CREATE TRIGGER owned_publisher_blog_group_grants AFTER INSERT OR UPDATE OR DELETE ON owned_authority_group_grants FOR EACH ROW EXECUTE FUNCTION owned_publisher_blog_bump_group();
DROP TRIGGER IF EXISTS owned_publisher_blog_members ON owned_publisher_members;
CREATE TRIGGER owned_publisher_blog_members AFTER INSERT OR UPDATE OR DELETE ON owned_publisher_members FOR EACH ROW EXECUTE FUNCTION owned_publisher_blog_bump_actor('account_id');
DROP TRIGGER IF EXISTS owned_publisher_blog_realm_members ON owned_realm_members;
CREATE TRIGGER owned_publisher_blog_realm_members AFTER INSERT OR UPDATE OR DELETE ON owned_realm_members FOR EACH ROW EXECUTE FUNCTION owned_publisher_blog_bump_actor('account_id');
DROP TRIGGER IF EXISTS owned_publisher_blog_account ON owned_authority_accounts;
CREATE TRIGGER owned_publisher_blog_account AFTER UPDATE OF version,disabled,is_superuser ON owned_authority_accounts FOR EACH ROW WHEN(NEW.version IS DISTINCT FROM OLD.version OR NEW.disabled IS DISTINCT FROM OLD.disabled OR NEW.is_superuser IS DISTINCT FROM OLD.is_superuser) EXECUTE FUNCTION owned_publisher_blog_bump_actor('id');

DROP TRIGGER IF EXISTS owned_publisher_blog_session ON owned_authority_sessions;
CREATE TRIGGER owned_publisher_blog_session AFTER UPDATE OF authorization_epoch,revoked_at,expires_at ON owned_authority_sessions FOR EACH ROW WHEN(NEW.authorization_epoch IS DISTINCT FROM OLD.authorization_epoch OR NEW.revoked_at IS DISTINCT FROM OLD.revoked_at OR NEW.expires_at<OLD.expires_at) EXECUTE FUNCTION owned_publisher_blog_bump_actor('account_id');
DROP TRIGGER IF EXISTS owned_publisher_blog_session_delete ON owned_authority_sessions;
CREATE TRIGGER owned_publisher_blog_session_delete AFTER DELETE ON owned_authority_sessions FOR EACH ROW EXECUTE FUNCTION owned_publisher_blog_bump_actor('account_id');

CREATE OR REPLACE FUNCTION owned_publisher_domains_revoke_realm_owner() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
 UPDATE owned_publisher_domains d SET status=3,revision=revision+1,verified_name=NULL,updated_at=clock_timestamp(),last_error='Publisher realm ownership changed' FROM owned_publishers p WHERE d.publisher_id=p.id AND p.realm_id=NEW.id AND d.deleted_at IS NULL;
 RETURN NEW;
END$$;
DROP TRIGGER IF EXISTS owned_publisher_domains_revoke_realm_owner ON owned_realms;
CREATE TRIGGER owned_publisher_domains_revoke_realm_owner AFTER UPDATE OF account_id ON owned_realms FOR EACH ROW WHEN(NEW.account_id IS DISTINCT FROM OLD.account_id) EXECUTE FUNCTION owned_publisher_domains_revoke_realm_owner();
