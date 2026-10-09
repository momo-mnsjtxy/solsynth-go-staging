-- SPDX-License-Identifier: AGPL-3.0-only
ALTER TABLE owned_authority_contacts ADD COLUMN IF NOT EXISTS revision bigint NOT NULL DEFAULT 1 CHECK(revision>0);
