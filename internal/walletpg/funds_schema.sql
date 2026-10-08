-- SPDX-License-Identifier: AGPL-3.0-only
-- Owned, additive fund schema. Canonical pockets remain the only balance store.
CREATE TABLE wallet_v1.funds(
 id uuid PRIMARY KEY,creator_account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 beneficiary_wallet_id uuid NOT NULL REFERENCES wallet_v1.wallets(id),currency varchar(128) NOT NULL CHECK(length(currency)>0),
 total_amount numeric(32,3) NOT NULL CHECK(total_amount>0),amount_of_splits integer NOT NULL CHECK(amount_of_splits BETWEEN 1 AND 512),split_type integer NOT NULL CHECK(split_type IN(0,1)),
 message varchar(4096),status integer NOT NULL DEFAULT 0 CHECK(status BETWEEN 0 AND 4),is_open boolean NOT NULL,is_raising boolean NOT NULL,
 target_amount numeric(32,3) NOT NULL CHECK(target_amount>=0),contribution_type integer NOT NULL CHECK(contribution_type IN(0,1)),contribution_amount numeric(32,3) NOT NULL CHECK(contribution_amount>=0),
 deadline_at timestamptz,expires_at timestamptz NOT NULL,created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,
 idempotency_key varchar(128),fingerprint char(64) NOT NULL,
 CHECK(expires_at>created_at),CHECK(deadline_at IS NULL OR(deadline_at>created_at AND deadline_at<=expires_at)),
 CHECK((is_raising AND target_amount=total_amount)OR(NOT is_raising AND target_amount=0)),
 UNIQUE(creator_account_id,idempotency_key)
);
CREATE TABLE wallet_v1.fund_reservations(
 id uuid PRIMARY KEY,fund_id uuid NOT NULL REFERENCES wallet_v1.funds(id),account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 payer_wallet_id uuid NOT NULL REFERENCES wallet_v1.wallets(id),amount numeric(32,3) NOT NULL CHECK(amount>0),created_at timestamptz NOT NULL,
 receipt_transaction_id uuid UNIQUE REFERENCES wallet_v1.transactions(id),
 UNIQUE(fund_id,account_id)
);
CREATE TABLE wallet_v1.fund_allocations(
 reservation_id uuid NOT NULL REFERENCES wallet_v1.fund_reservations(id),transaction_id uuid PRIMARY KEY REFERENCES wallet_v1.transactions(id),
 kind varchar(8) NOT NULL CHECK(kind IN('claim','payout','refund'))
);
CREATE INDEX wallet_fund_allocations_reservation ON wallet_v1.fund_allocations(reservation_id,transaction_id);
CREATE TABLE wallet_v1.fund_participants(
 id uuid PRIMARY KEY,fund_id uuid NOT NULL REFERENCES wallet_v1.funds(id),account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 is_invited boolean NOT NULL,received_at timestamptz,transaction_id uuid UNIQUE REFERENCES wallet_v1.transactions(id),reservation_id uuid UNIQUE REFERENCES wallet_v1.fund_reservations(id),
 UNIQUE(fund_id,account_id),CHECK((received_at IS NULL)=(transaction_id IS NULL)),CHECK(reservation_id IS NULL OR transaction_id IS NOT NULL)
);
CREATE INDEX wallet_funds_creator ON wallet_v1.funds(creator_account_id,created_at DESC,id);
CREATE INDEX wallet_funds_due ON wallet_v1.funds((LEAST(expires_at,deadline_at)),id)WHERE status IN(0,1);
ALTER TABLE wallet_v1.transactions DROP CONSTRAINT IF EXISTS transactions_purpose_check;
ALTER TABLE wallet_v1.transactions ADD CONSTRAINT transactions_purpose_check CHECK(purpose IN('transfer','commerce','fund'));
DO $$ DECLARE c record; BEGIN
 FOR c IN SELECT conname FROM pg_constraint WHERE conrelid='wallet_v1.transactions'::regclass AND contype='c' AND (pg_get_constraintdef(oid) LIKE '%payer_wallet_id <> payee_wallet_id%' OR pg_get_constraintdef(oid) LIKE '%purpose%commerce%managed_by%') LOOP
  EXECUTE format('ALTER TABLE wallet_v1.transactions DROP CONSTRAINT %I',c.conname);
 END LOOP;
END $$;
ALTER TABLE wallet_v1.transactions ADD COLUMN fund_kind varchar(8);
ALTER TABLE wallet_v1.transactions ADD CONSTRAINT wallet_managed_purpose CHECK((purpose IN('commerce','fund'))=(managed_by IS NOT NULL));
ALTER TABLE wallet_v1.transactions ADD CONSTRAINT wallet_fund_kind CHECK((purpose='fund' AND fund_kind IS NOT NULL AND fund_kind IN('hold','claim','payout','refund'))OR(purpose<>'fund' AND fund_kind IS NULL));
ALTER TABLE wallet_v1.transactions ADD CONSTRAINT wallet_fund_self_only CHECK(payer_wallet_id<>payee_wallet_id OR(purpose='fund' AND fund_kind='refund' AND status=3));
ALTER TABLE wallet_v1.payment_authorizations DROP CONSTRAINT IF EXISTS payment_authorizations_purpose_check;
ALTER TABLE wallet_v1.payment_authorizations ADD CONSTRAINT payment_authorizations_purpose_check CHECK(purpose IN('wallet.transfer','commerce.order.pay','wallet.fund.create','wallet.fund.contribute'));
CREATE FUNCTION wallet_v1.immutable_fund_record() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 RAISE EXCEPTION 'fund backing records are immutable';
END $$;
CREATE TRIGGER immutable_fund_reservation BEFORE UPDATE OR DELETE ON wallet_v1.fund_reservations FOR EACH ROW EXECUTE FUNCTION wallet_v1.immutable_fund_record();
CREATE TRIGGER immutable_fund_allocation BEFORE UPDATE OR DELETE ON wallet_v1.fund_allocations FOR EACH ROW EXECUTE FUNCTION wallet_v1.immutable_fund_record();
CREATE FUNCTION wallet_v1.immutable_fund_terms() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF (to_jsonb(OLD)-'status'-'updated_at') IS DISTINCT FROM (to_jsonb(NEW)-'status'-'updated_at') THEN
  RAISE EXCEPTION 'fund terms and beneficiary are immutable';
 END IF;
 IF OLD.status IN(2,3,4) AND NEW.status<>OLD.status THEN RAISE EXCEPTION 'fund is terminal'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER immutable_fund_terms BEFORE UPDATE ON wallet_v1.funds FOR EACH ROW EXECUTE FUNCTION wallet_v1.immutable_fund_terms();
-- Validate allocation linkage at the database boundary as well as in the service.
CREATE FUNCTION wallet_v1.validate_fund_allocation() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE r wallet_v1.fund_reservations; f wallet_v1.funds; t wallet_v1.transactions; allocated numeric;
BEGIN
 SELECT * INTO STRICT r FROM wallet_v1.fund_reservations WHERE id=NEW.reservation_id;
 SELECT * INTO STRICT f FROM wallet_v1.funds WHERE id=r.fund_id FOR UPDATE;
 SELECT * INTO STRICT t FROM wallet_v1.transactions WHERE id=NEW.transaction_id;
 IF t.purpose<>'fund' OR t.managed_by<>f.id OR t.payer_wallet_id<>r.payer_wallet_id OR t.currency<>f.currency OR t.fund_kind<>NEW.kind
 OR(NEW.kind='refund' AND t.status<>3)OR(NEW.kind<>'refund' AND t.status<>2)
 OR(NEW.kind='payout' AND(t.payee_wallet_id<>f.beneficiary_wallet_id OR NOT f.is_raising))
 OR(NEW.kind='claim' AND f.is_raising)
 OR(r.receipt_transaction_id IS NOT NULL AND r.receipt_transaction_id<>t.id) THEN RAISE EXCEPTION 'fund allocation mismatch'; END IF;
 SELECT COALESCE(sum(x.amount),0) INTO allocated FROM wallet_v1.fund_allocations a JOIN wallet_v1.transactions x ON x.id=a.transaction_id WHERE a.reservation_id=r.id;
 IF allocated+t.amount>r.amount THEN RAISE EXCEPTION 'fund allocation exceeds immutable reservation'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER validate_fund_allocation BEFORE INSERT ON wallet_v1.fund_allocations FOR EACH ROW EXECUTE FUNCTION wallet_v1.validate_fund_allocation();
