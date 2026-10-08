// SPDX-License-Identifier: AGPL-3.0-only
package livestreampg

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	commerce "solsynth.local/go-migration/ports/wallet/commerce"
	"strings"
	"time"
)

//go:embed awards_schema.sql
var AwardsSchema string

type AwardConfig struct {
	PointsCurrency, TreasuryAccountID, TreasuryWalletID string
	MaxAmount                                           wallet.Amount
	MaxOrdersPerAccountStream, MaxAwardsPerStream       int
	OrderTTL                                            time.Duration
}
type AwardCommerce interface {
	LiveAwardRequest(context.Context, *sql.DB, *sql.Tx, string, string, string, string) (string, error)
	CreateLiveAwardOrder(context.Context, *sql.DB, *sql.Tx, string, string, string, string, string, wallet.Amount, time.Time, time.Time) (commerce.Order, error)
	ReserveLiveAwardShare(context.Context, *sql.DB, *sql.Tx, string, string, string, string, commerce.Order, wallet.Amount, time.Time) (commerce.Order, error)
	ReleaseLiveAwardShare(context.Context, *sql.DB, *sql.Tx, string, time.Time) error
}

func (r *Repository) WithAwards(c AwardConfig, store AwardCommerce) error {
	maximum, _ := wallet.ParseAmount("43200")
	if r == nil || store == nil || !authority.ValidID(c.TreasuryAccountID) || !authority.ValidID(c.TreasuryWalletID) || !live.Text(c.PointsCurrency, 128) || strings.TrimSpace(c.PointsCurrency) == "" || strings.TrimSpace(c.PointsCurrency) != c.PointsCurrency || c.MaxAmount.Sign() <= 0 || c.MaxAmount.Cmp(maximum) > 0 || c.MaxOrdersPerAccountStream < 1 || c.MaxOrdersPerAccountStream > 1000 || c.MaxAwardsPerStream < 1 || c.MaxAwardsPerStream > 100000 || c.OrderTTL < time.Minute || c.OrderTTL > 24*time.Hour {
		return live.ErrInvalid
	}
	if _, e := live.ParseAwardPoints(c.MaxAmount.String()); e != nil {
		return e
	}
	if _, ok := r.publishers.(awardAccounts); !ok {
		return live.ErrUnavailable
	}
	r.awards = &c
	r.awardCommerce = store
	return nil
}
func (r *Repository) awardsConfigured() error {
	if r == nil || r.awards == nil || r.awardCommerce == nil {
		return live.ErrAwardUnavailable
	}
	return nil
}
func (r *Repository) AwardsReady(ctx context.Context) error {
	if e := r.awardsConfigured(); e != nil {
		return e
	}
	_, e := r.db.ExecContext(ctx, `SELECT id,order_id,fulfilled_at FROM owned_livestream_award_purchases LIMIT 0;SELECT purchase_id,reserve_transaction_id,payout_state FROM owned_livestream_award_receipts LIMIT 0;SELECT managed_by,status FROM wallet_v1.transactions LIMIT 0;SELECT id,status FROM commerce_v1.orders LIMIT 0`)
	if e != nil {
		return e
	}
	var ready bool
	e = r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_index WHERE indexrelid=to_regclass('commerce_v1.owned_livestream_award_distribution_source') AND indisunique AND indisvalid) AND EXISTS(SELECT 1 FROM wallet_v1.wallets w JOIN owned_authority_accounts a ON a.id=w.account_id WHERE w.id=$1::uuid AND w.account_id=$2::uuid AND w.realm_id IS NULL AND w.deleted_at IS NULL AND NOT a.disabled)`, r.awards.TreasuryWalletID, r.awards.TreasuryAccountID).Scan(&ready)
	if e != nil {
		return e
	}
	if !ready {
		return live.ErrAwardUnavailable
	}
	return nil
}
func awardMapped(e error) error {
	var pg *pgconn.PgError
	if errors.As(e, &pg) && pg.Code == "55P03" || errors.Is(e, commerce.ErrRetry) {
		return live.ErrAwardRetry
	}
	return e
}

type awardPurchase struct {
	ID, Order, Stream, Actor, Owner, TreasuryActor, TreasuryWallet, BeneficiaryWallet, Currency string
	Generation, OwnerVersion                                                                    int64
	Amount, Share                                                                               wallet.Amount
	Message, Sender                                                                             *string
	Created                                                                                     time.Time
	Fulfilled                                                                                   *time.Time
}

const awardPurchaseFields = `id::text,order_id::text,stream_id::text,generation,account_id::text,owner_id::text,owner_version,treasury_account_id::text,treasury_wallet_id::text,beneficiary_wallet_id::text,currency,amount::text,share_amount::text,message,sender_name,created_at,fulfilled_at`

func purchaseAward(ctx context.Context, tx *sql.Tx, id string, lock bool) (p awardPurchase, e error) {
	q := `SELECT ` + awardPurchaseFields + ` FROM owned_livestream_award_purchases WHERE id=$1::uuid`
	if lock {
		q += ` FOR UPDATE NOWAIT`
	}
	e = tx.QueryRowContext(ctx, q, id).Scan(&p.ID, &p.Order, &p.Stream, &p.Generation, &p.Actor, &p.Owner, &p.OwnerVersion, &p.TreasuryActor, &p.TreasuryWallet, &p.BeneficiaryWallet, &p.Currency, &p.Amount, &p.Share, &p.Message, &p.Sender, &p.Created, &p.Fulfilled)
	if errors.Is(e, sql.ErrNoRows) {
		e = live.ErrNotFound
	}
	return
}
func (r *Repository) awardOwner(ctx context.Context, tx *sql.Tx, v live.Stream) (owner string, version int64, wid string, e error) {
	// The authored payout service supports individual publisher accounts only.
	e = tx.QueryRowContext(ctx, `SELECT p.account_id::text,a.version FROM owned_publishers p JOIN owned_authority_accounts a ON a.id=p.account_id WHERE p.id=$1::uuid AND p.type=0 AND p.deleted_at IS NULL AND NOT a.disabled`, v.PublisherID).Scan(&owner, &version)
	if errors.Is(e, sql.ErrNoRows) {
		return "", 0, "", live.ErrAwardUnavailable
	}
	if e != nil {
		return
	}
	e = tx.QueryRowContext(ctx, `SELECT id::text FROM wallet_v1.wallets WHERE account_id=$1::uuid AND realm_id IS NULL AND deleted_at IS NULL ORDER BY is_primary DESC,created_at,id LIMIT 1`, owner).Scan(&wid)
	if errors.Is(e, sql.ErrNoRows) {
		e = live.ErrAwardUnavailable
	}
	return
}
func walletOwned(ctx context.Context, tx *sql.Tx, wid, actor string) error {
	var yes bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wallet_v1.wallets WHERE id=$1::uuid AND account_id=$2::uuid AND realm_id IS NULL AND deleted_at IS NULL)`, wid, actor).Scan(&yes)
	if e != nil {
		return e
	}
	if !yes {
		return live.ErrAwardUnavailable
	}
	return nil
}
func (r *Repository) CreateAwardOrder(ctx context.Context, actor, sid, key string, q live.AwardRequest) (out live.AwardOrderResponse, e error) {
	defer func() { e = awardMapped(e) }()
	if e = r.awardsConfigured(); e != nil {
		return
	}
	if e = r.configured(); e != nil {
		return out, e
	}
	if e = chatKey(key); e != nil {
		return
	}
	if q.Amount.Amount().Sign() <= 0 || q.Amount.Amount().Cmp(r.awards.MaxAmount) > 0 || q.Message != nil && !live.Text(*q.Message, 4096) {
		return out, live.ErrInvalid
	}
	tx, _, v, e := r.locked(ctx, actor, sid, "", "livestreams.awards", false, false, r.awards.TreasuryAccountID)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if _, e = r.visible(ctx, tx, actor, &v); e != nil {
		return out, e
	}
	if v.Status != live.Active || v.OperationState != "active" {
		return out, live.ErrConflict
	}
	current, e := loadRun(ctx, tx, sid, v.Generation)
	if e != nil {
		return out, e
	}
	if e = r.providerMatches(current); e != nil {
		return out, e
	}
	alive, e := r.runAlive(ctx, tx, current)
	if e != nil {
		return out, e
	}
	if !alive {
		return out, live.ErrForbidden
	}
	owner, version, beneficiary, e := r.awardOwner(ctx, tx, v)
	if e != nil {
		return out, e
	}
	if beneficiary == r.awards.TreasuryWalletID {
		return out, live.ErrAwardUnavailable
	}
	if e = activeAccount(ctx, tx, r.awards.TreasuryAccountID); e != nil {
		return out, e
	}
	if e = walletOwned(ctx, tx, r.awards.TreasuryWalletID, r.awards.TreasuryAccountID); e != nil {
		return out, e
	}
	var drift bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_livestream_award_purchases WHERE stream_id=$1::uuid AND(currency<>$2::text OR treasury_wallet_id<>$3::uuid OR treasury_account_id<>$4::uuid))`, sid, r.awards.PointsCurrency, r.awards.TreasuryWalletID, r.awards.TreasuryAccountID).Scan(&drift); e != nil {
		return out, e
	}
	if drift {
		return out, live.ErrAwardUnavailable
	}
	fp := hex.EncodeToString(hash(struct {
		Stream                            string
		Generation                        int64
		Owner, Wallet, Currency, Treasury string
		Request                           live.AwardRequest
	}{sid, v.Generation, owner, beneficiary, r.awards.PointsCurrency, r.awards.TreasuryWalletID, q}))
	prior, e := r.awardCommerce.LiveAwardRequest(ctx, r.db, tx, actor, key, fp, "")
	if e != nil {
		return out, e
	}
	if prior != "" {
		p, x := purchaseAward(ctx, tx, prior, false)
		if x != nil {
			return out, x
		}
		if p.Actor != actor || p.Stream != sid {
			return out, live.ErrConflict
		}
		return live.AwardOrderResponse{OrderID: p.Order}, r.commit(ctx, tx, actor, "livestreams.awards")
	}
	var own, total int
	if e = tx.QueryRowContext(ctx, `SELECT count(*)FILTER(WHERE account_id=$2::uuid),count(*) FROM owned_livestream_award_purchases WHERE stream_id=$1::uuid`, sid, actor).Scan(&own, &total); e != nil {
		return out, e
	}
	if own >= r.awards.MaxOrdersPerAccountStream || total >= r.awards.MaxAwardsPerStream {
		return out, live.ErrConflict
	}
	id, e := uid()
	if e != nil {
		return out, e
	}
	at, e := now(ctx, tx)
	if e != nil {
		return out, e
	}
	expiry := at.Add(r.awards.OrderTTL)
	if expiry.After(current.ExpiresAt) {
		expiry = current.ExpiresAt
	}
	share, e := q.Amount.Share()
	if e != nil {
		return out, e
	}
	o, e := r.awardCommerce.CreateLiveAwardOrder(ctx, r.db, tx, actor, id, sid, r.awards.PointsCurrency, r.awards.TreasuryWalletID, q.Amount.Amount(), expiry, at)
	if e != nil {
		return out, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_livestream_award_purchases(id,order_id,stream_id,generation,account_id,owner_id,owner_version,beneficiary_wallet_id,treasury_account_id,treasury_wallet_id,currency,amount,share_amount,message,created_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::bigint,$5::uuid,$6::uuid,$7::bigint,$8::uuid,$9::uuid,$10::uuid,$11::text,$12::numeric,$13::numeric,$14::text,$15::timestamptz)`, id, o.ID, sid, v.Generation, actor, owner, version, beneficiary, r.awards.TreasuryAccountID, r.awards.TreasuryWalletID, r.awards.PointsCurrency, q.Amount.Amount(), share, q.Message, at)
	if e != nil {
		return out, e
	}
	if _, e = r.awardCommerce.LiveAwardRequest(ctx, r.db, tx, actor, key, fp, id); e != nil {
		return out, e
	}
	if e = audit(ctx, tx, sid, actor, "livestreams.award.order"); e != nil {
		return out, e
	}
	return live.AwardOrderResponse{OrderID: o.ID}, r.commit(ctx, tx, actor, "livestreams.awards")
}

// PayOrder already holds the original actor account/order. Every additional
// account/domain lock is NOWAIT; contention rolls back the entire payment.
func (r *Repository) lockedAwardPayment(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, o commerce.Order) (p awardPurchase, stream live.Stream, e error) {
	if e = r.awardsConfigured(); e != nil {
		return
	}
	if r.db != db || db == nil || tx == nil || !authority.ValidID(actor) || o.AppIdentifier != "internal" || o.ProductIdentifier == nil || *o.ProductIdentifier != live.AwardProduct {
		return p, stream, live.ErrAwardUnavailable
	}
	if _, e = r.guard.Preflight(ctx, r.db, tx, actor, "orders.pay", "livestreams.awards"); e != nil {
		return
	}
	var id string
	if json.Unmarshal(o.Meta["purchase_id"], &id) != nil || !authority.ValidID(id) {
		return p, stream, live.ErrInvalid
	}
	p, e = purchaseAward(ctx, tx, id, false)
	if e != nil {
		return
	}
	if p.Actor != actor || p.Order != o.ID || p.Currency != o.Currency || p.Amount.Cmp(o.Amount) != 0 || p.TreasuryWallet != o.PayeeWalletID || p.TreasuryWallet != r.awards.TreasuryWalletID || p.TreasuryActor != r.awards.TreasuryAccountID || p.Currency != r.awards.PointsCurrency {
		return p, stream, live.ErrConflict
	}
	observed, e := observe(ctx, tx, "", p.Stream)
	if e != nil {
		return p, stream, e
	}
	if e = prelock(ctx, tx, actor, p.Stream, observed, true, p.Owner, p.TreasuryActor); e != nil {
		return p, stream, e
	}
	principal, e := r.guard.Authorize(ctx, r.db, tx, actor, "orders.pay", "livestreams.awards")
	if e != nil {
		return p, stream, e
	}
	if principal.Session.Type != 0 || principal.Session.ParentID != "" {
		return p, stream, authority.ErrInvalid
	}
	if e = lockScope(ctx, tx, observed, true); e != nil {
		return p, stream, e
	}
	var held string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_livestreams WHERE id=$1::uuid FOR UPDATE NOWAIT`, p.Stream).Scan(&held); e != nil {
		return p, stream, e
	}
	fresh, e := observe(ctx, tx, "", p.Stream)
	if e != nil {
		return p, stream, e
	}
	if !same(observed, fresh) {
		return p, stream, live.ErrAwardRetry
	}
	p, e = purchaseAward(ctx, tx, p.ID, true)
	if e != nil {
		return
	}
	stream, e = get(ctx, tx, p.Stream, false, false)
	if e != nil {
		return
	}
	if stream.Generation != p.Generation || stream.Status != live.Active || stream.OperationState != "active" {
		return p, stream, live.ErrConflict
	}
	if _, e = r.visible(ctx, tx, actor, &stream); e != nil {
		return
	}
	owner, version, beneficiary, e := r.awardOwner(ctx, tx, stream)
	if e != nil {
		return p, stream, e
	}
	if owner != p.Owner || version != p.OwnerVersion || beneficiary != p.BeneficiaryWallet {
		return p, stream, live.ErrConflict
	}
	if e = activeAccount(ctx, tx, p.TreasuryActor); e != nil {
		return
	}
	if e = walletOwned(ctx, tx, p.TreasuryWallet, p.TreasuryActor); e != nil {
		return
	}
	if e = walletOwned(ctx, tx, p.BeneficiaryWallet, p.Owner); e != nil {
		return
	}
	v, e := loadRun(ctx, tx, p.Stream, p.Generation)
	if e != nil {
		return p, stream, e
	}
	if e = r.providerMatches(v); e != nil {
		return p, stream, e
	}
	alive, e := r.runAlive(ctx, tx, v)
	if e != nil {
		return p, stream, e
	}
	if !alive {
		return p, stream, live.ErrForbidden
	}
	currentTime, err := now(ctx, tx)
	if err != nil {
		return p, stream, err
	}
	if !v.ExpiresAt.After(currentTime) {
		return p, stream, live.ErrConflict
	}
	return p, stream, nil
}
func (r *Repository) PrepareLiveStreamAward(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, o commerce.Order, payer string, at time.Time) (e error) {
	defer func() { e = awardMapped(e) }()
	p, _, e := r.lockedAwardPayment(ctx, db, tx, actor, o)
	if e != nil {
		return e
	}
	if p.Fulfilled != nil {
		return live.ErrConflict
	}
	if e = walletOwned(ctx, tx, payer, actor); e != nil {
		return e
	}
	return walletpg.LockAwardWallets(ctx, tx, payer, p.TreasuryWallet, p.BeneficiaryWallet)
}
func (r *Repository) FulfillLiveStreamAward(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, o commerce.Order, at time.Time) (e error) {
	defer func() { e = awardMapped(e) }()
	p, _, e := r.lockedAwardPayment(ctx, db, tx, actor, o)
	if e != nil {
		return e
	}
	if o.TransactionID == nil {
		return live.ErrInvalid
	}
	if p.Fulfilled != nil {
		return live.ErrConflict
	}
	var valid bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM commerce_v1.orders o JOIN wallet_v1.transactions t ON t.id=$2::uuid WHERE o.id=$1::uuid AND o.actor_id=$3::uuid AND o.app_id IS NULL AND o.product_identifier=$4::text AND o.subscription_id IS NULL AND o.gift_id IS NULL AND t.managed_by=o.id AND t.purpose='commerce' AND t.actor_id=o.actor_id AND t.amount=o.amount AND t.currency=o.currency AND t.payee_wallet_id=o.payee_wallet_id AND t.status=2 AND t.confirmed_at IS NOT NULL AND t.deleted_at IS NULL)`, o.ID, *o.TransactionID, actor, live.AwardProduct).Scan(&valid)
	if e != nil {
		return e
	}
	if !valid {
		return live.ErrForbidden
	}
	distribution, e := r.awardCommerce.ReserveLiveAwardShare(ctx, db, tx, actor, p.ID, p.TreasuryActor, p.BeneficiaryWallet, o, p.Share, at)
	if e != nil {
		return e
	}
	if distribution.TransactionID == nil {
		return live.ErrUnavailable
	}
	who, e := r.publishers.(live.ChatAccountProvider).AccountInTransaction(ctx, tx, actor, actor)
	if e != nil {
		return e
	}
	name := who.Nick
	if name == "" {
		name = who.Name
	}
	if name == "" {
		name = "Anonymous"
	}
	runes := []rune(name)
	if len(runes) > 128 {
		name = string(runes[:128])
	}
	at, e = now(ctx, tx)
	if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_livestream_award_receipts(purchase_id,order_id,transaction_id,distribution_order_id,reserve_transaction_id,fulfilled_at,next_attempt_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6::timestamptz,$6::timestamptz)`, p.ID, o.ID, *o.TransactionID, distribution.ID, *distribution.TransactionID, at); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_livestream_award_purchases SET fulfilled_at=$2::timestamptz,sender_name=$3::text WHERE id=$1::uuid`, p.ID, at, name); e != nil {
		return e
	}
	if e = audit(ctx, tx, p.Stream, actor, "livestreams.award.fulfilled"); e != nil {
		return e
	}
	_, e = r.guard.Authorize(ctx, db, tx, actor, "orders.pay", "livestreams.awards")
	return e
}
