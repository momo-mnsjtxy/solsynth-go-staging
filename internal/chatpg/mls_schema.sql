-- SPDX-License-Identifier: AGPL-3.0-only
-- Owned opaque delivery tables, installed explicitly after chat and authority.
CREATE TABLE IF NOT EXISTS owned_mls_devices (
 id varchar(256) PRIMARY KEY,
 account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 revoked_at timestamptz,
 next_sequence bigint NOT NULL DEFAULT 0 CHECK(next_sequence>=0)
);
CREATE INDEX IF NOT EXISTS owned_mls_account_devices ON owned_mls_devices(account_id);
CREATE TABLE IF NOT EXISTS owned_mls_key_packages (
 device_id varchar(256) NOT NULL REFERENCES owned_mls_devices(id),
 digest bytea NOT NULL CHECK(octet_length(digest)=32),
 package text NOT NULL CHECK(length(package)<=21848),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 consumed_at timestamptz,
 consumed_by uuid REFERENCES owned_authority_accounts(id),
 CHECK((consumed_at IS NULL)=(consumed_by IS NULL)),
 PRIMARY KEY(device_id,digest)
);
CREATE TABLE IF NOT EXISTS owned_mls_groups (
 id varchar(256) PRIMARY KEY,
 room_id uuid NOT NULL UNIQUE REFERENCES chat_rooms(id),
 epoch bigint NOT NULL DEFAULT 0 CHECK(epoch BETWEEN 0 AND 9007199254740991),
 state_version bigint NOT NULL DEFAULT 1 CHECK(state_version BETWEEN 1 AND 9007199254740991),
 bootstrap_device varchar(256) REFERENCES owned_mls_devices(id),
 policy jsonb NOT NULL DEFAULT '{}',
 group_info text,
 ratchet_tree text,
 info_epoch bigint,
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK((group_info IS NULL AND ratchet_tree IS NULL AND info_epoch IS NULL) OR (length(group_info)<=174764 AND length(ratchet_tree)<=174764 AND info_epoch=epoch))
);
CREATE TABLE IF NOT EXISTS owned_mls_memberships (
 group_id varchar(256) NOT NULL REFERENCES owned_mls_groups(id),
 device_id varchar(256) NOT NULL REFERENCES owned_mls_devices(id),
 member_id uuid NOT NULL REFERENCES chat_members(id),
 membership_since timestamptz NOT NULL,
 state varchar(16) NOT NULL CHECK(state IN('joining','welcomed','active')),
 epoch bigint NOT NULL CHECK(epoch>=0),
 expires_at timestamptz,
 reshare_required boolean NOT NULL DEFAULT false,
 PRIMARY KEY(group_id,device_id)
);
CREATE TABLE IF NOT EXISTS owned_mls_envelopes (
 id uuid PRIMARY KEY,
 group_id varchar(256) NOT NULL REFERENCES owned_mls_groups(id),
 device_id varchar(256) NOT NULL REFERENCES owned_mls_devices(id),
 member_id uuid NOT NULL REFERENCES chat_members(id),
 membership_since timestamptz NOT NULL,
 sequence bigint NOT NULL CHECK(sequence>0),
 state_version bigint NOT NULL CHECK(state_version>=1),
 type integer NOT NULL CHECK(type IN(4,5,6,7)),
 epoch bigint NOT NULL CHECK(epoch>=0),
 ciphertext text NOT NULL CHECK(length(ciphertext)<=87384),
 header text NOT NULL CHECK(length(header)<=5464),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 acked_at timestamptz,
 UNIQUE(device_id,sequence)
);
CREATE INDEX IF NOT EXISTS owned_mls_pending ON owned_mls_envelopes(device_id,sequence) WHERE acked_at IS NULL;
CREATE TABLE IF NOT EXISTS owned_mls_requests (
 group_id varchar(256) NOT NULL REFERENCES owned_mls_groups(id),
 state_version bigint NOT NULL CHECK(state_version>=1),
 sender_device varchar(256) NOT NULL REFERENCES owned_mls_devices(id),
 operation varchar(32) NOT NULL,
 client_id varchar(128) NOT NULL,
 digest bytea NOT NULL CHECK(octet_length(digest)=32),
 epoch bigint NOT NULL,
 delivered integer NOT NULL CHECK(delivered>=0),
 PRIMARY KEY(group_id,state_version,sender_device,operation,client_id)
);
