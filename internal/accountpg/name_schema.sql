-- SPDX-License-Identifier: AGPL-3.0-only
-- Apply after owned_publishers. Retired names never move to another identity.
CREATE TABLE IF NOT EXISTS owned_publisher_name_reservations(
 folded_name text PRIMARY KEY CHECK(folded_name=lower(folded_name) AND length(folded_name) BETWEEN 1 AND 256),
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 256 AND folded_name=lower(name)),publisher_id uuid NOT NULL REFERENCES owned_publishers(id),created_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS owned_publisher_names_owner ON owned_publisher_name_reservations(publisher_id);
INSERT INTO owned_publisher_name_reservations(folded_name,name,publisher_id,created_at)
 SELECT lower(name),name,id,created_at FROM owned_publishers ON CONFLICT(folded_name)DO NOTHING;
DO $$BEGIN
 IF EXISTS(SELECT 1 FROM owned_publishers p JOIN owned_publisher_name_reservations n ON n.folded_name=lower(p.name) WHERE n.publisher_id<>p.id)THEN
  RAISE EXCEPTION 'Publisher name reservation collision' USING ERRCODE='23505';
 END IF;
END$$;
CREATE OR REPLACE FUNCTION owned_publisher_reservation_immutable()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Publisher name reservations are permanent' USING ERRCODE='23514';END IF;
 IF NEW.folded_name IS DISTINCT FROM OLD.folded_name OR NEW.publisher_id IS DISTINCT FROM OLD.publisher_id OR NEW.name IS DISTINCT FROM OLD.name OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
  RAISE EXCEPTION 'Publisher name reservations are permanent' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END$$;
DROP TRIGGER IF EXISTS owned_publisher_reservation_immutable ON owned_publisher_name_reservations;
CREATE TRIGGER owned_publisher_reservation_immutable BEFORE UPDATE OR DELETE ON owned_publisher_name_reservations FOR EACH ROW EXECUTE FUNCTION owned_publisher_reservation_immutable();
CREATE OR REPLACE FUNCTION owned_publisher_reserve_name()RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE reserved_owner uuid; name_count integer; anchored boolean;
BEGIN
 IF TG_OP='UPDATE' AND NEW.name IS NOT DISTINCT FROM OLD.name THEN RETURN NEW;END IF;
 IF TG_OP='UPDATE' THEN
  IF to_regclass('owned_federation_publishers')IS NOT NULL THEN
   EXECUTE 'SELECT (to_jsonb(f)->>''actor_uri'') IS NOT NULL AND (to_jsonb(f)->>''key_id'') IS NOT NULL FROM owned_federation_publishers f WHERE publisher_id=$1' INTO anchored USING NEW.id;
   IF anchored IS FALSE THEN RAISE EXCEPTION 'Federation identity is not anchored' USING ERRCODE='23514';END IF;
  END IF;
  INSERT INTO owned_publisher_name_reservations(folded_name,name,publisher_id,created_at)VALUES(lower(OLD.name),OLD.name,OLD.id,OLD.created_at)ON CONFLICT DO NOTHING;
 END IF;
 SELECT publisher_id INTO reserved_owner FROM owned_publisher_name_reservations WHERE folded_name=lower(NEW.name);
 IF reserved_owner IS NOT NULL AND reserved_owner<>NEW.id THEN RAISE EXCEPTION 'Publisher name is permanently reserved' USING ERRCODE='23505';END IF;
 IF reserved_owner IS NULL THEN
  SELECT count(*) INTO name_count FROM owned_publisher_name_reservations WHERE publisher_id=NEW.id;
  IF name_count>=256 THEN RAISE EXCEPTION 'Publisher name reservation limit reached' USING ERRCODE='23514';END IF;
  INSERT INTO owned_publisher_name_reservations(folded_name,name,publisher_id,created_at)VALUES(lower(NEW.name),NEW.name,NEW.id,CASE WHEN TG_OP='INSERT' THEN NEW.created_at ELSE NEW.updated_at END)ON CONFLICT DO NOTHING;
  SELECT publisher_id INTO reserved_owner FROM owned_publisher_name_reservations WHERE folded_name=lower(NEW.name);
  IF reserved_owner IS DISTINCT FROM NEW.id THEN RAISE EXCEPTION 'Publisher name is permanently reserved' USING ERRCODE='23505';END IF;
 END IF;
 RETURN NEW;
END$$;
DROP TRIGGER IF EXISTS owned_publisher_reserve_name ON owned_publishers;
CREATE TRIGGER owned_publisher_reserve_name AFTER INSERT OR UPDATE OF name ON owned_publishers FOR EACH ROW EXECUTE FUNCTION owned_publisher_reserve_name();
