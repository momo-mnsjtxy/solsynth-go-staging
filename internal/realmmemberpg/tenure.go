// SPDX-License-Identifier: AGPL-3.0-only
package realmmemberpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
	m "solsynth.local/go-migration/ports/passport/realmmembers"
	"time"
)

func tenureXP(perk int) (int64, error) {
	if perk < 0 || perk > 3 {
		return 0, m.ErrUnavailable
	}
	if perk == 0 {
		return 5, nil
	}
	return int64(5 * (perk + 2) / 2), nil
}

// GrantTenure is an explicitly enabled, bounded owner maintenance flow. It grants
// only today's source-authored award, never caller-selected dates or deltas.
// The final clock is sampled after every planned account and realm lock.
func (r *Repository) GrantTenure(ctx context.Context, actor, ref string, q m.TenureRequest) (out m.TenureResult, e error) {
	defer func() { e = mapped(e) }()
	if r == nil || !r.cfg.EnableDailyTenure {
		return out, m.ErrUnavailable
	}
	if !authority.ValidID(actor) || q.Take < 1 || q.Take > 200 || q.After != "" && !authority.ValidID(q.After) {
		return out, m.ErrInvalid
	}
	// Resolve the bounded lock set under preflight proof, then reconstruct all
	// authority and eligibility after the locks in begin. The owner account lock
	// serializes realm writers; newly eligible earlier IDs wait for the next pass.
	preview, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return
	}
	defer preview.Rollback()
	if _, e = r.guard.Preflight(ctx, r.db, preview, actor, "realms.members.manage"); e != nil {
		return
	}
	before, e := observe(ctx, preview, ref)
	if e != nil {
		return
	}
	if before.Owner != actor {
		return out, m.ErrForbidden
	}
	rows, e := preview.QueryContext(ctx, `SELECT m.account_id::text FROM owned_realm_members m JOIN owned_authority_accounts a ON a.id=m.account_id WHERE m.realm_id=$1::uuid AND m.joined_at IS NOT NULL AND m.leave_at IS NULL AND NOT a.disabled AND($2::text='' OR m.account_id>NULLIF($2::text,'')::uuid) ORDER BY m.account_id LIMIT $3`, before.ID, q.After, q.Take+1)
	if e != nil {
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return
	}
	preview.Rollback()
	more := len(ids) > q.Take
	if more {
		ids = ids[:q.Take]
	}
	if more && len(ids) > 0 {
		next := ids[len(ids)-1]
		out.Next = &next
	}
	tx, v, e := r.begin(ctx, actor, ref, true, "realms.members.manage", ids)
	if e != nil {
		return
	}
	defer tx.Rollback()
	if v.ID != before.ID || v.Owner != actor {
		return out, m.ErrConflict
	}
	at := r.cfg.Now().UTC().Truncate(time.Microsecond)
	day := at.Format("2006-01-02")
	out.Day = day
	grantedPerks := map[string]int{}
	cutoff := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
	for _, account := range ids {
		out.Examined++
		var joined time.Time
		err := tx.QueryRowContext(ctx, `SELECT m.joined_at FROM owned_realm_members m JOIN owned_authority_accounts a ON a.id=m.account_id WHERE m.realm_id=$1::uuid AND m.account_id=$2::uuid AND m.joined_at IS NOT NULL AND m.leave_at IS NULL AND NOT a.disabled`, v.ID, account).Scan(&joined)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return out, err
		}
		if !joined.Before(cutoff) {
			continue
		}
		perk, err := r.boosts.MemberPerkInTransaction(ctx, tx, actor, v.ID, account, at)
		if err != nil {
			return out, err
		}
		delta, err := tenureXP(perk)
		if err != nil {
			return out, err
		}
		id, err := uid()
		if err != nil {
			return out, err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO owned_realm_tenure_experience(id,realm_id,account_id,day,delta,perk_level,created_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::date,$5::bigint,$6::integer,$7::timestamptz)ON CONFLICT(realm_id,account_id,day)DO NOTHING`, id, v.ID, account, day, delta, perk, at)
		if err != nil {
			return out, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return out, err
		}
		if n == 0 {
			continue
		}
		if e = audit(ctx, tx, actor, v.ID, id, "tenure.granted", at); e != nil {
			return
		}
		out.Awarded++
		grantedPerks[account] = perk
	}
	for account, before := range grantedPerks {
		after, err := r.boosts.MemberPerkInTransaction(ctx, tx, actor, v.ID, account, r.cfg.Now().UTC())
		if err != nil {
			return out, err
		}
		if before != after {
			return out, m.ErrRetry
		}
	}
	if e = r.authorize(ctx, tx, actor, v, "realms.members.manage", false); e != nil {
		return
	}
	if r.cfg.Now().UTC().Format("2006-01-02") != day {
		return out, m.ErrRetry
	}
	return out, tx.Commit()
}
