-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit migration after canonical commerce schema; never constructor I/O.
CREATE TABLE IF NOT EXISTS commerce_v1.subscription_maintenance_quarantine(
 subscription_id uuid PRIMARY KEY REFERENCES commerce_v1.subscriptions(id),
 reason text NOT NULL CHECK(reason IN('invalid_document','projection_mismatch')),
 document_sha256 char(64) NOT NULL CHECK(document_sha256~'^[0-9a-f]{64}$'),
 first_seen_at timestamptz NOT NULL,last_seen_at timestamptz NOT NULL,resolved_at timestamptz,
 observations bigint NOT NULL DEFAULT 1 CHECK(observations>0),
 CHECK(last_seen_at>=first_seen_at),CHECK(resolved_at IS NULL OR resolved_at>=last_seen_at)
);
CREATE INDEX IF NOT EXISTS commerce_subscription_quarantined ON commerce_v1.subscription_maintenance_quarantine(subscription_id) WHERE resolved_at IS NULL;
