// SPDX-License-Identifier: AGPL-3.0-only
package progressionpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/progression"
	"sort"
)

// ConfiguredLevelInTransaction projects the same one-based configured account
// level as Summary. It requires explicit policy; it never assumes a legacy curve.
// The caller locks/authenticates the account and keeps the supplied transaction.
func ConfiguredLevelInTransaction(ctx context.Context, tx *sql.Tx, actor string) (int, error) {
	if tx == nil || !authority.ValidID(actor) {
		return 0, progression.ErrInvalid
	}
	var raw []byte
	if e := tx.QueryRowContext(ctx, `SELECT thresholds FROM owned_progression_level_policy WHERE id=1 FOR SHARE NOWAIT`).Scan(&raw); e != nil {
		return 0, e
	}
	var thresholds []int64
	if e := json.Unmarshal(raw, &thresholds); e != nil {
		return 0, e
	}
	if e := validateThresholds(thresholds); e != nil {
		return 0, e
	}
	var xp int64
	if e := tx.QueryRowContext(ctx, `SELECT COALESCE(sum(delta),0)::bigint FROM owned_progression_experience WHERE account_id=$1::uuid`, actor).Scan(&xp); e != nil {
		return 0, e
	}
	return sort.Search(len(thresholds), func(i int) bool { return thresholds[i] > xp }), nil
}
