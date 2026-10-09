-- SPDX-License-Identifier: AGPL-3.0-only
CREATE UNIQUE INDEX IF NOT EXISTS owned_connection_current_identity ON owned_account_connections(provider,provided_identifier) WHERE revoked_at IS NULL;
CREATE TABLE IF NOT EXISTS owned_account_connection_challenges(
 state_hash bytea PRIMARY KEY CHECK(octet_length(state_hash)=32),
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),session_id uuid NOT NULL,
 account_version bigint NOT NULL,session_epoch bigint NOT NULL,authorization_epoch bigint NOT NULL,
 provider text NOT NULL,verifier bytea NOT NULL,
 created_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,consumed_at timestamptz,
 FOREIGN KEY(account_id,session_id) REFERENCES owned_authority_sessions(account_id,id),
 CHECK(expires_at=created_at+interval '10 minutes')
);
CREATE INDEX IF NOT EXISTS owned_connection_challenge_expiry ON owned_account_connection_challenges(expires_at);
