-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit migration after authority, developer apps, bots and canonical chat.
CREATE TABLE IF NOT EXISTS owned_bot_enrollments (
 bot_id uuid NOT NULL REFERENCES owned_bots(id),room_id uuid NOT NULL REFERENCES chat_rooms(id),
 adapter text NOT NULL CHECK(length(adapter) BETWEEN 1 AND 64),adapter_binding text NOT NULL CHECK(length(adapter_binding) BETWEEN 1 AND 128),generation bigint NOT NULL CHECK(generation>0),active boolean NOT NULL,
 issuer_id uuid NOT NULL REFERENCES owned_authority_accounts(id),session_id uuid NOT NULL REFERENCES owned_authority_sessions(id),
 account_version bigint NOT NULL,session_epoch bigint NOT NULL,owner_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 owner_version bigint NOT NULL,app_epoch bigint NOT NULL CHECK(app_epoch>0),issuer_joined_at timestamptz NOT NULL,issuer_room_joined_at timestamptz NOT NULL,bot_version bigint NOT NULL,joined_at timestamptz NOT NULL,mls_binding text NOT NULL,expires_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,
 PRIMARY KEY(bot_id,room_id)
);
CREATE TABLE IF NOT EXISTS owned_bot_message_outbox (
 id uuid PRIMARY KEY,sequence bigserial UNIQUE,bot_id uuid NOT NULL,room_id uuid NOT NULL,
 generation bigint NOT NULL,adapter text NOT NULL,actor_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 session_id uuid NOT NULL REFERENCES owned_authority_sessions(id),account_version bigint NOT NULL,session_epoch bigint NOT NULL,
 message_id uuid NOT NULL REFERENCES chat_messages(id),message_updated_at timestamptz NOT NULL,actor_joined_at timestamptz NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN('pending','accepted','cancelled','failed')),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),reason text,
 lease_owner uuid,lease_until timestamptz,
 CHECK((lease_owner IS NULL)=(lease_until IS NULL)),
 created_at timestamptz NOT NULL,next_attempt_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,accepted_at timestamptz,
 UNIQUE(bot_id,message_id),FOREIGN KEY(bot_id,room_id)REFERENCES owned_bot_enrollments(bot_id,room_id)
);
CREATE INDEX IF NOT EXISTS owned_bot_message_due ON owned_bot_message_outbox(next_attempt_at,sequence)WHERE state='pending';
CREATE TABLE IF NOT EXISTS owned_bot_enrollment_renewals (
 bot_id uuid NOT NULL,room_id uuid NOT NULL,enrollment_generation bigint NOT NULL CHECK(enrollment_generation>0),
 extend_by_seconds bigint NOT NULL CHECK(extend_by_seconds BETWEEN 300 AND 86400),
 renew_before_seconds bigint NOT NULL CHECK(renew_before_seconds BETWEEN 60 AND 3600 AND renew_before_seconds<extend_by_seconds),
 consent_expires_at timestamptz NOT NULL,active boolean NOT NULL DEFAULT true,
 issuer_id uuid NOT NULL REFERENCES owned_authority_accounts(id),session_id uuid NOT NULL REFERENCES owned_authority_sessions(id),
 account_version bigint NOT NULL CHECK(account_version>0),session_epoch bigint NOT NULL CHECK(session_epoch>0),
 owner_id uuid NOT NULL REFERENCES owned_authority_accounts(id),owner_version bigint NOT NULL CHECK(owner_version>0),
 issuer_joined_at timestamptz NOT NULL,issuer_room_joined_at timestamptz NOT NULL,
 bot_version bigint NOT NULL,bot_joined_at timestamptz NOT NULL,app_epoch bigint NOT NULL CHECK(app_epoch>0),mls_binding text NOT NULL,adapter text NOT NULL,adapter_binding text NOT NULL,
 created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,revoked_at timestamptz,reason text,
 PRIMARY KEY(bot_id,room_id),FOREIGN KEY(bot_id,room_id)REFERENCES owned_bot_enrollments(bot_id,room_id),
 CHECK((active AND revoked_at IS NULL AND reason IS NULL)OR(NOT active AND revoked_at IS NOT NULL AND reason IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS owned_bot_enrollment_renewals_due ON owned_bot_enrollment_renewals(consent_expires_at,bot_id,room_id)WHERE active;
