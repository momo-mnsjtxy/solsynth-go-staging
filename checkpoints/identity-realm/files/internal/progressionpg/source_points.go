// SPDX-License-Identifier: AGPL-3.0-only
package progressionpg

import (
	"context"
	"database/sql"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/progression"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"strconv"
	"time"
)

type SourcePointConfig struct {
	TreasuryAccountID, TreasuryWalletID, Currency string
	MaxPerGrant, MaxPerDay                        int64
}
type SourcePointPayout interface {
	PaySourcePointsInTransaction(context.Context, *sql.Tx, string, string, *string, int64, time.Time) error
	SourcePointCurrency() string
}
type FundedSourcePoints struct{ cfg SourcePointConfig }

func NewFundedSourcePoints(c SourcePointConfig) (*FundedSourcePoints, error) {
	if !authority.ValidID(c.TreasuryAccountID) || !authority.ValidID(c.TreasuryWalletID) || !wallet.ValidCurrency(c.Currency) || c.MaxPerGrant < 1 || c.MaxPerGrant > 1000000000 || c.MaxPerDay < c.MaxPerGrant || c.MaxPerDay > 1000000000000 {
		return nil, progression.ErrInvalid
	}
	return &FundedSourcePoints{c}, nil
}
func (p *FundedSourcePoints) SourcePointCurrency() string                   { return p.cfg.Currency }
func (r *Repository) WithSourcePointPayout(p SourcePointPayout) *Repository { r.points = p; return r }
func (p *FundedSourcePoints) PaySourcePointsInTransaction(ctx context.Context, tx *sql.Tx, actor, grant string, requested *string, points int64, now time.Time) error {
	if p == nil || tx == nil || !authority.ValidID(actor) || !authority.ValidID(grant) || actor == p.cfg.TreasuryAccountID || points < 1 || points > p.cfg.MaxPerGrant {
		return progression.ErrInvalid
	}
	currency := p.cfg.Currency
	if requested != nil && *requested != "" {
		if *requested != currency {
			return progression.ErrInvalid
		}
	}
	var valid bool
	// The immutable earned grant supplies account, amount and currency provenance.
	if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_progression_grants WHERE id=$1::uuid AND account_id=$2::uuid AND (reward->>'source_points')::bigint=$3::bigint AND COALESCE(NULLIF(reward->>'source_points_currency',''),$4::text)=$4::text)`, grant, actor, points, currency).Scan(&valid); e != nil {
		return e
	}
	if !valid {
		return authority.ErrForbidden
	}
	// Recipient's canonical account lock already precedes grant rows. NOWAIT on
	// treasury SHARE makes reverse account-order contention a retry, never a cycle.
	if e := tx.QueryRowContext(ctx, `SELECT NOT disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments u WHERE u.account_id=a.id AND u.kind='disable_account' AND(u.starts_at IS NULL OR u.starts_at<=clock_timestamp())AND(u.expires_at IS NULL OR u.expires_at>clock_timestamp())) FROM owned_authority_accounts a WHERE id=$1::uuid FOR SHARE NOWAIT`, p.cfg.TreasuryAccountID).Scan(&valid); e != nil {
		return e
	}
	if !valid {
		return authority.ErrForbidden
	}
	payeeWallet, e := walletpg.EnsurePrimaryWalletInTransaction(ctx, tx, actor, now)
	if e != nil {
		return e
	}
	payee := payeeWallet.ID
	if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wallet_v1.wallets WHERE id=$1::uuid AND account_id=$2::uuid AND realm_id IS NULL AND deleted_at IS NULL)`, p.cfg.TreasuryWalletID, p.cfg.TreasuryAccountID).Scan(&valid); e != nil {
		return e
	}
	if !valid || payee == p.cfg.TreasuryWalletID {
		return authority.ErrForbidden
	}
	// Serialize the rate window and canonical transfers across recipient accounts.
	if _, e := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('progression-budget:'||$1::text,0))`, p.cfg.TreasuryWalletID); e != nil {
		return e
	}
	var budgetNow time.Time
	if e := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&budgetNow); e != nil {
		return e
	}
	if budgetNow.UTC().Format("2006-01-02") != now.UTC().Format("2006-01-02") {
		return wallet.ErrConflict
	}
	now = budgetNow
	key := "progression:" + grant
	var existing bool
	if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wallet_v1.transactions WHERE actor_id=$1::uuid AND idempotency_key=$2::text AND deleted_at IS NULL)`, p.cfg.TreasuryAccountID, key).Scan(&existing); e != nil {
		return e
	}
	if !existing {
		var used string
		if e := tx.QueryRowContext(ctx, `SELECT COALESCE(sum(amount),0)::text FROM wallet_v1.transactions WHERE actor_id=$1::uuid AND payer_wallet_id=$2::uuid AND currency=$3::text AND idempotency_key LIKE 'progression:%' AND status=2 AND deleted_at IS NULL AND created_at>=date_trunc('day',$4::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AND created_at<(date_trunc('day',$4::timestamptz AT TIME ZONE 'UTC')+interval '1 day')AT TIME ZONE 'UTC'`, p.cfg.TreasuryAccountID, p.cfg.TreasuryWalletID, currency, now).Scan(&used); e != nil {
			return e
		}
		amount, e := wallet.ParseAmount(used)
		if e != nil {
			return e
		}
		limit, _ := wallet.ParseAmount(strconv.FormatInt(p.cfg.MaxPerDay-points, 10))
		if amount.Cmp(limit) > 0 {
			return progression.ErrUnavailable
		}
	}
	amount, e := wallet.ParseAmount(strconv.FormatInt(points, 10))
	if e != nil {
		return e
	}
	var fresh time.Time
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&fresh); e != nil {
		return e
	}
	if fresh.UTC().Format("2006-01-02") != now.UTC().Format("2006-01-02") {
		return wallet.ErrConflict
	}
	remark := "Earned progression grant " + grant
	_, e = walletpg.FundedTransfer(ctx, tx, p.cfg.TreasuryAccountID, p.cfg.TreasuryWalletID, payee, currency, amount, &remark, key, now)
	if e != nil {
		return e
	}
	// Wallet ownership is stable under wallet locks acquired by FundedTransfer;
	// sample account eligibility after every transfer wait, before caller commit.
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wallet_v1.wallets w JOIN owned_authority_accounts a ON a.id=w.account_id WHERE w.id=$1::uuid AND w.account_id=$2::uuid AND w.deleted_at IS NULL AND NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments u WHERE u.account_id=a.id AND u.kind='disable_account' AND(u.starts_at IS NULL OR u.starts_at<=clock_timestamp())AND(u.expires_at IS NULL OR u.expires_at>clock_timestamp())))`, p.cfg.TreasuryWalletID, p.cfg.TreasuryAccountID).Scan(&valid); e != nil {
		return e
	}
	if !valid {
		return authority.ErrForbidden
	}
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wallet_v1.wallets w JOIN owned_authority_accounts a ON a.id=w.account_id WHERE w.id=$1::uuid AND w.account_id=$2::uuid AND w.id=(SELECT canonical.id FROM wallet_v1.wallets canonical WHERE canonical.account_id=$2::uuid AND canonical.deleted_at IS NULL ORDER BY canonical.is_primary DESC,canonical.created_at,canonical.id LIMIT 1)AND w.deleted_at IS NULL AND NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments u WHERE u.account_id=a.id AND u.kind='disable_account' AND(u.starts_at IS NULL OR u.starts_at<=clock_timestamp())AND(u.expires_at IS NULL OR u.expires_at>clock_timestamp())))`, payee, actor).Scan(&valid); e != nil {
		return e
	}
	if !valid {
		return authority.ErrForbidden
	}
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&fresh); e != nil {
		return e
	}
	if fresh.UTC().Format("2006-01-02") != now.UTC().Format("2006-01-02") {
		return wallet.ErrConflict
	}
	return nil
}

// ProcessSourcePoints retries earned, unpaid grants without inventing balances,
// silently minting funds, or suppressing provider/treasury failures.
func (r *Repository) ProcessSourcePoints(ctx context.Context, limit int) (int, error) {
	if r == nil || r.points == nil {
		return 0, nil
	}
	if limit < 1 || limit > 1000 {
		return 0, progression.ErrInvalid
	}
	rows, e := r.db.QueryContext(ctx, `SELECT id::text,account_id::text FROM owned_progression_grants WHERE source_points_granted_at IS NULL AND (reward->>'source_points')::bigint>0 ORDER BY created_at,id LIMIT $1::integer`, limit)
	if e != nil {
		return 0, e
	}
	type item struct{ id, actor string }
	items := []item{}
	for rows.Next() {
		var v item
		if e = rows.Scan(&v.id, &v.actor); e != nil {
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
		applied := false
		e = r.within(ctx, "", nil, func(tx *sql.Tx, now time.Time) error {
			var active bool
			if e := tx.QueryRowContext(ctx, `SELECT NOT disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments u WHERE u.account_id=a.id AND u.kind='disable_account' AND(u.starts_at IS NULL OR u.starts_at<=clock_timestamp())AND(u.expires_at IS NULL OR u.expires_at>clock_timestamp())) FROM owned_authority_accounts a WHERE id=$1::uuid FOR UPDATE`, v.actor).Scan(&active); e != nil {
				return e
			}
			if !active {
				return nil
			}
			var points int64
			var currency *string
			var at *time.Time
			if e := tx.QueryRowContext(ctx, `SELECT (reward->>'source_points')::bigint,reward->>'source_points_currency',source_points_granted_at FROM owned_progression_grants WHERE id=$1::uuid AND account_id=$2::uuid FOR UPDATE`, v.id, v.actor).Scan(&points, &currency, &at); e != nil {
				return e
			}
			if at != nil {
				return nil
			}
			if e := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
				return e
			}
			if e := r.points.PaySourcePointsInTransaction(ctx, tx, v.actor, v.id, currency, points, now); e != nil {
				return e
			}
			if _, e := tx.ExecContext(ctx, `UPDATE owned_progression_grants SET source_points_granted_at=$2::timestamptz,updated_at=$2::timestamptz WHERE id=$1::uuid`, v.id, now); e != nil {
				return e
			}
			id, e := newID()
			if e != nil {
				return e
			}
			_, e = tx.ExecContext(ctx, `INSERT INTO owned_progression_outbox(id,account_id,grant_id,packet_type,payload,created_at)SELECT $2::uuid,account_id,id,'progression.points.granted',jsonb_build_object('grant_id',id,'source_points',(reward->>'source_points')::bigint,'currency',COALESCE(NULLIF(reward->>'source_points_currency',''),$4::text)),$3::timestamptz FROM owned_progression_grants WHERE id=$1::uuid`, v.id, id, now, r.points.SourcePointCurrency())
			applied = e == nil
			return e
		})
		if e != nil {
			var pg *pgconn.PgError
			if errors.Is(e, wallet.ErrConflict) {
				continue
			}
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

func (p *FundedSourcePoints) Ready(ctx context.Context, db *sql.DB) error {
	var active bool
	e := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wallet_v1.wallets w JOIN owned_authority_accounts a ON a.id=w.account_id WHERE w.id=$1::uuid AND w.account_id=$2::uuid AND w.realm_id IS NULL AND w.deleted_at IS NULL AND NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments u WHERE u.account_id=a.id AND u.kind='disable_account'AND(u.starts_at IS NULL OR u.starts_at<=clock_timestamp())AND(u.expires_at IS NULL OR u.expires_at>clock_timestamp())))`, p.cfg.TreasuryWalletID, p.cfg.TreasuryAccountID).Scan(&active)
	if e != nil {
		return e
	}
	if !active {
		return authority.ErrForbidden
	}
	return nil
}
func (r *Repository) Ready(ctx context.Context) error {
	if r == nil || r.db == nil || r.guard == nil {
		return progression.ErrUnavailable
	}
	_, e := r.db.ExecContext(ctx, `SELECT backdated_from FROM owned_progression_checkins LIMIT 0;SELECT grant_id,packet_type FROM owned_progression_outbox LIMIT 0;SELECT reward,source_points_granted_at FROM owned_progression_grants LIMIT 0;SELECT state_hash FROM owned_account_connection_challenges LIMIT 0;SELECT id,verified_at FROM notification_v1.devices LIMIT 0`)
	if e != nil {
		return e
	}
	if p, ok := r.points.(interface {
		Ready(context.Context, *sql.DB) error
	}); ok {
		return p.Ready(ctx, r.db)
	}
	return nil
}
