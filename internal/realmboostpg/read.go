// SPDX-License-Identifier: AGPL-3.0-only
package realmboostpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	b "solsynth.local/go-migration/ports/passport/realmboosts"
)

func (r *Repository) read(ctx context.Context, actor, ref string) (tx *sql.Tx, realm string, e error) {
	if r == nil {
		return nil, "", b.ErrUnavailable
	}
	if actor != "" && !authority.ValidID(actor) {
		return nil, "", authority.ErrInvalid
	}
	tx, e = r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return
	}
	defer func() {
		if e != nil {
			tx.Rollback()
		}
	}()
	if actor != "" {
		if _, e = r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
			return
		}
	}
	e = tx.QueryRowContext(ctx, `SELECT r.id::text FROM owned_realms r JOIN owned_authority_accounts a ON a.id=r.account_id WHERE r.deleted_at IS NULL AND NOT a.disabled AND(r.id::text=$1 OR lower(r.slug)=lower($1)) AND(r.is_public OR EXISTS(SELECT 1 FROM owned_realm_members m WHERE m.realm_id=r.id AND m.account_id=NULLIF($2::text,'')::uuid AND m.joined_at IS NOT NULL AND m.leave_at IS NULL AND m.role>=0)) FOR SHARE OF r`, ref, actor).Scan(&realm)
	e = mapped(e)
	return
}

// BoostStateInTransaction is the canonical live entitlement projection used by
// social and administrative promotion gates. It never trusts stored boost_level.
func (r *Repository) BoostStateInTransaction(ctx context.Context, tx *sql.Tx, realm string) (shares int64, level int, e error) {
	if r == nil || tx == nil || !authority.ValidID(realm) {
		return 0, 0, b.ErrUnavailable
	}
	at := r.cfg.Now().UTC()
	e = tx.QueryRowContext(ctx, `SELECT COALESCE(sum(shares),0)::bigint FROM owned_realm_boost_purchases WHERE realm_id=$1::uuid AND fulfilled_at<=$2::timestamptz AND expires_at>=$2::timestamptz`, realm, at).Scan(&shares)
	return shares, b.Level(shares), e
}
func (r *Repository) Status(ctx context.Context, actor, ref string) (out b.Status, e error) {
	tx, realm, e := r.read(ctx, actor, ref)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	out.BoostPoints, out.BoostLevel, e = r.BoostStateInTransaction(ctx, tx, realm)
	if e != nil {
		return out, e
	}
	out.LabelCap = r.cfg.Policy.LabelCaps[out.BoostLevel]
	out.ExpiresAfterDays = b.ExpirationDays
	out.SupportedCurrencies = []string{"golds", "points"}
	out.DefaultCurrency = "golds"
	return out, tx.Commit()
}
func (r *Repository) Leaderboard(ctx context.Context, actor, ref string, take int) (out []b.Entry, e error) {
	out = []b.Entry{}
	if take < 1 || take > 200 {
		return out, b.ErrInvalid
	}
	tx, realm, e := r.read(ctx, actor, ref)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT account_id::text,COALESCE(sum(amount)FILTER(WHERE currency='golds'),0)::text,COALESCE(sum(amount)FILTER(WHERE currency='points'),0)::text,sum(shares)::bigint,count(*),max(fulfilled_at) FROM owned_realm_boost_purchases WHERE realm_id=$1::uuid AND fulfilled_at<=$2::timestamptz AND expires_at>=$2::timestamptz GROUP BY account_id ORDER BY sum(shares) DESC,max(fulfilled_at) DESC,account_id LIMIT $3`, realm, r.cfg.Now().UTC(), take)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var v b.Entry
		if e = rows.Scan(&v.AccountID, &v.AmountGolds, &v.AmountPoints, &v.Shares, &v.Boosts, &v.LastBoostedAt); e != nil {
			rows.Close()
			return out, e
		}
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	for i := range out {
		a := &accounts.Account{}
		e = tx.QueryRowContext(ctx, `SELECT id::text,name,nick,language,region,created_at FROM owned_authority_accounts WHERE id=$1::uuid AND NOT disabled`, out[i].AccountID).Scan(&a.ID, &a.Name, &a.Nick, &a.Language, &a.Region, &a.CreatedAt)
		if errors.Is(e, sql.ErrNoRows) {
			out[i].Account = nil
		} else if e != nil {
			return out, e
		} else {
			out[i].Account = a
		}
	}
	return out, tx.Commit()
}
func (r *Repository) Experience(ctx context.Context, actor, ref, member string, take int) (out []b.Experience, e error) {
	out = []b.Experience{}
	if !authority.ValidID(member) || take < 1 || take > 200 {
		return out, b.ErrInvalid
	}
	tx, realm, e := r.read(ctx, actor, ref)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT id::text,realm_id::text,account_id::text,order_id::text,delta,created_at FROM owned_realm_boost_experience WHERE realm_id=$1::uuid AND account_id=$2::uuid ORDER BY created_at DESC,id LIMIT $3`, realm, member, take)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var v b.Experience
		if e = rows.Scan(&v.ID, &v.RealmID, &v.AccountID, &v.Reason, &v.Delta, &v.CreatedAt); e != nil {
			rows.Close()
			return out, e
		}
		v.ReasonType = "realm.boost.purchase"
		v.UpdatedAt = v.CreatedAt
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	return out, tx.Commit()
}
