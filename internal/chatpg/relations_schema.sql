-- SPDX-License-Identifier: AGPL-3.0-only
-- Install after canonical authority, social and chat schemas.
CREATE TABLE IF NOT EXISTS owned_chat_direct_pairs(
 account_low uuid NOT NULL REFERENCES owned_authority_accounts(id),account_high uuid NOT NULL REFERENCES owned_authority_accounts(id),room_id uuid NOT NULL UNIQUE REFERENCES chat_rooms(id),
 PRIMARY KEY(account_low,account_high),CHECK(account_low<account_high)
);
CREATE TABLE IF NOT EXISTS owned_chat_realm_links(room_id uuid PRIMARY KEY REFERENCES chat_rooms(id),realm_id uuid NOT NULL REFERENCES owned_realms(id));
CREATE INDEX IF NOT EXISTS owned_chat_realm_rooms ON owned_chat_realm_links(realm_id,room_id);
CREATE TABLE IF NOT EXISTS owned_chat_relations_audit(id bigserial PRIMARY KEY,actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),room_id uuid NOT NULL REFERENCES chat_rooms(id),action text NOT NULL,target_id uuid,occurred_at timestamptz NOT NULL);
