-- SPDX-License-Identifier: AGPL-3.0-only
CREATE TABLE IF NOT EXISTS owned_domain_trust_state(
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),generation bigint NOT NULL DEFAULT 0 CHECK(generation>=0)
);
INSERT INTO owned_domain_trust_state(singleton)VALUES(true)ON CONFLICT(singleton)DO NOTHING;
CREATE TABLE IF NOT EXISTS owned_domain_trust_rules(
 id uuid PRIMARY KEY,domain_pattern varchar(512) NOT NULL CHECK(length(domain_pattern)>0),
 protocol text CHECK(protocol IN('http','https')),port_restriction integer CHECK(port_restriction BETWEEN 1 AND 65535),
 reason varchar(256),priority integer NOT NULL CHECK(priority BETWEEN -1000000 AND 1000000),
 trust_level integer NOT NULL CHECK(trust_level IN(0,1,2)),is_active boolean NOT NULL,
 created_by_account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,deleted_at timestamptz
);
CREATE INDEX IF NOT EXISTS owned_domain_trust_rules_order ON owned_domain_trust_rules(priority DESC,domain_pattern,id)WHERE deleted_at IS NULL;
CREATE TABLE IF NOT EXISTS owned_domain_trust_metrics(
 domain varchar(253) PRIMARY KEY,check_count bigint NOT NULL CHECK(check_count>0),
 blocked_count bigint NOT NULL CHECK(blocked_count>=0),verified_count bigint NOT NULL CHECK(verified_count>=0),
 last_checked_at timestamptz NOT NULL,CHECK(blocked_count+verified_count<=check_count)
);
CREATE TABLE IF NOT EXISTS owned_domain_trust_audit(
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 rule_id uuid NOT NULL REFERENCES owned_domain_trust_rules(id),
 action text NOT NULL CHECK(action IN('create','update','delete')),
 detail jsonb NOT NULL CHECK(jsonb_typeof(detail)='object' AND octet_length(detail::text)<=8192),
 occurred_at timestamptz NOT NULL
);
