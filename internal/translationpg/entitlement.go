// SPDX-License-Identifier: AGPL-3.0-only
package translationpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/translation"
	"sort"
	"strings"
	"time"
)

type SystemSubscriptions struct{ identifiers []string }

func NewSystemSubscriptions(ids []string) (*SystemSubscriptions, error) {
	if len(ids) == 0 || len(ids) > 100 {
		return nil, translation.ErrInvalid
	}
	out := append([]string{}, ids...)
	sort.Strings(out)
	for i, id := range out {
		if id == "" || len(id) > 256 || strings.TrimSpace(id) != id || i > 0 && out[i-1] == id {
			return nil, translation.ErrInvalid
		}
	}
	return &SystemSubscriptions{out}, nil
}

// Require reads the current canonical paid-grant state. Only operator-mapped
// system definitions qualify; custom app subscriptions cannot confer this right.
// The caller has already locked/revalidated the canonical account and proof.
func (p *SystemSubscriptions) Require(ctx context.Context, tx *sql.Tx, actor string, now time.Time) error {
	if p == nil || tx == nil || !authority.ValidID(actor) {
		return translation.ErrUnavailable
	}
	rows, e := tx.QueryContext(ctx, `SELECT identifier FROM commerce_v1.definitions WHERE identifier=ANY($1::text[]) AND enabled AND NULLIF(document->>'app_identifier','') IS NULL ORDER BY identifier FOR SHARE`, p.identifiers)
	if e != nil {
		return e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if len(ids) == 0 {
		return translation.ErrEntitlement
	}
	var id string
	e = tx.QueryRowContext(ctx, `SELECT id::text FROM commerce_v1.subscriptions WHERE account_id=$1 AND identifier=ANY($2::text[]) AND status=1 AND begun_at<=$3 AND ended_at>$3 AND COALESCE((document->>'is_active')::boolean,false) AND NOT COALESCE((document->>'is_testing')::boolean,true) ORDER BY id LIMIT 1 FOR SHARE`, actor, ids, now).Scan(&id)
	if e == sql.ErrNoRows {
		return translation.ErrEntitlement
	}
	return e
}
