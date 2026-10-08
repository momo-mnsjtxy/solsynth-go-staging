-- SPDX-License-Identifier: AGPL-3.0-only
-- Once-only owned activation, independent of account/session generations.
CREATE TABLE IF NOT EXISTS owned_authority_activations (
 account_id uuid PRIMARY KEY REFERENCES owned_authority_accounts(id),activated_at timestamptz NOT NULL,
 policy_id text NOT NULL CHECK(length(policy_id) BETWEEN 1 AND 128),policy jsonb NOT NULL CHECK(jsonb_typeof(policy)='object'),
 policy_sha256 text NOT NULL CHECK(length(policy_sha256)=64)
);
