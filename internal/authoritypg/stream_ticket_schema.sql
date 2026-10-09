-- SPDX-License-Identifier: AGPL-3.0-only
-- Shared one-use browser credentials for realtime transports. Only the random
-- ticket hash and the already authenticated admission coordinates are stored.
CREATE TABLE IF NOT EXISTS owned_authority_stream_tickets (
 ticket_hash bytea PRIMARY KEY CHECK(octet_length(ticket_hash)=32),
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id) ON DELETE CASCADE,
 session_id uuid NOT NULL,
 account_version bigint NOT NULL CHECK(account_version>0),
 session_epoch bigint NOT NULL CHECK(session_epoch>0),
 authorization_epoch bigint NOT NULL CHECK(authorization_epoch>0 AND authorization_epoch<=session_epoch),
 credential_use text NOT NULL CHECK(credential_use IN('user','oauth-v1')),
 issued_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL CHECK(expires_at>issued_at AND expires_at<=issued_at+interval '30 seconds'),
 FOREIGN KEY(account_id,session_id) REFERENCES owned_authority_sessions(account_id,id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS owned_authority_stream_tickets_account_expiry
 ON owned_authority_stream_tickets(account_id,expires_at);
