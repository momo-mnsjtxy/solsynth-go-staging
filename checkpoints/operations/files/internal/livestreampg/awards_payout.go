// SPDX-License-Identifier: AGPL-3.0-only
package livestreampg

import (
	"context"
	"errors"
	"fmt"
	"solsynth.local/go-migration/internal/walletpg"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

var errAwardBeneficiary = errors.New("captured award beneficiary unavailable")
var errAwardTreasury = errors.New("captured award treasury unavailable")

func (r *Repository) releaseAward(ctx context.Context, id string) error {
	tx, e := r.begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	p, e := purchaseAward(ctx, tx, id, false)
	if e != nil {
		return e
	}
	s, e := observe(ctx, tx, "", p.Stream)
	if e != nil {
		return e
	}
	capturedRealmOwner := ""
	if p.BeneficiaryRealm != "" {
		if e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_realms WHERE id=$1::uuid AND deleted_at IS NULL`, p.BeneficiaryRealm).Scan(&capturedRealmOwner); e != nil {
			return fmt.Errorf("%w: %w", errAwardBeneficiary, e)
		}
	}
	// Current stream ancestry plus captured monetary beneficiary/treasury accounts
	// are locked before domain/order/wallet rows. No authority principal is minted.
	beneficiaryOwner := p.Owner
	if capturedRealmOwner != "" {
		beneficiaryOwner = capturedRealmOwner
	}
	if e = prelock(ctx, tx, "", p.Stream, s, true, beneficiaryOwner, p.TreasuryActor); e != nil {
		return e
	}
	if e = lockScope(ctx, tx, s, true); e != nil {
		return e
	}
	if p.BeneficiaryRealm != "" {
		var freshRealmOwner string
		if e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_realms WHERE id=$1::uuid AND deleted_at IS NULL FOR SHARE NOWAIT`, p.BeneficiaryRealm).Scan(&freshRealmOwner); e != nil || freshRealmOwner != capturedRealmOwner {
			if e == nil {
				e = live.ErrAwardRetry
			}
			return fmt.Errorf("%w: %w", errAwardBeneficiary, e)
		}
	}
	var held string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_livestreams WHERE id=$1::uuid FOR UPDATE NOWAIT`, p.Stream).Scan(&held); e != nil {
		return e
	}
	fresh, e := observe(ctx, tx, "", p.Stream)
	if e != nil {
		return e
	}
	if !same(s, fresh) {
		return live.ErrAwardRetry
	}
	p, e = purchaseAward(ctx, tx, id, true)
	if e != nil {
		return e
	}
	var state string
	if e = tx.QueryRowContext(ctx, `SELECT payout_state FROM owned_livestream_award_receipts WHERE purchase_id=$1::uuid FOR UPDATE NOWAIT`, id).Scan(&state); e != nil {
		return e
	}
	if state == "released" {
		return tx.Commit()
	}
	// The due-list read is only an observation. A refund request may commit
	// before this receipt lock is acquired; never distribute that receipt.
	if state != "held" {
		return tx.Commit()
	}
	if p.Fulfilled == nil {
		return live.ErrConflict
	}
	v, e := loadRun(ctx, tx, p.Stream, p.Generation)
	if e != nil {
		return e
	}
	if v.Phase != "ending" && v.Phase != "done" {
		return tx.Commit()
	}
	// A later stream ownership transfer cannot redirect an earned receipt. The
	// captured account must still be active and own the captured canonical wallet.
	if e = activeAccount(ctx, tx, beneficiaryOwner); e != nil {
		return fmt.Errorf("%w: %w", errAwardBeneficiary, e)
	}
	if e = activeAccount(ctx, tx, p.TreasuryActor); e != nil {
		return fmt.Errorf("%w: %w", errAwardTreasury, e)
	}
	if e = walletOwned(ctx, tx, p.TreasuryWallet, p.TreasuryActor); e != nil {
		return fmt.Errorf("%w: %w", errAwardTreasury, e)
	}
	if e = walletBound(ctx, tx, p.BeneficiaryWallet, p.BeneficiaryAccount, p.BeneficiaryRealm); e != nil {
		return fmt.Errorf("%w: %w", errAwardBeneficiary, e)
	}
	if e = walletpg.LockAwardWallets(ctx, tx, p.TreasuryWallet, p.BeneficiaryWallet); e != nil {
		return e
	}
	at, e := now(ctx, tx)
	if e != nil {
		return e
	}
	if e = r.awardCommerce.ReleaseLiveAwardShare(ctx, r.db, tx, id, at); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_livestream_award_receipts SET payout_state='released',released_at=$2::timestamptz,reason=NULL WHERE purchase_id=$1::uuid AND payout_state='held'`, id, at); e != nil {
		return e
	}
	if e = audit(ctx, tx, p.Stream, "", "livestreams.award.distributed"); e != nil {
		return e
	}
	return tx.Commit()
}

// ProcessAwardsDue releases only already-reserved, receipted funds after the
// recorded run ends. It neither calls an RTC provider nor starts new payments.
func (r *Repository) ProcessAwardsDue(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, live.ErrInvalid
	}
	if e := r.awardsConfigured(); e != nil {
		return 0, e
	}
	rows, e := r.db.QueryContext(ctx, `SELECT r.purchase_id::text,r.payout_state FROM owned_livestream_award_receipts r JOIN owned_livestream_award_purchases p ON p.id=r.purchase_id JOIN owned_livestream_runs run ON run.stream_id=p.stream_id AND run.generation=p.generation WHERE r.next_attempt_at<=clock_timestamp() AND (r.payout_state='held' AND run.phase IN('ending','done') OR r.payout_state IN('refund_pending','refund_ready')) AND p.treasury_account_id=$2::uuid AND p.treasury_wallet_id=$3::uuid AND p.currency=$4::text ORDER BY r.next_attempt_at,r.purchase_id LIMIT $1::integer`, limit, r.awards.TreasuryAccountID, r.awards.TreasuryWalletID, r.awards.PointsCurrency)
	if e != nil {
		return 0, e
	}
	type due struct{ id, state string }
	ids := []due{}
	for rows.Next() {
		var item due
		if e = rows.Scan(&item.id, &item.state); e != nil {
			rows.Close()
			return 0, e
		}
		ids = append(ids, item)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	n := 0
	var result error
	for _, item := range ids {
		if ctx.Err() != nil {
			return n, errors.Join(result, ctx.Err())
		}
		if item.state == "held" {
			e = r.releaseAward(ctx, item.id)
		} else {
			e = r.refundAward(ctx, item.id)
		}
		if e == nil {
			n++
			continue
		}
		result = errors.Join(result, awardMapped(e))
		reason := "release_pending"
		if errors.Is(e, errAwardBeneficiary) {
			reason = "beneficiary_unavailable"
		} else if errors.Is(e, errAwardTreasury) {
			reason = "treasury_unavailable"
		} else if errors.Is(e, errAwardRefundAuthority) {
			reason = "refund_authority_unavailable"
		} else if errors.Is(awardMapped(e), live.ErrAwardRetry) {
			reason = "concurrent_change"
		}
		_, x := r.db.ExecContext(ctx, `UPDATE owned_livestream_award_receipts SET attempts=LEAST(attempts+1,30),reason=$2::text,next_attempt_at=clock_timestamp()+LEAST(3600,1<<LEAST(attempts,12))*interval '1 second' WHERE purchase_id=$1::uuid AND payout_state IN('held','refund_pending','refund_ready')`, item.id, reason)
		result = errors.Join(result, x)
	}
	return n, result
}
