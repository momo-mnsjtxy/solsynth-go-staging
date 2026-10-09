-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit additive owned schema, after canonical funds/PIN schemas.
-- Runtime never creates a treasury, funds balances or installs an active policy.
CREATE TABLE wallet_v1.exchange_policy_versions (
 version varchar(64) PRIMARY KEY,
 config_hash char(64) NOT NULL UNIQUE CHECK(config_hash ~ '^[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL
);
CREATE TABLE wallet_v1.exchange_active_policy (
 singleton boolean PRIMARY KEY CHECK(singleton),
 version varchar(64) NOT NULL REFERENCES wallet_v1.exchange_policy_versions(version),
 enabled boolean NOT NULL DEFAULT false
);
CREATE FUNCTION wallet_v1.immutable_exchange_record() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 RAISE EXCEPTION 'exchange records are immutable';
END $$;
CREATE TRIGGER immutable_exchange_policy BEFORE UPDATE OR DELETE ON wallet_v1.exchange_policy_versions FOR EACH ROW EXECUTE FUNCTION wallet_v1.immutable_exchange_record();
CREATE TABLE wallet_v1.exchanges (
 id uuid PRIMARY KEY,
 actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 wallet_id uuid NOT NULL REFERENCES wallet_v1.wallets(id),
 treasury_wallet_id uuid NOT NULL REFERENCES wallet_v1.wallets(id),
 policy_version varchar(64) NOT NULL REFERENCES wallet_v1.exchange_policy_versions(version),
 policy_hash char(64) NOT NULL,
 source_currency varchar(64) NOT NULL,target_currency varchar(64) NOT NULL,
 source_amount numeric(32,3) NOT NULL CHECK(source_amount>0),target_amount numeric(32,3) NOT NULL CHECK(target_amount>0),
 quote jsonb NOT NULL,
 debit_transaction_id uuid NOT NULL UNIQUE REFERENCES wallet_v1.transactions(id),
 credit_transaction_id uuid NOT NULL UNIQUE REFERENCES wallet_v1.transactions(id),
 idempotency_key varchar(128) NOT NULL,
 fingerprint char(64) NOT NULL CHECK(fingerprint ~ '^[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL,
 UNIQUE(actor_id,idempotency_key),
 CHECK(wallet_id<>treasury_wallet_id AND source_currency<>target_currency AND debit_transaction_id<>credit_transaction_id)
);
CREATE TRIGGER immutable_exchange_receipt BEFORE UPDATE OR DELETE ON wallet_v1.exchanges FOR EACH ROW EXECUTE FUNCTION wallet_v1.immutable_exchange_record();
ALTER TABLE wallet_v1.transactions DROP CONSTRAINT transactions_purpose_check;
ALTER TABLE wallet_v1.transactions ADD CONSTRAINT transactions_purpose_check CHECK(purpose IN('transfer','commerce','fund','exchange'));
ALTER TABLE wallet_v1.transactions DROP CONSTRAINT wallet_managed_purpose;
ALTER TABLE wallet_v1.transactions ADD CONSTRAINT wallet_managed_purpose CHECK((purpose IN('commerce','fund','exchange'))=(managed_by IS NOT NULL));
ALTER TABLE wallet_v1.transactions ADD CONSTRAINT wallet_exchange_settled CHECK(purpose<>'exchange' OR(status=2 AND NOT is_frozen AND NOT require_confirmation AND confirmed_at IS NOT NULL));
ALTER TABLE wallet_v1.payment_authorizations DROP CONSTRAINT payment_authorizations_purpose_check;
ALTER TABLE wallet_v1.payment_authorizations ADD CONSTRAINT payment_authorizations_purpose_check CHECK(purpose IN('wallet.transfer','commerce.order.pay','wallet.fund.create','wallet.fund.contribute','wallet.exchange'));
CREATE FUNCTION wallet_v1.validate_exchange_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE d wallet_v1.transactions; c wallet_v1.transactions; h text;
BEGIN
 SELECT config_hash INTO STRICT h FROM wallet_v1.exchange_policy_versions WHERE version=NEW.policy_version;
 IF h<>NEW.policy_hash THEN RAISE EXCEPTION 'exchange policy mismatch'; END IF;
 SELECT * INTO STRICT d FROM wallet_v1.transactions WHERE id=NEW.debit_transaction_id;
 SELECT * INTO STRICT c FROM wallet_v1.transactions WHERE id=NEW.credit_transaction_id;
 IF d.purpose<>'exchange' OR c.purpose<>'exchange' OR d.managed_by<>NEW.id OR c.managed_by<>NEW.id
 OR d.actor_id<>NEW.actor_id OR c.actor_id<>NEW.actor_id OR d.status<>2 OR c.status<>2
 OR d.payer_wallet_id<>NEW.wallet_id OR d.payee_wallet_id<>NEW.treasury_wallet_id
 OR c.payer_wallet_id<>NEW.treasury_wallet_id OR c.payee_wallet_id<>NEW.wallet_id
 OR d.currency<>NEW.source_currency OR c.currency<>NEW.target_currency
 OR d.amount<>NEW.source_amount OR c.amount<>NEW.target_amount
 OR d.fingerprint<>NEW.fingerprint OR c.fingerprint<>NEW.fingerprint THEN RAISE EXCEPTION 'exchange leg mismatch'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER validate_exchange_receipt BEFORE INSERT ON wallet_v1.exchanges FOR EACH ROW EXECUTE FUNCTION wallet_v1.validate_exchange_receipt();
