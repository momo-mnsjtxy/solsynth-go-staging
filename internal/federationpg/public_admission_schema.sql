-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit optional migration after DiscoverySchema; never applied at startup.
CREATE TABLE IF NOT EXISTS owned_federation_public_policies(
 origin text PRIMARY KEY CHECK(length(origin) BETWEEN 1 AND 2048),
 incarnation uuid NOT NULL UNIQUE,
 created_at timestamptz NOT NULL,
 revoked_at timestamptz
);
CREATE TABLE IF NOT EXISTS owned_federation_public_keys(
 actor_uri text PRIMARY KEY CHECK(length(actor_uri) BETWEEN 1 AND 2048),
 resource text NOT NULL CHECK(length(resource) BETWEEN 1 AND 512),
 inbox_uri text NOT NULL CHECK(length(inbox_uri) BETWEEN 1 AND 2048),
 key_id text NOT NULL UNIQUE CHECK(key_id=actor_uri||'#main-key'),
 fingerprint text NOT NULL UNIQUE CHECK(fingerprint~'^[0-9a-f]{64}$'),
 public_pem text NOT NULL CHECK(octet_length(public_pem) BETWEEN 1 AND 16384),
 incarnation uuid NOT NULL UNIQUE,
 origin text NOT NULL REFERENCES owned_federation_public_policies(origin),
 policy_incarnation uuid NOT NULL REFERENCES owned_federation_public_policies(incarnation),
 admitted_at timestamptz NOT NULL,
 revoked_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS owned_federation_public_resource ON owned_federation_public_keys(lower(resource));
CREATE TABLE IF NOT EXISTS owned_federation_public_audit(
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 origin text NOT NULL,actor_uri text,job_id uuid,
 action text NOT NULL CHECK(action IN('policy.initialize','key.admit','key.revoke','policy.revoke')),
 occurred_at timestamptz NOT NULL
);
CREATE OR REPLACE FUNCTION owned_federation_public_permanent()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Federation trust reservations are permanent' USING ERRCODE='23514';END IF;
 IF (to_jsonb(NEW)-'revoked_at') IS DISTINCT FROM (to_jsonb(OLD)-'revoked_at') OR OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS DISTINCT FROM OLD.revoked_at THEN
 RAISE EXCEPTION 'Explicit trust transition required; first-contact identities cannot rotate or reactivate' USING ERRCODE='23514';END IF;
 RETURN NEW;
END$$;
DROP TRIGGER IF EXISTS owned_federation_public_permanent ON owned_federation_public_keys;
CREATE TRIGGER owned_federation_public_permanent BEFORE UPDATE OR DELETE ON owned_federation_public_keys FOR EACH ROW EXECUTE FUNCTION owned_federation_public_permanent();
DROP TRIGGER IF EXISTS owned_federation_public_permanent ON owned_federation_public_policies;
CREATE TRIGGER owned_federation_public_permanent BEFORE UPDATE OR DELETE ON owned_federation_public_policies FOR EACH ROW EXECUTE FUNCTION owned_federation_public_permanent();
CREATE TABLE IF NOT EXISTS owned_federation_public_admission_jobs(
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
CREATE INDEX IF NOT EXISTS owned_federation_public_admission_due ON owned_federation_public_admission_jobs(next_attempt,id)WHERE status IN('pending','inflight');
CREATE OR REPLACE FUNCTION owned_federation_public_admission_identity_immutable()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
 IF NEW.id IS DISTINCT FROM OLD.id OR NEW.publisher_id IS DISTINCT FROM OLD.publisher_id OR NEW.owner_id IS DISTINCT FROM OLD.owner_id OR NEW.requester_id IS DISTINCT FROM OLD.requester_id OR NEW.requester_version IS DISTINCT FROM OLD.requester_version OR NEW.requester_session IS DISTINCT FROM OLD.requester_session OR NEW.requester_epoch IS DISTINCT FROM OLD.requester_epoch OR NEW.resource IS DISTINCT FROM OLD.resource OR NEW.actor_uri IS DISTINCT FROM OLD.actor_uri OR NEW.binding IS DISTINCT FROM OLD.binding OR NEW.created_at IS DISTINCT FROM OLD.created_at OR NEW.max_attempts IS DISTINCT FROM OLD.max_attempts OR NEW.expires_at IS DISTINCT FROM OLD.expires_at OR NEW.policy_stamp IS DISTINCT FROM OLD.policy_stamp THEN
  RAISE EXCEPTION 'Discovery identity binding is immutable' USING ERRCODE='23514';
 END IF;
 IF OLD.status IN('succeeded','dead','cancelled') AND NEW IS DISTINCT FROM OLD THEN
  RAISE EXCEPTION 'Discovery outcome is terminal' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END$$;
DROP TRIGGER IF EXISTS owned_federation_public_admission_identity_immutable ON owned_federation_public_admission_jobs;
CREATE TRIGGER owned_federation_public_admission_identity_immutable BEFORE UPDATE ON owned_federation_public_admission_jobs FOR EACH ROW EXECUTE FUNCTION owned_federation_public_admission_identity_immutable();


CREATE OR REPLACE FUNCTION owned_federation_public_job_no_delete()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
 RAISE EXCEPTION 'First-contact retry budgets and terminal jobs are permanent' USING ERRCODE='23514';
END$$;
DROP TRIGGER IF EXISTS owned_federation_public_job_no_delete ON owned_federation_public_admission_jobs;
CREATE TRIGGER owned_federation_public_job_no_delete BEFORE DELETE ON owned_federation_public_admission_jobs FOR EACH ROW EXECUTE FUNCTION owned_federation_public_job_no_delete();
