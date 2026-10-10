-- SPDX-License-Identifier: AGPL-3.0-only
CREATE TABLE IF NOT EXISTS notification_v1.night_owl_receipts (
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),local_day date NOT NULL,local_hour integer NOT NULL CHECK(local_hour BETWEEN 0 AND 2),
 outbox_id uuid NOT NULL REFERENCES notification_v1.outbox(id),created_at timestamptz NOT NULL,PRIMARY KEY(account_id,local_day,local_hour)
);
