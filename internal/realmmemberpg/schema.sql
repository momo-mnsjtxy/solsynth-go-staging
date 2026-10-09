-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit installation after authority, social, and realm boosts. No runtime DDL.
CREATE TABLE IF NOT EXISTS owned_realm_labels (
 id uuid PRIMARY KEY, realm_id uuid NOT NULL REFERENCES owned_realms(id),
 name text NOT NULL CHECK(length(name)<=1024 AND length(btrim(name))>=1),
 description text CHECK(length(description)<=4096), color text CHECK(length(color)<=64), icon text CHECK(length(icon)<=256),
 created_by_account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,
 UNIQUE(realm_id,id)
);
CREATE INDEX IF NOT EXISTS owned_realm_labels_order ON owned_realm_labels(realm_id,name,id);
CREATE UNIQUE INDEX IF NOT EXISTS owned_realm_members_identity ON owned_realm_members(id,realm_id);
CREATE TABLE IF NOT EXISTS owned_realm_label_assignments (
 member_id uuid PRIMARY KEY REFERENCES owned_realm_members(id), realm_id uuid NOT NULL REFERENCES owned_realms(id),
 label_id uuid NOT NULL, joined_at timestamptz NOT NULL, assigned_by uuid NOT NULL REFERENCES owned_authority_accounts(id), updated_at timestamptz NOT NULL,
 FOREIGN KEY(member_id,realm_id) REFERENCES owned_realm_members(id,realm_id),
 FOREIGN KEY(realm_id,label_id) REFERENCES owned_realm_labels(realm_id,id) ON DELETE CASCADE
);
-- Membership and realm are both checked on writes and reads. The joined_at
-- binding makes stale assignments inert across leave/invite/rejoin cycles.
CREATE TABLE IF NOT EXISTS owned_realm_label_requests (
 actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id), request_key text NOT NULL CHECK(length(request_key) BETWEEN 1 AND 128),
 realm_id uuid NOT NULL REFERENCES owned_realms(id), fingerprint text NOT NULL CHECK(length(fingerprint)=64),
 label_id uuid NOT NULL, created_at timestamptz NOT NULL, PRIMARY KEY(actor_id,request_key)
);
CREATE TABLE IF NOT EXISTS owned_realm_tenure_experience (
 id uuid PRIMARY KEY, realm_id uuid NOT NULL REFERENCES owned_realms(id), account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 day date NOT NULL, delta bigint NOT NULL CHECK(delta IN(5,7,10,12)), perk_level integer NOT NULL CHECK(perk_level BETWEEN 0 AND 3), created_at timestamptz NOT NULL,
 CHECK(delta=CASE perk_level WHEN 0 THEN 5 WHEN 1 THEN 7 WHEN 2 THEN 10 ELSE 12 END),
 CHECK(day=(created_at AT TIME ZONE 'UTC')::date), UNIQUE(realm_id,account_id,day)
);
CREATE INDEX IF NOT EXISTS owned_realm_tenure_history ON owned_realm_tenure_experience(realm_id,account_id,created_at DESC,id);
CREATE TABLE IF NOT EXISTS owned_realm_member_audit (
 id bigserial PRIMARY KEY, actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id), realm_id uuid NOT NULL REFERENCES owned_realms(id),
 target_id uuid NOT NULL,action text NOT NULL CHECK(action IN('label.created','label.updated','label.deleted','label.assigned','label.cleared','tenure.granted')),
 occurred_at timestamptz NOT NULL
);

-- Realm nick/bio are separate from the account profile and bound to the current
-- membership incarnation. Leaving and rejoining cannot revive an old override.
CREATE TABLE IF NOT EXISTS owned_realm_member_profiles (
 member_id uuid PRIMARY KEY REFERENCES owned_realm_members(id),
 realm_id uuid NOT NULL REFERENCES owned_realms(id), joined_at timestamptz NOT NULL,
 nick text CHECK(length(nick)<=1024),bio text CHECK(length(bio)<=4096),
 updated_at timestamptz NOT NULL,
 FOREIGN KEY(member_id,realm_id) REFERENCES owned_realm_members(id,realm_id)
);
CREATE TABLE IF NOT EXISTS owned_realm_profile_audit (
 id bigserial PRIMARY KEY,actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 realm_id uuid NOT NULL REFERENCES owned_realms(id),member_id uuid NOT NULL REFERENCES owned_realm_members(id),
 action text NOT NULL CHECK(action IN('profile.updated','profile.cleared')),occurred_at timestamptz NOT NULL
);
