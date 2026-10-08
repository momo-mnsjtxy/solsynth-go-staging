-- SPDX-License-Identifier: AGPL-3.0-only
-- Apply after owned authority, app, and canonical wallet schemas. Independent
-- schema only; never read/mutate legacy tables. No runtime DDL or seeding.
CREATE SCHEMA IF NOT EXISTS commerce_v1;
CREATE UNIQUE INDEX wallet_commerce_order ON wallet_v1.transactions(managed_by) WHERE purpose='commerce' AND managed_by IS NOT NULL;
CREATE TABLE commerce_v1.merchants (
 app_id uuid PRIMARY KEY REFERENCES owned_custom_apps(id), wallet_id uuid NOT NULL REFERENCES wallet_v1.wallets(id)
);
CREATE TABLE commerce_v1.definitions (
 identifier varchar(256) PRIMARY KEY, document jsonb NOT NULL, payee_wallet_id uuid NOT NULL REFERENCES wallet_v1.wallets(id), enabled boolean NOT NULL DEFAULT true
);
CREATE TABLE commerce_v1.orders (
 id uuid PRIMARY KEY, app_id uuid REFERENCES owned_custom_apps(id), actor_id uuid REFERENCES owned_authority_accounts(id),
 payer_wallet_id uuid REFERENCES wallet_v1.wallets(id), payee_wallet_id uuid NOT NULL REFERENCES wallet_v1.wallets(id),
 transaction_id uuid UNIQUE REFERENCES wallet_v1.transactions(id), status integer NOT NULL CHECK(status BETWEEN 0 AND 4),
 amount numeric(32,3) NOT NULL CHECK(amount>0), currency varchar(128) NOT NULL,
 product_identifier varchar(256), subscription_id uuid, gift_id uuid, created_at timestamptz NOT NULL, expires_at timestamptz NOT NULL,
 document jsonb NOT NULL, CHECK(NOT (subscription_id IS NOT NULL AND gift_id IS NOT NULL))
);
CREATE INDEX commerce_orders_actor ON commerce_v1.orders(actor_id,created_at DESC,id);
CREATE INDEX commerce_orders_due ON commerce_v1.orders(expires_at,id) WHERE status=0;
CREATE TABLE commerce_v1.order_items (
 order_id uuid NOT NULL REFERENCES commerce_v1.orders(id), product_id uuid NOT NULL REFERENCES owned_app_products(id), identifier text NOT NULL,
 quantity integer NOT NULL CHECK(quantity>0), PRIMARY KEY(order_id,product_id)
);
CREATE TABLE commerce_v1.subscriptions (
 id uuid PRIMARY KEY, account_id uuid NOT NULL REFERENCES owned_authority_accounts(id), identifier text NOT NULL REFERENCES commerce_v1.definitions(identifier),
 status integer NOT NULL CHECK(status BETWEEN 0 AND 3), begun_at timestamptz NOT NULL, ended_at timestamptz NOT NULL, renewal_at timestamptz,
 cycle_days integer NOT NULL CHECK(cycle_days BETWEEN 1 AND 366), auto_renew boolean NOT NULL, payer_wallet_id uuid REFERENCES wallet_v1.wallets(id),
 document jsonb NOT NULL
);
CREATE INDEX commerce_subscription_account ON commerce_v1.subscriptions(account_id,identifier,begun_at DESC,id);
CREATE INDEX commerce_subscription_due ON commerce_v1.subscriptions(ended_at,id) WHERE status=1;
CREATE TABLE commerce_v1.gifts (
 id uuid PRIMARY KEY, gifter_id uuid NOT NULL REFERENCES owned_authority_accounts(id), recipient_id uuid REFERENCES owned_authority_accounts(id),
 redeemer_id uuid REFERENCES owned_authority_accounts(id), subscription_id uuid UNIQUE REFERENCES commerce_v1.subscriptions(id),
 code varchar(32) NOT NULL UNIQUE, status integer NOT NULL CHECK(status BETWEEN 0 AND 4), expires_at timestamptz NOT NULL,
 cycle_days integer NOT NULL CHECK(cycle_days BETWEEN 1 AND 366), paid_order_id uuid UNIQUE REFERENCES commerce_v1.orders(id), document jsonb NOT NULL
);
ALTER TABLE commerce_v1.orders ADD FOREIGN KEY(subscription_id) REFERENCES commerce_v1.subscriptions(id);
ALTER TABLE commerce_v1.orders ADD FOREIGN KEY(gift_id) REFERENCES commerce_v1.gifts(id);
CREATE TABLE commerce_v1.idempotency (
 owner_key text NOT NULL, request_key varchar(128) NOT NULL, fingerprint char(64) NOT NULL, resource_id uuid NOT NULL, kind varchar(16) NOT NULL,
 PRIMARY KEY(owner_key,request_key)
);
CREATE TABLE commerce_v1.provider_receipts (
 provider varchar(32) NOT NULL, external_id varchar(256) NOT NULL, fingerprint char(64) NOT NULL, subscription_id uuid NOT NULL UNIQUE REFERENCES commerce_v1.subscriptions(id),
 PRIMARY KEY(provider,external_id)
);
CREATE TABLE commerce_v1.outbox (
 sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, event_id uuid NOT NULL UNIQUE, event_type text NOT NULL CHECK(event_type LIKE 'commerce.%'),
 aggregate_id uuid NOT NULL, payload jsonb NOT NULL, created_at timestamptz NOT NULL, published_at timestamptz
);
