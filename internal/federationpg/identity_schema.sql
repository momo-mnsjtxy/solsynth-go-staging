-- SPDX-License-Identifier: AGPL-3.0-only
-- The explicit configured-origin initializer completes URI/key-ID anchors.
-- No key material is stored or created.
ALTER TABLE owned_federation_publishers ADD COLUMN IF NOT EXISTS identity_handle text;
ALTER TABLE owned_federation_publishers ADD COLUMN IF NOT EXISTS actor_uri text;
ALTER TABLE owned_federation_publishers ADD COLUMN IF NOT EXISTS key_id text;
UPDATE owned_federation_publishers f SET identity_handle=p.name FROM owned_publishers p WHERE p.id=f.publisher_id AND f.identity_handle IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS owned_federation_actor_uri ON owned_federation_publishers(actor_uri)WHERE actor_uri IS NOT NULL;
ALTER TABLE owned_federation_publishers DROP CONSTRAINT IF EXISTS owned_federation_identity_shape;
ALTER TABLE owned_federation_publishers ADD CONSTRAINT owned_federation_identity_shape CHECK(identity_handle IS NOT NULL AND length(identity_handle) BETWEEN 1 AND 256 AND((actor_uri IS NULL AND key_id IS NULL)OR(actor_uri IS NOT NULL AND key_id IS NOT NULL AND actor_uri LIKE 'https://%' AND length(actor_uri)<=2048 AND key_id=actor_uri||'#main-key')));
CREATE OR REPLACE FUNCTION owned_federation_identity_immutable()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Federation identities are permanent' USING ERRCODE='23514';END IF;
 IF TG_OP='INSERT' THEN
  IF NEW.identity_handle IS NULL THEN SELECT name INTO NEW.identity_handle FROM owned_publishers WHERE id=NEW.publisher_id;END IF;
  RETURN NEW;
 END IF;
 IF NEW.publisher_id IS DISTINCT FROM OLD.publisher_id OR(OLD.identity_handle IS NOT NULL AND NEW.identity_handle IS DISTINCT FROM OLD.identity_handle)OR(OLD.actor_uri IS NOT NULL AND NEW.actor_uri IS DISTINCT FROM OLD.actor_uri)OR(OLD.key_id IS NOT NULL AND NEW.key_id IS DISTINCT FROM OLD.key_id)THEN
  RAISE EXCEPTION 'Federation actor and key identifiers are immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END$$;
DROP TRIGGER IF EXISTS owned_federation_identity_immutable ON owned_federation_publishers;
CREATE TRIGGER owned_federation_identity_immutable BEFORE INSERT OR UPDATE OR DELETE ON owned_federation_publishers FOR EACH ROW EXECUTE FUNCTION owned_federation_identity_immutable();
