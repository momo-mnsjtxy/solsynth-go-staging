-- SPDX-License-Identifier: AGPL-3.0-only
ALTER TABLE owned_authority_factors DROP CONSTRAINT IF EXISTS owned_authority_factors_type_check;
ALTER TABLE owned_authority_factors ADD CONSTRAINT owned_authority_factors_type_check CHECK(type IN(0,3,4,5));
ALTER TABLE owned_authority_factors ALTER COLUMN secret DROP NOT NULL;
ALTER TABLE owned_authority_factors DROP CONSTRAINT IF EXISTS owned_authority_factors_secret_check;
ALTER TABLE owned_authority_factors ADD CONSTRAINT owned_authority_factors_secret_check CHECK((type=4 AND secret IS NULL)OR(type<>4 AND secret IS NOT NULL AND octet_length(secret)>0));
ALTER TABLE owned_authority_challenges ADD COLUMN IF NOT EXISTS auth_methods jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(auth_methods)='array');
CREATE TABLE IF NOT EXISTS owned_authority_factor_audit(
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,account_id uuid NOT NULL REFERENCES owned_authority_accounts(id),
 factor_id uuid NOT NULL,action text NOT NULL CHECK(action IN('pin.create','pin.enable','pin.disable','pin.delete')),occurred_at timestamptz NOT NULL
);
