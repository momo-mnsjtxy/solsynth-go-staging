-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicit additive schema after authority, accounts, canonical media and commerce.
CREATE SEQUENCE owned_apple_pass_updates AS bigint MINVALUE 1;
CREATE TABLE owned_apple_passes (
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 pass_type varchar(255) NOT NULL, serial_number uuid NOT NULL,
 authentication_token char(48) NOT NULL CHECK(authentication_token ~ '^[0-9a-f]{48}$'),
 fingerprint char(64) NOT NULL CHECK(fingerprint ~ '^[0-9a-f]{64}$'),
 update_tag bigint NOT NULL CHECK(update_tag>0), updated_at timestamptz NOT NULL,
 PRIMARY KEY(pass_type,serial_number), UNIQUE(account_id,pass_type),
 CHECK(serial_number=account_id)
);
CREATE TABLE owned_apple_pass_registrations (
 pass_type varchar(255) NOT NULL, serial_number uuid NOT NULL,
 device_id varchar(255) NOT NULL, push_token varchar(512) NOT NULL,
 updated_at timestamptz NOT NULL,
 PRIMARY KEY(pass_type,serial_number,device_id),
 FOREIGN KEY(pass_type,serial_number) REFERENCES owned_apple_passes(pass_type,serial_number)
);
CREATE INDEX owned_apple_pass_devices ON owned_apple_pass_registrations(device_id,pass_type);
