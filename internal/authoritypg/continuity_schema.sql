-- SPDX-License-Identifier: AGPL-3.0-only
-- Apply atomically before deploying any writer/reader using AuthorizationEpoch.
-- Existing capture columns deliberately retain their values: an already stale
-- token-generation capture must not become current during this migration.
ALTER TABLE owned_authority_sessions ADD COLUMN IF NOT EXISTS authorization_epoch bigint;
UPDATE owned_authority_sessions SET authorization_epoch=epoch WHERE authorization_epoch IS NULL;
ALTER TABLE owned_authority_sessions ALTER COLUMN authorization_epoch SET NOT NULL;
-- Every subsequent insertion must explicitly choose both generations.
ALTER TABLE owned_authority_sessions ALTER COLUMN authorization_epoch DROP DEFAULT;
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='owned_authority_sessions'::regclass AND conname='owned_authority_sessions_authorization_epoch_check') THEN
  ALTER TABLE owned_authority_sessions ADD CONSTRAINT owned_authority_sessions_authorization_epoch_check CHECK(authorization_epoch>0 AND authorization_epoch<=epoch);
 END IF;
END $$;
