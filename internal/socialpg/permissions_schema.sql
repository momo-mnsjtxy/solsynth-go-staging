-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit owned migration. Numeric ranks are caller-visible; permission masks
-- below are private owned storage for the eight authored booleans.
ALTER TABLE owned_realm_members DROP CONSTRAINT IF EXISTS owned_realm_members_role_check;
ALTER TABLE owned_realm_members ADD CONSTRAINT owned_realm_members_role_check CHECK(role BETWEEN 0 AND 100);
ALTER TABLE owned_realm_members ADD COLUMN IF NOT EXISTS permission_generation bigint NOT NULL DEFAULT 1 CHECK(permission_generation>0);
CREATE TABLE IF NOT EXISTS owned_realm_role_permissions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 realm_id uuid NOT NULL REFERENCES owned_realms(id),
 role_level integer NOT NULL CHECK(role_level BETWEEN 0 AND 100),
 generation bigint NOT NULL DEFAULT 1 CHECK(generation BETWEEN 1 AND 9007199254740991),
 permission_mask integer NOT NULL CHECK(permission_mask BETWEEN 0 AND 255),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 deleted_at timestamptz,
 UNIQUE(realm_id,role_level),
 CHECK(role_level<>100 OR permission_mask=255),
 CHECK(role_level NOT IN(0,50,100) OR deleted_at IS NULL)
);
CREATE TABLE IF NOT EXISTS owned_realm_user_permissions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 realm_id uuid NOT NULL REFERENCES owned_realms(id),
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 member_id uuid NOT NULL REFERENCES owned_realm_members(id) ON DELETE CASCADE,
 membership_since timestamptz NOT NULL,
 allow_mask integer NOT NULL CHECK(allow_mask BETWEEN 0 AND 255),
 deny_mask integer NOT NULL CHECK(deny_mask BETWEEN 0 AND 255),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(realm_id,account_id),
 CHECK((allow_mask & deny_mask)=0)
);
INSERT INTO owned_realm_role_permissions(realm_id,role_level,permission_mask)
 SELECT r.id,p.level,p.mask FROM owned_realms r CROSS JOIN(VALUES(0,15),(50,127),(100,255))p(level,mask)
 ON CONFLICT(realm_id,role_level)DO NOTHING;

-- Ordinary invoker rights only. Unknown/deleted roles and stale membership
-- incarnations never acquire grants from role rank or a previous override.
CREATE OR REPLACE FUNCTION owned_realm_permission_mask(realm_uuid uuid,actor_uuid uuid)
RETURNS integer LANGUAGE sql VOLATILE AS $$
 SELECT COALESCE((SELECT CASE
  WHEN r.account_id=actor_uuid THEN 255
  WHEN m.role=100 OR rp.id IS NULL OR rp.deleted_at IS NOT NULL OR rp.generation<>m.permission_generation THEN 0
  ELSE (rp.permission_mask | COALESCE(u.allow_mask,0)) & (~COALESCE(u.deny_mask,0)) & 255 END
 FROM owned_realms r
 JOIN owned_authority_accounts owner_account ON owner_account.id=r.account_id
 JOIN owned_realm_members m ON m.realm_id=r.id AND m.account_id=actor_uuid
 JOIN owned_authority_accounts actor_account ON actor_account.id=m.account_id
 LEFT JOIN owned_realm_role_permissions rp ON rp.realm_id=r.id AND rp.role_level=m.role
 LEFT JOIN owned_realm_user_permissions u ON u.realm_id=r.id AND u.account_id=m.account_id AND u.member_id=m.id AND u.membership_since=m.joined_at
 WHERE r.id=realm_uuid AND r.deleted_at IS NULL AND m.joined_at IS NOT NULL AND m.leave_at IS NULL
 AND NOT actor_account.disabled AND NOT owner_account.disabled
 AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id IN(actor_uuid,r.account_id)AND p.kind='disable_account'AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp()))),0)::integer
$$;
