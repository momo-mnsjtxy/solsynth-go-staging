// SPDX-License-Identifier: AGPL-3.0-only
package livestreampg

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

const awardRefundPermission = "livestreams.awards.refund"

var errAwardRefundAuthority = errors.New("award refund authority unavailable")

func (r *Repository) RequestAwardRefund(ctx context.Context, actor, sid, purchase string, q live.AwardRefundRequest) (out live.AwardRefund, e error) {
	if e = r.awardsConfigured(); e != nil {
		return
	}
	q.Reason = strings.TrimSpace(q.Reason)
	if q.Reason == "" || len(q.Reason) > 256 || strings.ContainsRune(q.Reason, 0) {
		return out, live.ErrInvalid
	}
	tx, principal, _, e := r.locked(ctx, actor, sid, "", awardRefundPermission, false, true, r.awards.TreasuryAccountID)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if !principal.Account.Superuser {
		return out, live.ErrForbidden
	}
	p, e := purchaseAward(ctx, tx, purchase, true)
	if e != nil || p.Stream != sid || p.Fulfilled == nil {
		if e != nil {
			return out, e
		}
		return out, live.ErrNotFound
	}
	var state, reason string
	var requested, refunded *time.Time
	e = tx.QueryRowContext(ctx, `SELECT payout_state,COALESCE(refund_reason,''),refund_requested_at,refunded_at FROM owned_livestream_award_receipts WHERE purchase_id=$1::uuid FOR UPDATE`, purchase).Scan(&state, &reason, &requested, &refunded)
	if e != nil {
		return out, e
	}
	if state == "released" {
		return out, live.ErrConflict
	}
	if state == "refunded" {
		return live.AwardRefund{PurchaseID: purchase, State: state, Reason: reason, RequestedAt: *requested, RefundedAt: refunded}, r.commit(ctx, tx, actor, awardRefundPermission)
	}
	at, e := now(ctx, tx)
	if e != nil {
		return out, e
	}
	if requested == nil {
		requested = &at
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_livestream_award_receipts SET payout_state=CASE WHEN payout_state='held' THEN 'refund_pending' ELSE payout_state END,refund_requested_by=$2::uuid,refund_session_id=$3::uuid,refund_account_version=$4::bigint,refund_session_epoch=$5::bigint,refund_reason=$6::text,refund_requested_at=COALESCE(refund_requested_at,$7::timestamptz),reason=NULL,next_attempt_at=$7::timestamptz WHERE purchase_id=$1::uuid AND payout_state IN('held','refund_pending','refund_ready')`, purchase, actor, principal.Session.ID, principal.Account.Version, principal.Session.AuthorizationEpoch, q.Reason, at)
	if e != nil {
		return out, e
	}
	if e = audit(ctx, tx, sid, actor, "livestreams.award.refund.requested"); e != nil {
		return out, e
	}
	out = live.AwardRefund{PurchaseID: purchase, State: state, Reason: q.Reason, RequestedAt: *requested}
	if state == "held" {
		out.State = "refund_pending"
	}
	return out, r.commit(ctx, tx, actor, awardRefundPermission)
}

func lockAwardRefundAccounts(ctx context.Context, tx *sql.Tx, ids ...string) error {
	sort.Strings(ids)
	previous := ""
	for _, id := range ids {
		if id == "" || id == previous {
			continue
		}
		previous = id
		var held string
		if e := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1::uuid FOR SHARE NOWAIT`, id).Scan(&held); e != nil {
			return e
		}
	}
	return nil
}

func (r *Repository) refundAward(ctx context.Context, id string) error {
	var purchaseActor, treasuryActor, requester string
	if e := r.db.QueryRowContext(ctx, `SELECT p.account_id::text,p.treasury_account_id::text,COALESCE(r.refund_requested_by::text,'') FROM owned_livestream_award_purchases p JOIN owned_livestream_award_receipts r ON r.purchase_id=p.id WHERE p.id=$1::uuid`, id).Scan(&purchaseActor, &treasuryActor, &requester); e != nil {
		return e
	}
	tx, e := r.begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = lockAwardRefundAccounts(ctx, tx, purchaseActor, treasuryActor, requester); e != nil {
		return e
	}
	p, e := purchaseAward(ctx, tx, id, true)
	if e != nil {
		return e
	}
	var state, currentRequester, session string
	var version, epoch int64
	var requested time.Time
	e = tx.QueryRowContext(ctx, `SELECT payout_state,COALESCE(refund_requested_by::text,''),COALESCE(refund_session_id::text,''),COALESCE(refund_account_version,0),COALESCE(refund_session_epoch,0),refund_requested_at FROM owned_livestream_award_receipts WHERE purchase_id=$1::uuid FOR UPDATE NOWAIT`, id).Scan(&state, &currentRequester, &session, &version, &epoch, &requested)
	if e != nil {
		return e
	}
	if state == "refunded" {
		return tx.Commit()
	}
	if state != "refund_pending" && state != "refund_ready" || requester == "" || currentRequester != requester || session == "" || requested.IsZero() {
		return live.ErrConflict
	}
	eligible, e := r.eligible(ctx, tx, requester, session, version, epoch, awardRefundPermission)
	if e != nil || !eligible {
		if e != nil {
			return e
		}
		return errAwardRefundAuthority
	}
	var super bool
	if e = tx.QueryRowContext(ctx, `SELECT is_superuser FROM owned_authority_accounts WHERE id=$1::uuid`, requester).Scan(&super); e != nil || !super {
		if e != nil {
			return e
		}
		return errAwardRefundAuthority
	}
	at, e := now(ctx, tx)
	if e != nil {
		return e
	}
	if state == "refund_pending" {
		if e = r.awardCommerce.ReleaseLiveAwardRefundReserve(ctx, r.db, tx, id, at); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_livestream_award_receipts SET payout_state='refund_ready',reason=NULL,next_attempt_at=$2::timestamptz WHERE purchase_id=$1::uuid AND payout_state='refund_pending'`, id, at); e != nil {
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
		return r.refundAward(ctx, id)
	}
	transaction, e := r.awardCommerce.ReverseLiveAwardPayment(ctx, r.db, tx, id, at)
	if e != nil {
		return e
	}
	result, e := tx.ExecContext(ctx, `UPDATE owned_livestream_award_receipts SET payout_state='refunded',refund_transaction_id=$2::uuid,refunded_at=$3::timestamptz,reason=NULL WHERE purchase_id=$1::uuid AND payout_state='refund_ready'`, id, transaction, at)
	if e != nil {
		return e
	}
	if n, e := result.RowsAffected(); e != nil || n != 1 {
		if e != nil {
			return e
		}
		return live.ErrConflict
	}
	if e = audit(ctx, tx, p.Stream, requester, "livestreams.award.refunded"); e != nil {
		return e
	}
	return tx.Commit()
}
