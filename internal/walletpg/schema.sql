-- SPDX-License-Identifier: AGPL-3.0-only
-- Independently owned wallet persistence v1; NOT an in-place legacy migration.
-- Apply explicitly to a dedicated database. Runtime never applies schema DDL.
CREATE SCHEMA IF NOT EXISTS wallet_v1;
CREATE TABLE wallet_v1.wallets (
 id uuid PRIMARY KEY, account_id uuid, realm_id uuid,
 name varchar(1024) NOT NULL, is_primary boolean NOT NULL DEFAULT false,
 public_id varchar(32), created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
 deleted_at timestamptz,
 CHECK ((account_id IS NULL) <> (realm_id IS NULL)),
 CHECK (public_id IS NULL OR public_id ~ '^DNW-[A-HJ-NP-Z2-9]{4}-[A-HJ-NP-Z2-9]{4}-[A-HJ-NP-Z2-9]{4}$')
);
CREATE UNIQUE INDEX wallet_primary_account ON wallet_v1.wallets(account_id) WHERE is_primary AND deleted_at IS NULL;
CREATE UNIQUE INDEX wallet_primary_realm ON wallet_v1.wallets(realm_id) WHERE is_primary AND deleted_at IS NULL;
-- Never recycle a currently retained public ID, even for a tombstone.
CREATE UNIQUE INDEX wallet_public_id ON wallet_v1.wallets(public_id) WHERE public_id IS NOT NULL;
CREATE INDEX wallet_account ON wallet_v1.wallets(account_id,created_at,id) WHERE deleted_at IS NULL;
CREATE INDEX wallet_realm ON wallet_v1.wallets(realm_id,created_at,id) WHERE deleted_at IS NULL;
CREATE TABLE wallet_v1.pockets (
 id uuid PRIMARY KEY, wallet_id uuid NOT NULL REFERENCES wallet_v1.wallets(id),currency varchar(128) NOT NULL,
 amount numeric(32,3) NOT NULL DEFAULT 0,held_amount numeric(32,3) NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,deleted_at timestamptz,
 UNIQUE(wallet_id,currency), CHECK (amount>=0 AND held_amount>=0),
 CHECK (amount+held_amount<=79228162514264337593543950335),CHECK(length(currency)>0)
);
CREATE TABLE wallet_v1.transactions (
 id uuid PRIMARY KEY,actor_id uuid NOT NULL,payer_wallet_id uuid NOT NULL REFERENCES wallet_v1.wallets(id),
 payee_wallet_id uuid NOT NULL REFERENCES wallet_v1.wallets(id),currency varchar(128) NOT NULL,
 amount numeric(32,3) NOT NULL,remarks varchar(4096),status integer NOT NULL,
 is_frozen boolean NOT NULL,require_confirmation boolean NOT NULL,
 frozen_at timestamptz,expires_at timestamptz,confirmed_at timestamptz,confirmation_received_at timestamptz,
 created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,deleted_at timestamptz,
 idempotency_key varchar(128),fingerprint char(64) NOT NULL,
 CHECK(payer_wallet_id<>payee_wallet_id),CHECK(amount>0 AND amount<=79228162514264337593543950335),
 CHECK(status IN (0,1,2,3,4)),CHECK(status NOT IN (0,1) OR expires_at IS NOT NULL),
 UNIQUE(actor_id,idempotency_key)
);
CREATE INDEX wallet_transaction_payer ON wallet_v1.transactions(payer_wallet_id,created_at DESC,id DESC);
CREATE INDEX wallet_transaction_payee ON wallet_v1.transactions(payee_wallet_id,created_at DESC,id DESC);
CREATE INDEX wallet_transaction_due ON wallet_v1.transactions(expires_at,id) WHERE status IN (0,1) AND deleted_at IS NULL;
CREATE TABLE wallet_v1.outbox (
 sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,event_id uuid NOT NULL UNIQUE,
 event_type text NOT NULL,aggregate_id uuid NOT NULL,payload jsonb NOT NULL,
 created_at timestamptz NOT NULL,published_at timestamptz,
 CHECK(event_type LIKE 'wallet.%')
);
-- The application has no API to mint balances, destroy history, change owners,
-- publish/ack events, or disable factor/realm verification. Admin SQL access is
-- outside the application trust boundary. An external publisher must provide
-- at-least-once delivery, idempotent consumers and a verified ack protocol.

-- Independent factor policy, deliberately not an import of Passport secrets.
-- No public enrollment or disable endpoint. Only trusted provisioning may set
-- this row. Missing, disabled or malformed policy fails closed.
CREATE TABLE wallet_v1.payment_policies (
 account_id uuid PRIMARY KEY,pin_required boolean NOT NULL,
 salt bytea,pin_mac bytea,iterations integer,pepper_id varchar(64),
 failed_attempts integer NOT NULL DEFAULT 0,locked_until timestamptz,
 disabled boolean NOT NULL DEFAULT false,updated_at timestamptz NOT NULL,
 CHECK(failed_attempts BETWEEN 0 AND 5),
 CHECK((pin_required AND salt IS NOT NULL AND pin_mac IS NOT NULL AND iterations IS NOT NULL AND pepper_id IS NOT NULL AND octet_length(salt)=32 AND octet_length(pin_mac)=32 AND iterations=600000 AND length(pepper_id)>0)
 OR (NOT pin_required AND salt IS NULL AND pin_mac IS NULL AND iterations IS NULL AND pepper_id IS NULL))
);

-- Independent realm wallet authority. These are not inferred publisher roles.
-- Only trusted provisioning may insert/update these grants; the wallet HTTP
-- family has no membership or role-write endpoint.
CREATE TABLE wallet_v1.realm_grants (
 account_id uuid NOT NULL,realm_id uuid NOT NULL,role varchar(6) NOT NULL,
 expires_at timestamptz,revoked_at timestamptz,updated_at timestamptz NOT NULL,
 PRIMARY KEY(account_id,realm_id),CHECK(role IN ('viewer','editor'))
);
