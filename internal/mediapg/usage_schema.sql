-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit additive upgrade for deployments already using the prior owned media
-- schema. Fresh installs have this same column in schema.sql. Existing records
-- retain the no-usage default; this label is never an authorization grant.
ALTER TABLE owned_media_files ADD COLUMN IF NOT EXISTS usage text NOT NULL DEFAULT '' CHECK(usage IN ('','post'));

-- The documented profile label remains descriptive and never grants a read.
ALTER TABLE owned_media_files DROP CONSTRAINT IF EXISTS owned_media_files_usage_check;
ALTER TABLE owned_media_files ADD CONSTRAINT owned_media_files_usage_check CHECK(usage IN ('','post','profile_picture'));
