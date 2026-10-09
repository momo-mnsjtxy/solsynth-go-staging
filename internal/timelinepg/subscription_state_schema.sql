-- SPDX-License-Identifier: AGPL-3.0-only
-- Additive owned migration, after timeline schema; never applied at startup.
ALTER TABLE owned_publisher_follows ADD COLUMN IF NOT EXISTS last_read_at timestamptz;
ALTER TABLE owned_publisher_follows ADD COLUMN IF NOT EXISTS ended_by_account_id uuid REFERENCES owned_authority_accounts(id);
