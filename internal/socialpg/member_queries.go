// SPDX-License-Identifier: AGPL-3.0-only
package socialpg

import (
	"context"
	"database/sql"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/presencepg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/presence"
	"solsynth.local/go-migration/ports/passport/social"
	"sort"
	"time"
)

func (r *Repository) MembersFiltered(ctx context.Context, actor, ref string, f social.MemberFilter) (out []social.Member, total int64, e error) {
	out = []social.Member{}
	if e = social.ValidateMemberFilter(f); e != nil {
		return
	}
	e = r.memberRead(ctx, actor, ref, func(tx *sql.Tx) error {
		realm, x := r.realm(ctx, tx, ref, false)
		if x != nil {
			return x
		}
		if x = canViewRealm(ctx, tx, realm, actor); x != nil {
			return x
		}
		rows, x := tx.QueryContext(ctx, `SELECT `+memberColumns+` FROM owned_realm_members m JOIN owned_authority_accounts a ON a.id=m.account_id WHERE m.realm_id=$1::uuid AND m.leave_at IS NULL AND m.joined_at IS NOT NULL AND NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp())) AND position(lower($2::text)in lower(a.name||' '||a.nick))>0 AND($3::text='' OR EXISTS(SELECT 1 FROM owned_realm_label_assignments z WHERE z.member_id=m.id AND z.realm_id=m.realm_id AND z.joined_at=m.joined_at AND z.label_id=NULLIF($3::text,'')::uuid)) ORDER BY m.id LIMIT 10001`, realm.ID, f.AccountName, f.LabelID)
		if x != nil {
			return x
		}
		all := []social.Member{}
		for rows.Next() {
			v, x := scanMember(rows)
			if x != nil {
				rows.Close()
				return x
			}
			all = append(all, v)
		}
		x = rows.Err()
		rows.Close()
		if x != nil {
			return x
		}
		if len(all) > 10000 {
			return social.ErrUnavailable
		}
		for i := range all {
			all[i].Account, x = account(ctx, tx, all[i].AccountID)
			if x != nil {
				return x
			}
			if x = r.enrichMember(ctx, tx, &all[i]); x != nil {
				return x
			}
		}
		// A scheduled punishment can become effective while an enrichment waits.
		// Reconcile all candidates from a fresh canonical query before paging.
		ids := make([]string, len(all))
		for i := range all {
			ids[i] = all[i].AccountID
		}
		live, x := tx.QueryContext(ctx, `SELECT id::text FROM owned_authority_accounts a WHERE id=ANY($1::uuid[])AND NOT disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account'AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp()))`, ids)
		if x != nil {
			return x
		}
		active := map[string]bool{}
		for live.Next() {
			var id string
			if x = live.Scan(&id); x != nil {
				break
			}
			active[id] = true
		}
		if x == nil {
			x = live.Err()
		}
		live.Close()
		if x != nil {
			return x
		}
		current := all[:0]
		for _, v := range all {
			if active[v.AccountID] {
				current = append(current, v)
			}
		}
		all = current
		if f.WithStatus {
			var now time.Time
			if x = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); x != nil {
				return x
			}
			for i := range all {
				v, x := presencepg.StatusInTransaction(ctx, tx, actor, all[i].AccountID, now)
				if errors.Is(x, presence.ErrNotFound) {
					all[i].Status = nil
				} else if x != nil {
					return x
				} else {
					all[i].Status = &v
				}
			}
		}
		xp := func(m social.Member) int64 {
			if m.Progression == nil {
				return 0
			}
			return m.Progression.RecordedExperience
		}
		sort.SliceStable(all, func(i, j int) bool {
			a, b := all[i], all[j]
			if f.WithStatus {
				online := func(m social.Member) bool { return m.Status != nil && m.Status.IsOnline }
				if online(a) != online(b) {
					return online(a)
				}
			}
			switch f.OrderBy {
			case "name_asc":
				if a.Account.Name != b.Account.Name {
					return a.Account.Name < b.Account.Name
				}
			case "role_desc":
				if a.Role != b.Role {
					return a.Role > b.Role
				}
			case "joined_at_asc":
				if !a.JoinedAt.Equal(*b.JoinedAt) {
					return a.JoinedAt.Before(*b.JoinedAt)
				}
			default:
				if xp(a) != xp(b) {
					return xp(a) > xp(b)
				}
			}
			if !a.JoinedAt.Equal(*b.JoinedAt) {
				return a.JoinedAt.Before(*b.JoinedAt)
			}
			return a.ID < b.ID
		})
		total = int64(len(all))
		start := min(f.Offset, len(all))
		out = all[start:min(start+f.Take, len(all))]
		return nil
	})
	return
}

var _ social.MemberQueryRepository = (*Repository)(nil)

type memberReadGuard interface {
	Preflight(context.Context, *sql.DB, *sql.Tx, string, ...string) (authority.Principal, error)
	Authenticate(context.Context, *sql.DB, *sql.Tx, string) (authority.Principal, error)
}

func (r *Repository) memberRead(ctx context.Context, actor, ref string, fn func(*sql.Tx) error) error {
	if r == nil || r.db == nil {
		return social.ErrUnavailable
	}
	g, ok := r.guard.(memberReadGuard)
	if actor != "" && (!ok || !r.requireGuard) {
		return authority.ErrUnavailable
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if actor != "" {
		if _, e = g.Preflight(ctx, r.db, tx, actor); e != nil {
			return e
		}
	}
	before, e := r.realm(ctx, tx, ref, false)
	if e != nil {
		return e
	}
	if e = canViewRealm(ctx, tx, before, actor); e != nil {
		return e
	}
	rows, e := tx.QueryContext(ctx, `SELECT account_id::text FROM owned_realm_members WHERE realm_id=$1::uuid AND leave_at IS NULL AND joined_at IS NOT NULL ORDER BY account_id LIMIT 10001`, before.ID)
	if e != nil {
		return e
	}
	ids := []string{before.AccountID}
	if actor != "" {
		ids = append(ids, actor)
	}
	n := 0
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			break
		}
		ids = append(ids, id)
		n++
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return e
	}
	if n > 10000 {
		return social.ErrUnavailable
	}
	sort.Strings(ids)
	last := ""
	for _, id := range ids {
		if id == last {
			continue
		}
		last = id
		var held string
		lock := " FOR SHARE NOWAIT"
		if id == actor {
			lock = " FOR UPDATE NOWAIT"
		}
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1::uuid`+lock, id).Scan(&held); e != nil {
			var pg *pgconn.PgError
			if errors.As(e, &pg) && pg.Code == "55P03" {
				return social.ErrConflict
			}
			return e
		}
	}
	var realm string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_realms WHERE id=$1::uuid AND account_id=$2::uuid AND deleted_at IS NULL FOR SHARE NOWAIT`, before.ID, before.AccountID).Scan(&realm); e != nil {
		return mapped(e)
	}
	ownerActive := func() error {
		var active bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE id=$1::uuid AND NOT disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account'AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp())))`, before.AccountID).Scan(&active); err != nil {
			return err
		}
		if !active {
			return social.ErrNotFound
		}
		return nil
	}
	if e = ownerActive(); e != nil {
		return e
	}
	if actor != "" {
		if _, e = g.Authenticate(ctx, r.db, tx, actor); e != nil {
			return e
		}
	}
	if e = fn(tx); e != nil {
		return mapped(e)
	}
	if e = ownerActive(); e != nil {
		return e
	}
	if actor != "" {
		if _, e = g.Authenticate(ctx, r.db, tx, actor); e != nil {
			return e
		}
	}
	return tx.Commit()
}
