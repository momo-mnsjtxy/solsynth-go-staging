// SPDX-License-Identifier: AGPL-3.0-only
package authoritypg

import (
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/authority"
)

func (t *transaction) SudoElevation(session string) (authority.SudoElevation, error) {
	var out authority.SudoElevation
	var methods []byte
	err := t.tx.QueryRowContext(t.ctx, `SELECT e.session_id::text,e.account_version,e.session_epoch,e.auth_time,e.expires_at,e.methods,e.trust_points FROM owned_authority_sudo_elevations e JOIN owned_authority_accounts a ON a.id=e.account_id WHERE e.account_id=$1 AND e.session_id=$2 AND e.factor_policy_version=a.factor_policy_version FOR UPDATE OF e`, t.account.ID, session).Scan(&out.SessionID, &out.AccountVersion, &out.SessionEpoch, &out.AuthTime, &out.ExpiresAt, &methods, &out.TrustPoints)
	if errors.Is(err, sql.ErrNoRows) {
		return out, authority.ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal(methods, &out.Methods)
	}
	return out, err
}
