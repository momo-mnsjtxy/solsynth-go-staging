// SPDX-License-Identifier: AGPL-3.0-only
package realmmemberpg

import (
	"context"
	"database/sql"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	m "solsynth.local/go-migration/ports/passport/realmmembers"
	"time"
)

type maintenancePerks interface {
	MaintenancePerkInTransaction(context.Context, *sql.Tx, string, string, time.Time) (int, error)
}
type sourceAward struct {
	kind, realm, account, source string
	at                           time.Time
}

func sourceXP(kind string, perk int) (int64, error) {
	if perk < 0 || perk > 3 {
		return 0, m.ErrUnavailable
	}
	base := int64(2)
	if kind == "realm.post.created" {
		base = 20
	} else if kind != "realm.chat.message" {
		return 0, m.ErrInvalid
	}
	if perk == 0 {
		return base, nil
	}
	return base * int64(perk+2) / 2, nil
}

// ProcessDue derives awards from durable, committed canonical messages/posts and
// current day membership. No caller supplies deltas, identity or source metadata.
func (r *Repository) ProcessDue(ctx context.Context, limit int) (int, error) {
	if r == nil {
		return 0, nil
	}
	if limit < 1 || limit > 1000 {
		return 0, m.ErrInvalid
	}
	_, ok := r.boosts.(maintenancePerks)
	if !ok {
		return 0, m.ErrUnavailable
	}
	rows, e := r.db.QueryContext(ctx, `SELECT kind,realm::text,account::text,source::text,at FROM(
 SELECT 'realm.post.created'::text kind,p.realm_id realm,a.actor_id account,p.id source,p.published_at at FROM owned_text_posts p JOIN owned_text_post_audit a ON a.post_id=p.id AND a.action='create' WHERE p.realm_id IS NOT NULL AND p.deleted_at IS NULL AND p.published_at IS NOT NULL
 UNION ALL SELECT 'realm.chat.message',link.realm_id,s.account_id,p.id,p.created_at FROM chat_messages p JOIN chat_members s ON s.id=p.sender_id JOIN chat_rooms room ON room.id=p.chat_room_id JOIN owned_chat_realm_links link ON link.room_id=room.id WHERE p.deleted_at IS NULL AND room.deleted_at IS NULL AND room.owned_owner_id IS NOT NULL AND p.owned_request_hash IS NOT NULL AND p.type<>'placeholder'
 )candidate JOIN owned_realms ownedrealm ON ownedrealm.id=candidate.realm JOIN owned_authority_accounts sourceaccount ON sourceaccount.id=candidate.account JOIN owned_authority_accounts realmowner ON realmowner.id=ownedrealm.account_id WHERE ownedrealm.deleted_at IS NULL AND NOT sourceaccount.disabled AND NOT realmowner.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id IN(sourceaccount.id,realmowner.id)AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp())) AND at<=clock_timestamp() AND NOT EXISTS(SELECT 1 FROM owned_realm_source_receipts z WHERE z.realm_id=candidate.realm AND z.account_id=candidate.account AND z.source_kind=candidate.kind AND z.source_id=candidate.source)ORDER BY at,kind,source LIMIT $1::integer`, limit)
	if e != nil {
		return 0, e
	}
	items := []sourceAward{}
	for rows.Next() {
		var v sourceAward
		if e = rows.Scan(&v.kind, &v.realm, &v.account, &v.source, &v.at); e != nil {
			break
		}
		items = append(items, v)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return 0, e
	}
	count := 0
	for _, v := range items {
		applied, x := r.awardSource(ctx, v)
		if x != nil {
			var pg *pgconn.PgError
			if errors.As(x, &pg) && (pg.Code == "55P03" || pg.Code == "40P01" || pg.Code == "40001") {
				continue
			}
			return count, x
		}
		if applied {
			count++
		}
	}
	if r.cfg.EnableDailyTenure {
		n, x := r.processTenure(ctx, limit)
		count += n
		e = errors.Join(e, x)
	}
	return count, e
}
func (r *Repository) maintenanceBegin(ctx context.Context, realm, account string) (tx *sql.Tx, owner string, e error) {
	tx, e = r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return
	}
	defer func() {
		if e != nil {
			tx.Rollback()
		}
	}()
	if e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_realms WHERE id=$1::uuid AND deleted_at IS NULL`, realm).Scan(&owner); e != nil {
		return
	}
	if e = lockAccounts(ctx, tx, []string{owner, account}); e != nil {
		return
	}
	var actual string
	e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_realms WHERE id=$1::uuid AND deleted_at IS NULL FOR UPDATE`, realm).Scan(&actual)
	if e == nil && actual != owner {
		e = m.ErrConflict
	}
	return
}
func (r *Repository) awardSource(ctx context.Context, v sourceAward) (bool, error) {
	tx, _, e := r.maintenanceBegin(ctx, v.realm, v.account)
	if errors.Is(e, sql.ErrNoRows) || errors.Is(e, m.ErrForbidden) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	res, e := tx.ExecContext(ctx, `INSERT INTO owned_realm_source_receipts(realm_id,account_id,source_kind,source_id,processed_at,outcome)VALUES($1::uuid,$2::uuid,$3::text,$4::uuid,clock_timestamp(),'gone')ON CONFLICT DO NOTHING`, v.realm, v.account, v.kind, v.source)
	if e != nil {
		return false, e
	}
	n, e := res.RowsAffected()
	if e != nil || n == 0 {
		return false, e
	}
	var joined time.Time
	e = tx.QueryRowContext(ctx, `SELECT joined_at FROM owned_realm_members WHERE realm_id=$1::uuid AND account_id=$2::uuid AND joined_at IS NOT NULL AND joined_at<=$3::timestamptz AND leave_at IS NULL`, v.realm, v.account, v.at).Scan(&joined)
	if errors.Is(e, sql.ErrNoRows) {
		return true, tx.Commit()
	}
	if e != nil {
		return false, e
	}
	var at time.Time
	if v.kind == "realm.post.created" {
		e = tx.QueryRowContext(ctx, `SELECT p.published_at FROM owned_text_posts p WHERE p.id=$1::uuid AND p.realm_id=$2::uuid AND p.deleted_at IS NULL AND p.published_at IS NOT NULL AND EXISTS(SELECT 1 FROM owned_text_post_audit a WHERE a.post_id=p.id AND a.actor_id=$3::uuid AND a.action='create')FOR SHARE OF p NOWAIT`, v.source, v.realm, v.account).Scan(&at)
	} else {
		e = tx.QueryRowContext(ctx, `SELECT p.created_at FROM chat_messages p JOIN chat_members s ON s.id=p.sender_id JOIN chat_rooms room ON room.id=p.chat_room_id JOIN owned_chat_realm_links link ON link.room_id=room.id WHERE p.id=$1::uuid AND link.realm_id=$2::uuid AND s.account_id=$3::uuid AND p.deleted_at IS NULL AND room.deleted_at IS NULL AND p.owned_request_hash IS NOT NULL AND p.type<>'placeholder' FOR SHARE OF p,room NOWAIT`, v.source, v.realm, v.account).Scan(&at)
	}
	if errors.Is(e, sql.ErrNoRows) {
		return true, tx.Commit()
	}
	if e != nil {
		return false, e
	}
	if !at.Equal(v.at) {
		return false, m.ErrConflict
	}
	now := r.cfg.Now().UTC()
	perks := r.boosts.(maintenancePerks)
	perk, e := perks.MaintenancePerkInTransaction(ctx, tx, v.realm, v.account, now)
	if e != nil {
		return false, e
	}
	delta, e := sourceXP(v.kind, perk)
	if e != nil {
		return false, e
	}
	id, e := uid()
	if e != nil {
		return false, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_realm_source_experience(id,realm_id,account_id,source_kind,source_id,joined_at,delta,perk_level,created_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::text,$5::uuid,$6::timestamptz,$7::bigint,$8::integer,clock_timestamp())ON CONFLICT(realm_id,account_id,source_kind,source_id)DO NOTHING`, id, v.realm, v.account, v.kind, v.source, joined, delta, perk); e != nil {
		return false, e
	}
	fresh, e := perks.MaintenancePerkInTransaction(ctx, tx, v.realm, v.account, r.cfg.Now().UTC())
	if e != nil {
		return false, e
	}
	if fresh != perk {
		return false, m.ErrRetry
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_realm_source_receipts SET outcome='applied' WHERE realm_id=$1::uuid AND account_id=$2::uuid AND source_kind=$3::text AND source_id=$4::uuid`, v.realm, v.account, v.kind, v.source); e != nil {
		return false, e
	}
	return true, tx.Commit()
}
func (r *Repository) processTenure(ctx context.Context, limit int) (int, error) {
	rows, e := r.db.QueryContext(ctx, `SELECT m.realm_id::text,m.account_id::text FROM owned_realm_members m JOIN owned_realms r ON r.id=m.realm_id JOIN owned_authority_accounts a ON a.id=m.account_id JOIN owned_authority_accounts o ON o.id=r.account_id WHERE m.leave_at IS NULL AND m.joined_at<date_trunc('day',clock_timestamp() AT TIME ZONE 'UTC')AT TIME ZONE 'UTC' AND r.deleted_at IS NULL AND NOT a.disabled AND NOT o.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id IN(a.id,o.id)AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp())) AND NOT EXISTS(SELECT 1 FROM owned_realm_tenure_experience z WHERE z.realm_id=m.realm_id AND z.account_id=m.account_id AND z.day=(clock_timestamp() AT TIME ZONE 'UTC')::date)ORDER BY m.realm_id,m.account_id LIMIT $1::integer`, limit)
	if e != nil {
		return 0, e
	}
	items := []sourceAward{}
	for rows.Next() {
		var v sourceAward
		if e = rows.Scan(&v.realm, &v.account); e != nil {
			break
		}
		items = append(items, v)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return 0, e
	}
	count := 0
	for _, v := range items {
		applied, e := r.awardTenure(ctx, v.realm, v.account)
		if e != nil {
			if errors.Is(e, m.ErrRetry) || errors.Is(e, m.ErrConflict) {
				continue
			}
			var pg *pgconn.PgError
			if errors.As(e, &pg) && (pg.Code == "55P03" || pg.Code == "40P01" || pg.Code == "40001") {
				continue
			}
			return count, e
		}
		if applied {
			count++
		}
	}
	return count, nil
}
func (r *Repository) awardTenure(ctx context.Context, realm, account string) (bool, error) {
	tx, _, e := r.maintenanceBegin(ctx, realm, account)
	if errors.Is(e, sql.ErrNoRows) || errors.Is(e, m.ErrForbidden) || errors.Is(e, m.ErrNotFound) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	var now time.Time
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return false, e
	}
	day := now.UTC().Format("2006-01-02")
	var eligible bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_realm_members WHERE realm_id=$1::uuid AND account_id=$2::uuid AND leave_at IS NULL AND joined_at<date_trunc('day',$3::timestamptz AT TIME ZONE 'UTC')AT TIME ZONE 'UTC')`, realm, account, now).Scan(&eligible)
	if e != nil {
		return false, e
	}
	if !eligible {
		return false, nil
	}
	perks := r.boosts.(maintenancePerks)
	perk, e := perks.MaintenancePerkInTransaction(ctx, tx, realm, account, now)
	if e != nil {
		return false, e
	}
	delta, e := tenureXP(perk)
	if e != nil {
		return false, e
	}
	id, e := uid()
	if e != nil {
		return false, e
	}
	res, e := tx.ExecContext(ctx, `INSERT INTO owned_realm_tenure_experience(id,realm_id,account_id,day,delta,perk_level,created_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::date,$5::bigint,$6::integer,$7::timestamptz)ON CONFLICT(realm_id,account_id,day)DO NOTHING`, id, realm, account, day, delta, perk, now)
	if e != nil {
		return false, e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return false, e
	}
	fresh, e := perks.MaintenancePerkInTransaction(ctx, tx, realm, account, r.cfg.Now().UTC())
	if e != nil {
		return false, e
	}
	if fresh != perk {
		return false, m.ErrRetry
	}
	var end time.Time
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&end); e != nil {
		return false, e
	}
	if end.UTC().Format("2006-01-02") != day {
		return false, m.ErrRetry
	}
	return n > 0, tx.Commit()
}
