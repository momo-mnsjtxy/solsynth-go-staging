-- SPDX-License-Identifier: AGPL-3.0-only
CREATE TABLE IF NOT EXISTS notification_v1.devices (
 id uuid PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),label text NOT NULL CHECK(length(label)<=128),provider text NOT NULL CHECK(provider='fcm'),token_hash bytea NOT NULL UNIQUE CHECK(octet_length(token_hash)=32),sealed_token bytea NOT NULL,
 session_id uuid NOT NULL,account_version bigint NOT NULL,authorization_epoch bigint NOT NULL,created_at timestamptz NOT NULL,verified_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,revoked_at timestamptz,
 FOREIGN KEY(account_id,session_id) REFERENCES owned_authority_sessions(account_id,id),CHECK(expires_at>verified_at AND expires_at<=verified_at+interval '30 days')
);
CREATE TABLE IF NOT EXISTS notification_v1.device_challenges (
 id uuid PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),device_id uuid NOT NULL,label text NOT NULL,token_hash bytea NOT NULL CHECK(octet_length(token_hash)=32),sealed_token bytea NOT NULL,code_hash bytea NOT NULL CHECK(octet_length(code_hash)=32),
 session_id uuid NOT NULL,account_version bigint NOT NULL,authorization_epoch bigint NOT NULL,consent_hours integer NOT NULL CHECK(consent_hours BETWEEN 1 AND 720),created_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,consumed_at timestamptz,sent_at timestamptz,
 FOREIGN KEY(account_id,session_id) REFERENCES owned_authority_sessions(account_id,id),CHECK(expires_at>created_at AND expires_at<=created_at+interval '10 minutes')
);
CREATE INDEX IF NOT EXISTS notification_device_challenge_due ON notification_v1.device_challenges(expires_at,id);
