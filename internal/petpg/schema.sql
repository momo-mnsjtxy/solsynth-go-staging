-- SPDX-License-Identifier: AGPL-3.0-only
-- Additive owned schema. Apply explicitly after authoritypg/schema.sql.
-- Runtime code never installs or alters schema. No wallet/payment relationships.
CREATE TABLE IF NOT EXISTS owned_pets (
 account_id uuid PRIMARY KEY REFERENCES owned_authority_accounts(id),
 version bigint NOT NULL CHECK(version BETWEEN 1 AND 1000000000000),
 coins bigint NOT NULL CHECK(coins BETWEEN 0 AND 1000000000),
 state jsonb NOT NULL CHECK(jsonb_typeof(state)='object' AND octet_length(state::text)<=16384),
 updated_at timestamptz NOT NULL,
 CHECK(state ?& ARRAY['version','coins','stats','inventory','equipment','daily','experience','level','name','species']),
 CHECK((state->>'version')::bigint=version AND (state->>'coins')::bigint=coins),
 CHECK((state->>'experience')::bigint BETWEEN 0 AND 1000000000 AND (state->>'level')::int BETWEEN 1 AND 100),
 CHECK((state->'stats') ?& ARRAY['satiety','cleanliness','mood','energy']),
 CHECK((state->'stats'->>'satiety')::int BETWEEN 0 AND 100 AND (state->'stats'->>'cleanliness')::int BETWEEN 0 AND 100 AND (state->'stats'->>'mood')::int BETWEEN 0 AND 100 AND (state->'stats'->>'energy')::int BETWEEN 0 AND 100),
 CHECK(jsonb_typeof(state->'inventory')='object' AND jsonb_typeof(state->'equipment')='object'),
 CHECK((state->'daily'->>'commands')::int BETWEEN 1 AND 100 AND (state->'daily'->>'rewarded_care')::int BETWEEN 0 AND 20)
);
-- A successful command and its exact response are committed alongside the pet.
-- Bounded request IDs and daily mutations limit growth; replay keys are retained.
CREATE TABLE IF NOT EXISTS owned_pet_commands (
 account_id uuid NOT NULL REFERENCES owned_pets(account_id),
 request_id text NOT NULL CHECK(request_id ~ '^[A-Za-z0-9_-]{16,64}$'),
 request_hash text NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),
 kind text NOT NULL CHECK(kind IN ('adopt','rename','feed','wash','play','rest','buy','equip','claim')),
 response jsonb NOT NULL CHECK(jsonb_typeof(response)='object' AND octet_length(response::text)<=16384),
 coin_delta bigint NOT NULL CHECK(coin_delta BETWEEN -1000000000 AND 1000000000),
 balance_after bigint NOT NULL CHECK(balance_after BETWEEN 0 AND 1000000000),
 version bigint NOT NULL CHECK(version BETWEEN 1 AND 1000000000000),
 created_at timestamptz NOT NULL,
 PRIMARY KEY(account_id,request_id), UNIQUE(account_id,version),
 CHECK((response->>'coins')::bigint=balance_after AND (response->>'version')::bigint=version)
);
CREATE INDEX IF NOT EXISTS owned_pet_commands_created ON owned_pet_commands(created_at,account_id);
