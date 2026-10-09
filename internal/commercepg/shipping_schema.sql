-- SPDX-License-Identifier: AGPL-3.0-only
-- An app may submit an unverified address selector. Only canonical payer checkout
-- captures its owner/content. No supplied address text becomes trusted state.
CREATE TABLE IF NOT EXISTS commerce_v1.order_destinations (
 order_id uuid PRIMARY KEY REFERENCES commerce_v1.orders(id),address_contact_id uuid NOT NULL,
 account_id uuid REFERENCES owned_authority_accounts(id),address_snapshot text,contact_revision bigint,captured_at timestamptz,
 CHECK((account_id IS NULL AND address_snapshot IS NULL AND contact_revision IS NULL AND captured_at IS NULL)OR(account_id IS NOT NULL AND address_snapshot IS NOT NULL AND contact_revision IS NOT NULL AND length(address_snapshot) BETWEEN 1 AND 2048 AND contact_revision>0 AND captured_at IS NOT NULL))
);
CREATE OR REPLACE FUNCTION commerce_v1.immutable_order_destination() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR NEW.order_id<>OLD.order_id OR NEW.address_contact_id<>OLD.address_contact_id OR(OLD.captured_at IS NOT NULL AND NEW IS DISTINCT FROM OLD) THEN
  RAISE EXCEPTION 'order destination is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS immutable_order_destination ON commerce_v1.order_destinations;
CREATE TRIGGER immutable_order_destination BEFORE UPDATE OR DELETE ON commerce_v1.order_destinations FOR EACH ROW EXECUTE FUNCTION commerce_v1.immutable_order_destination();
CREATE OR REPLACE FUNCTION commerce_v1.immutable_paid_destination_projection() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.status<>0 AND EXISTS(SELECT 1 FROM commerce_v1.order_destinations d WHERE d.order_id=OLD.id AND d.captured_at IS NOT NULL)
 AND (NEW.document->'meta'->'fulfillment') IS DISTINCT FROM (OLD.document->'meta'->'fulfillment') THEN
  RAISE EXCEPTION 'paid destination projection is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS immutable_paid_destination_projection ON commerce_v1.orders;
CREATE TRIGGER immutable_paid_destination_projection BEFORE UPDATE ON commerce_v1.orders FOR EACH ROW EXECUTE FUNCTION commerce_v1.immutable_paid_destination_projection();
