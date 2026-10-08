-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit owned preferences after canonical authority; no push/contact store.
CREATE TABLE IF NOT EXISTS owned_account_notification_preferences (
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),app_id varchar(128) NOT NULL,topic varchar(128) NOT NULL,
 preference text NOT NULL CHECK(preference IN('normal','silent','reject')),description varchar(1024) NOT NULL DEFAULT '',updated_at timestamptz NOT NULL,
 PRIMARY KEY(account_id,app_id,topic)
);
CREATE TABLE IF NOT EXISTS owned_account_settings_audit(id bigserial PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),app_id varchar(128) NOT NULL,topic varchar(128) NOT NULL,action text NOT NULL,occurred_at timestamptz NOT NULL);
