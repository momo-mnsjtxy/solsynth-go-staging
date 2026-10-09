-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit atomic migration. Existing rows without an expiry remain permanent.
CREATE SEQUENCE IF NOT EXISTS owned_social_policy_revision;
ALTER TABLE owned_social_relationships DROP CONSTRAINT IF EXISTS owned_social_relationships_status_check;
ALTER TABLE owned_social_relationships ADD CONSTRAINT owned_social_relationships_status_check CHECK(status IN(-100,-50,100,200));
ALTER TABLE owned_social_relationships ADD COLUMN IF NOT EXISTS expired_at timestamptz;
ALTER TABLE owned_social_relationships ADD COLUMN IF NOT EXISTS degrade_to integer;
ALTER TABLE owned_social_relationships ADD COLUMN IF NOT EXISTS policy_revision bigint NOT NULL DEFAULT nextval('owned_social_policy_revision');
ALTER TABLE owned_social_relationships DROP CONSTRAINT IF EXISTS owned_social_relationship_expiry_shape;
ALTER TABLE owned_social_relationships ADD CONSTRAINT owned_social_relationship_expiry_shape CHECK((expired_at IS NULL OR status IN(-100,-50)) AND(degrade_to IS NULL OR(status=-100 AND expired_at IS NOT NULL AND degrade_to=-50)));
ALTER TABLE owned_social_requests ADD COLUMN IF NOT EXISTS expired_at timestamptz;
ALTER TABLE owned_social_requests ADD COLUMN IF NOT EXISTS policy_revision bigint NOT NULL DEFAULT nextval('owned_social_policy_revision');
CREATE INDEX IF NOT EXISTS owned_social_relationships_expiry ON owned_social_relationships(expired_at,id)WHERE expired_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS owned_social_requests_expiry ON owned_social_requests(expired_at,id)WHERE expired_at IS NOT NULL;
-- Every reader observes expiry even if cleanup is delayed. Only the sourced
-- block-to-muted transition survives its deadline; no friendship is invented.
CREATE OR REPLACE VIEW owned_social_effective_relationships AS
 WITH point AS MATERIALIZED(SELECT clock_timestamp() AS now)
 SELECT r.id,r.account_id,r.related_id,
 CASE WHEN r.expired_at<=point.now THEN r.degrade_to ELSE r.status END AS status,
 r.alias,r.created_at,r.updated_at,
 CASE WHEN r.expired_at<=point.now THEN NULL ELSE r.expired_at END AS expired_at,
 CASE WHEN r.expired_at<=point.now THEN NULL ELSE r.degrade_to END AS degrade_to,
 r.policy_revision
 FROM owned_social_relationships r CROSS JOIN point
 WHERE r.expired_at IS NULL OR r.expired_at>point.now OR r.degrade_to IS NOT NULL;
CREATE OR REPLACE VIEW owned_social_effective_requests AS
 SELECT id,account_id,related_id,created_at,expired_at,policy_revision
 FROM owned_social_requests WHERE expired_at IS NULL OR expired_at>clock_timestamp();
-- Durable local lifecycle events, not an instruction to contact either person.
CREATE TABLE IF NOT EXISTS owned_social_expiry_events(
 id uuid PRIMARY KEY,kind text NOT NULL CHECK(kind IN('relationship','request')),
 source_id uuid NOT NULL,policy_revision bigint NOT NULL,
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 related_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 previous_status integer NOT NULL CHECK(previous_status IN(-100,-50,0)),
 next_status integer CHECK(next_status=-50),expired_at timestamptz NOT NULL,
 processed_at timestamptz NOT NULL,UNIQUE(kind,source_id,policy_revision)
);
