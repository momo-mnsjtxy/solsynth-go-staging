// SPDX-License-Identifier: AGPL-3.0-only
package affiliationpg

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	a "solsynth.local/go-migration/ports/passport/affiliation"
	"solsynth.local/go-migration/ports/passport/tickets"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"strings"
	"time"
)

//go:embed schema.sql
var Schema string

//go:embed paid_schema.sql
var PaidSchema string

// Activation joins an already locked account's transaction. It never performs
// network I/O or commits independently.
type Activation interface {
	ActivationInTransaction(context.Context, *sql.DB, *sql.Tx, string, bool) (tickets.ActivationState, error)
}
type Config struct {
	Now        func() time.Time
	Activation Activation
	Purchase   *PurchaseConfig
}
type PurchaseConfig struct {
	Currency, TreasuryAccountID, TreasuryWalletID string
	Price                                         wallet.Amount
	MaxUsages                                     int
	MaxLifetime                                   time.Duration
}
type Repository struct {
	db    *sql.DB
	guard *authoritypg.TransactionGuard
	cfg   Config
}

func New(db *sql.DB, g *authoritypg.TransactionGuard, c Config) (*Repository, error) {
	if db == nil || g == nil {
		return nil, a.ErrUnavailable
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Purchase != nil && (!authority.ValidID(c.Purchase.TreasuryAccountID) || !authority.ValidID(c.Purchase.TreasuryWalletID) || !wallet.ValidCurrency(c.Purchase.Currency) || c.Purchase.Price.Sign() <= 0 || c.Purchase.MaxUsages < 1 || c.Purchase.MaxUsages > 1000 || c.Purchase.MaxLifetime < time.Hour || c.Purchase.MaxLifetime > 365*24*time.Hour) {
		return nil, a.ErrInvalid
	}
	return &Repository{db, g, c}, nil
}
func (r *Repository) BindAuthority(ctx context.Context, p authority.Principal) context.Context {
	return r.guard.Bind(ctx, p)
}
func (r *Repository) Ready(ctx context.Context) error {
	q := `SELECT id,account_id,revoked_at FROM owned_affiliation_spells LIMIT 0;SELECT id,recipient_id FROM owned_affiliation_results LIMIT 0;SELECT actor_id,operation FROM owned_affiliation_audit LIMIT 0`
	if r.cfg.Purchase != nil {
		q += `;SELECT spell_id,transaction_id FROM owned_affiliation_purchases LIMIT 0`
	}
	_, e := r.db.ExecContext(ctx, q)
	return e
}
func (r *Repository) now() time.Time { return r.cfg.Now().UTC().Truncate(time.Microsecond) }
func id() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func word() (string, error) {
	var b [18]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	return strings.ToUpper(hex.EncodeToString(b[:])), nil
}
func mapped(e error) error {
	if errors.Is(e, sql.ErrNoRows) {
		return a.ErrNotFound
	}
	var p *pgconn.PgError
	if errors.As(e, &p) {
		switch p.Code {
		case "23505", "40P01", "55P03":
			return a.ErrConflict
		case "23514", "22001", "22P02":
			return a.ErrInvalid
		case "23503":
			return a.ErrNotFound
		}
	}
	return e
}

const columns = `s.id::text,s.account_id::text,s.spell,s.type,s.max_usages,s.skip_tests,s.created_at,s.updated_at,s.expires_at,s.affected_at,(SELECT count(*) FROM owned_affiliation_results r WHERE r.spell_id=s.id)`

func scan(row interface{ Scan(...any) error }) (v a.Spell, e error) {
	e = row.Scan(&v.ID, &v.AccountID, &v.Spell, &v.Type, &v.Meta.MaxUsages, &v.Meta.SkipTests, &v.CreatedAt, &v.UpdatedAt, &v.ExpiresAt, &v.AffectedAt, &v.UsageCount)
	return
}
func (r *Repository) check(ctx context.Context, tx *sql.Tx, actor, op string, pre bool) error {
	perm := []string{}
	if op == "invite.create" || op == "purchase" {
		perm = []string{a.InviteIssuePermission}
	} else if op == "create" || op == "delete" {
		perm = []string{"affiliations.manage"}
	}
	var p authority.Principal
	var e error
	if pre {
		p, e = r.guard.Preflight(ctx, r.db, tx, actor, perm...)
	} else if len(perm) > 0 {
		p, e = r.guard.Authorize(ctx, r.db, tx, actor, perm...)
	} else {
		p, e = r.guard.Authenticate(ctx, r.db, tx, actor)
	}
	if e == nil && (op == "consume" || op == "invite.create" || op == "purchase") {
		e = a.Root(p)
	}
	return e
}
func lockAccounts(ctx context.Context, tx *sql.Tx, ids ...string) error {
	rows, e := tx.QueryContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=ANY($1::uuid[]) ORDER BY id FOR UPDATE`, ids)
	if e != nil {
		return e
	}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
	}
	e = rows.Err()
	rows.Close()
	return e
}
func active(ctx context.Context, tx *sql.Tx, account string, now time.Time) error {
	var valid bool
	e := tx.QueryRowContext(ctx, `SELECT NOT disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=$2)AND(p.expires_at IS NULL OR p.expires_at>$2)) FROM owned_authority_accounts a WHERE id=$1`, account, now).Scan(&valid)
	if e != nil {
		return mapped(e)
	}
	if !valid {
		return authority.ErrForbidden
	}
	return nil
}
func (r *Repository) Execute(ctx context.Context, actor string, c a.Command) (out a.Result, err error) {
	if r == nil || r.db == nil {
		return out, a.ErrUnavailable
	}
	public := c.Operation == "record" && actor == ""
	if !public && !authority.ValidID(actor) {
		return out, authority.ErrInvalid
	}
	switch c.Operation {
	case "create", "invite.create", "delete", "list", "get", "results", "record", "consume", "purchase":
	default:
		return out, a.ErrInvalid
	}
	if (c.Operation == "record" || c.Operation == "consume") && !a.ValidWord(c.Word) {
		return out, a.ErrInvalid
	}
	if c.Operation == "record" && !a.ValidResource(c.Resource) {
		return out, a.ErrInvalid
	}
	if (c.Operation == "get" || c.Operation == "delete" || c.Operation == "results") && !authority.ValidID(c.ID) {
		return out, a.ErrInvalid
	}
	if c.Query.Take == 0 {
		c.Query.Take = 10
	}
	if c.Query.Take < 1 || c.Query.Take > 100 || c.Query.Offset < 0 || c.Query.Offset > 100000 {
		return out, a.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if !public {
		if e = r.check(ctx, tx, actor, c.Operation, true); e != nil {
			return out, e
		}
	}
	owner := actor
	if c.Operation == "record" || c.Operation == "consume" {
		if e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_affiliation_spells WHERE spell=$1`, c.Word).Scan(&owner); e != nil {
			if c.Operation == "consume" {
				return out, a.ErrInvite
			}
			return out, mapped(e)
		}
	}
	ids := []string{owner}
	if actor != "" {
		ids = append(ids, actor)
	}
	if e = lockAccounts(ctx, tx, ids...); e != nil {
		return out, e
	}
	if !public {
		if e = r.check(ctx, tx, actor, c.Operation, false); e != nil {
			return out, e
		}
	}
	if e = active(ctx, tx, owner, r.now()); e != nil {
		return out, e
	}
	switch c.Operation {
	case "purchase":
		out.Value, e = r.purchase(ctx, tx, actor, c)
	case "create", "invite.create":
		out.Value, e = r.create(ctx, tx, actor, c)
	case "delete":
		var sid string
		e = tx.QueryRowContext(ctx, `UPDATE owned_affiliation_spells SET revoked_at=$3,updated_at=$3 WHERE id=$1 AND account_id=$2 AND revoked_at IS NULL RETURNING id::text`, c.ID, actor, r.now()).Scan(&sid)
		if e == nil {
			e = r.audit(ctx, tx, actor, sid, "revoke")
		}
	case "get":
		out.Value, e = scan(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM owned_affiliation_spells s WHERE s.id=$1 AND s.account_id=$2 AND s.revoked_at IS NULL`, c.ID, actor))
	case "list":
		out, e = r.list(ctx, tx, actor, c.Query)
	case "results":
		out, e = r.results(ctx, tx, actor, c)
	case "record":
		out.Value, e = r.record(ctx, tx, c.Word, c.Resource)
	case "consume":
		if r.cfg.Activation == nil {
			return out, a.ErrUnavailable
		}
		var state tickets.ActivationState
		state, e = r.cfg.Activation.ActivationInTransaction(ctx, r.db, tx, actor, false)
		if e == nil {
			e = r.consume(ctx, tx, c.Word, actor, state.IsActivated)
		}
		if e == nil {
			out.Value, e = r.cfg.Activation.ActivationInTransaction(ctx, r.db, tx, actor, true)
		}
	}
	if e != nil {
		return a.Result{}, mapped(e)
	}
	// Re-evaluate time-dependent revocation after every domain wait.
	if !public {
		if e = r.check(ctx, tx, actor, c.Operation, false); e != nil {
			return a.Result{}, e
		}
	}
	if e = active(ctx, tx, owner, r.now()); e != nil {
		return a.Result{}, e
	}
	if e = tx.Commit(); e != nil {
		return a.Result{}, mapped(e)
	}
	return out, nil
}

func (r *Repository) purchase(ctx context.Context, tx *sql.Tx, actor string, c a.Command) (a.InvitePurchaseReceipt, error) {
	var out a.InvitePurchaseReceipt
	if r.cfg.Purchase == nil || c.Purchase == nil {
		return out, a.ErrPurchase
	}
	q, p := c.Purchase, r.cfg.Purchase
	if !authority.ValidID(q.PayerWalletID) || q.Key == "" || len(q.Key) > 128 || q.MaxUsages < 1 || q.MaxUsages > p.MaxUsages || !q.ExpiresAt.After(r.now()) || q.ExpiresAt.After(r.now().Add(p.MaxLifetime)) || q.Remark != nil && (len(*q.Remark) > 4096 || strings.ContainsRune(*q.Remark, 0)) {
		return out, a.ErrInvalid
	}
	remark := ""
	if q.Remark != nil {
		remark = *q.Remark
	}
	fingerprint := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%s", q.PayerWalletID, p.TreasuryWalletID, p.Currency, p.Price.String(), remark, q.MaxUsages, q.ExpiresAt.UTC().Format(time.RFC3339Nano))))
	var payerOwner, treasuryOwner string
	if e := tx.QueryRowContext(ctx, `SELECT account_id::text FROM wallet_v1.wallets WHERE id=$1::uuid AND realm_id IS NULL AND deleted_at IS NULL`, q.PayerWalletID).Scan(&payerOwner); e != nil {
		return out, mapped(e)
	}
	if e := tx.QueryRowContext(ctx, `SELECT account_id::text FROM wallet_v1.wallets WHERE id=$1::uuid AND realm_id IS NULL AND deleted_at IS NULL`, p.TreasuryWalletID).Scan(&treasuryOwner); e != nil {
		return out, mapped(e)
	}
	if payerOwner != actor || treasuryOwner != p.TreasuryAccountID {
		return out, authority.ErrForbidden
	}
	var existing string
	var storedHash []byte
	e := tx.QueryRowContext(ctx, `SELECT spell_id::text,request_hash FROM owned_affiliation_purchases WHERE actor_id=$1::uuid AND idempotency_key=$2::text`, actor, q.Key).Scan(&existing, &storedHash)
	if e == nil {
		if !bytes.Equal(storedHash, fingerprint[:]) {
			return out, a.ErrConflict
		}
		out.Transaction, e = scanPurchaseTransaction(tx.QueryRowContext(ctx, `SELECT t.id::text,t.payer_wallet_id::text,t.payee_wallet_id::text,t.currency,t.amount::text,t.remarks,t.status,t.created_at,t.updated_at,t.deleted_at FROM owned_affiliation_purchases p JOIN wallet_v1.transactions t ON t.id=p.transaction_id WHERE p.spell_id=$1::uuid AND t.actor_id=$2::uuid AND t.payer_wallet_id=$3::uuid AND t.payee_wallet_id=$4::uuid AND t.currency=$5::text AND t.amount=$6::numeric AND t.remarks IS NOT DISTINCT FROM $7::text AND t.status=2 AND t.deleted_at IS NULL AND t.idempotency_key=$8::text AND t.purpose='transfer'`, existing, actor, q.PayerWalletID, p.TreasuryWalletID, p.Currency, p.Price, q.Remark, "affiliation-purchase:"+q.Key))
		if e != nil {
			return out, a.ErrConflict
		}
		out.Spell, e = scan(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM owned_affiliation_spells s WHERE s.id=$1::uuid`, existing))
		return out, e
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return out, e
	}
	var at time.Time
	if e := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&at); e != nil {
		return out, e
	}
	out.Transaction, e = walletpg.FundedTransfer(ctx, tx, actor, q.PayerWalletID, p.TreasuryWalletID, p.Currency, p.Price, q.Remark, "affiliation-purchase:"+q.Key, at)
	if e != nil {
		return out, e
	}
	if e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM wallet_v1.wallets WHERE id=$1::uuid AND realm_id IS NULL AND deleted_at IS NULL`, q.PayerWalletID).Scan(&payerOwner); e != nil {
		return out, e
	}
	if e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM wallet_v1.wallets WHERE id=$1::uuid AND realm_id IS NULL AND deleted_at IS NULL`, p.TreasuryWalletID).Scan(&treasuryOwner); e != nil {
		return out, e
	}
	if payerOwner != actor || treasuryOwner != p.TreasuryAccountID {
		return out, authority.ErrForbidden
	}
	max := q.MaxUsages
	c.Create = &a.Create{MaxUsages: &max}
	c.ExpiresAt = &q.ExpiresAt
	c.Operation = "invite.create"
	out.Spell, e = r.create(ctx, tx, actor, c)
	if e != nil {
		return out, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_affiliation_purchases(spell_id,transaction_id,actor_id,idempotency_key,currency,amount,request_hash,created_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::text,$5::text,$6::numeric,$7::bytea,$8::timestamptz)`, out.Spell.ID, out.Transaction.ID, actor, q.Key, p.Currency, p.Price, fingerprint[:], at)
	return out, e
}

func scanPurchaseTransaction(row interface{ Scan(...any) error }) (v wallet.Transaction, e error) {
	e = row.Scan(&v.ID, &v.PayerWalletID, &v.PayeeWalletID, &v.Currency, &v.Amount, &v.Remarks, &v.Status, &v.CreatedAt, &v.UpdatedAt, &v.DeletedAt)
	return
}
func (r *Repository) audit(ctx context.Context, tx *sql.Tx, actor, sid, op string) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_affiliation_audit(actor_id,spell_id,operation,occurred_at)VALUES($1,$2,$3,$4)`, actor, sid, op, r.now())
	return e
}
func (r *Repository) create(ctx context.Context, tx *sql.Tx, actor string, c a.Command) (a.Spell, error) {
	var v a.Spell
	if c.Create == nil {
		return v, a.ErrInvalid
	}
	if c.Create.MaxUsages != nil && (*c.Create.MaxUsages < 1 || int64(*c.Create.MaxUsages) > 2147483647) {
		return v, a.ErrInvalid
	}
	v.AccountID = actor
	v.Meta.MaxUsages = c.Create.MaxUsages
	v.Meta.SkipTests = true
	if c.Create.SkipTests != nil {
		v.Meta.SkipTests = *c.Create.SkipTests
	}
	v.CreatedAt = r.now()
	v.UpdatedAt = v.CreatedAt
	v.ExpiresAt = c.ExpiresAt
	if c.Operation == "invite.create" {
		v.Type = a.RegistrationInvite
		if v.Meta.MaxUsages == nil || v.ExpiresAt == nil || !v.ExpiresAt.After(v.CreatedAt) || v.ExpiresAt.After(v.CreatedAt.Add(365*24*time.Hour)) {
			return v, a.ErrInvalid
		}
	}
	var e error
	v.ID, e = id()
	if e != nil {
		return v, e
	}
	if c.Create.Spell == nil {
		v.Spell, e = word()
	} else {
		v.Spell = *c.Create.Spell
	}
	if e != nil {
		return v, e
	}
	if !a.ValidWord(v.Spell) {
		return v, a.ErrInvalid
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_affiliation_spells(id,account_id,spell,type,max_usages,skip_tests,created_at,updated_at,expires_at)VALUES($1,$2,$3,$4,$5,$6,$7,$7,$8)`, v.ID, actor, v.Spell, v.Type, v.Meta.MaxUsages, v.Meta.SkipTests, v.CreatedAt, v.ExpiresAt)
	if e == nil {
		e = r.audit(ctx, tx, actor, v.ID, c.Operation)
	}
	return v, e
}
func (r *Repository) list(ctx context.Context, tx *sql.Tx, actor string, q a.Query) (out a.Result, e error) {
	rows, e := tx.QueryContext(ctx, `SELECT `+columns+`,count(*) OVER() FROM owned_affiliation_spells s WHERE s.account_id=$1 AND s.revoked_at IS NULL ORDER BY CASE WHEN $2='usage' AND NOT $3 THEN(SELECT count(*) FROM owned_affiliation_results x WHERE x.spell_id=s.id) END ASC,CASE WHEN $2='usage' AND $3 THEN(SELECT count(*) FROM owned_affiliation_results x WHERE x.spell_id=s.id) END DESC,CASE WHEN NOT $3 THEN s.created_at END ASC,CASE WHEN $3 THEN s.created_at END DESC,s.id LIMIT $4 OFFSET $5`, actor, q.Order, q.Desc, q.Take, q.Offset)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	v := []a.Spell{}
	var total int64
	for rows.Next() {
		var s a.Spell
		if e = rows.Scan(&s.ID, &s.AccountID, &s.Spell, &s.Type, &s.Meta.MaxUsages, &s.Meta.SkipTests, &s.CreatedAt, &s.UpdatedAt, &s.ExpiresAt, &s.AffectedAt, &s.UsageCount, &total); e != nil {
			return out, e
		}
		v = append(v, s)
	}
	if e = rows.Err(); e != nil {
		return out, e
	}
	rows.Close()
	if len(v) == 0 {
		e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_affiliation_spells WHERE account_id=$1 AND revoked_at IS NULL`, actor).Scan(&total)
	}
	return a.Result{Value: v, Total: &total}, e
}
func (r *Repository) results(ctx context.Context, tx *sql.Tx, actor string, c a.Command) (out a.Result, e error) {
	var exists bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_affiliation_spells WHERE id=$1 AND account_id=$2 AND revoked_at IS NULL)`, c.ID, actor).Scan(&exists); e != nil {
		return out, e
	}
	if !exists {
		return out, a.ErrNotFound
	}
	var total int64
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_affiliation_results WHERE spell_id=$1`, c.ID).Scan(&total); e != nil {
		return out, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT id::text,spell_id::text,resource_identifier,created_at FROM owned_affiliation_results WHERE spell_id=$1 ORDER BY CASE WHEN NOT $2 THEN created_at END ASC,CASE WHEN $2 THEN created_at END DESC,id LIMIT $3 OFFSET $4`, c.ID, c.Query.Desc, c.Query.Take, c.Query.Offset)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	v := []a.Event{}
	for rows.Next() {
		var x a.Event
		if e = rows.Scan(&x.ID, &x.SpellID, &x.ResourceIdentifier, &x.CreatedAt); e != nil {
			return out, e
		}
		v = append(v, x)
	}
	return a.Result{Value: v, Total: &total}, rows.Err()
}
func (r *Repository) record(ctx context.Context, tx *sql.Tx, word, resource string) (out a.Event, e error) {
	s, e := scan(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM owned_affiliation_spells s WHERE spell=$1 AND revoked_at IS NULL FOR UPDATE OF s`, word))
	if e != nil {
		return out, e
	}
	if s.Type != a.Tracking {
		return out, a.ErrRestricted
	}
	if s.ExpiresAt != nil && !s.ExpiresAt.After(r.now()) || s.Meta.MaxUsages != nil && s.UsageCount >= int64(*s.Meta.MaxUsages) {
		return out, a.ErrInvite
	}
	out.ID, e = id()
	if e != nil {
		return out, e
	}
	out.SpellID = s.ID
	out.ResourceIdentifier = resource
	out.CreatedAt = r.now()
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_affiliation_results(id,spell_id,resource_identifier,created_at)VALUES($1,$2,$3,$4)`, out.ID, s.ID, resource, out.CreatedAt)
	return out, e
}
func (r *Repository) consume(ctx context.Context, tx *sql.Tx, word, actor string, activated bool) error {
	s, e := scan(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM owned_affiliation_spells s WHERE spell=$1 AND type=1 AND revoked_at IS NULL FOR UPDATE OF s`, word))
	if errors.Is(e, sql.ErrNoRows) {
		return a.ErrInvite
	}
	if e != nil {
		return e
	}
	var prior string
	e = tx.QueryRowContext(ctx, `SELECT spell_id::text FROM owned_affiliation_results WHERE recipient_id=$1`, actor).Scan(&prior)
	if e == nil {
		if prior == s.ID {
			return nil
		}
		return a.ErrConflict
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if activated || s.ExpiresAt == nil || !s.ExpiresAt.After(r.now()) || s.Meta.MaxUsages == nil || s.UsageCount >= int64(*s.Meta.MaxUsages) {
		return a.ErrInvite
	}
	eid, e := id()
	if e != nil {
		return e
	}
	now := r.now()
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_affiliation_results(id,spell_id,resource_identifier,recipient_id,created_at)VALUES($1,$2,$3,$4,$5)`, eid, s.ID, "account:"+actor, actor, now); e != nil {
		return e
	}
	if *s.Meta.MaxUsages == 1 {
		if _, e = tx.ExecContext(ctx, `UPDATE owned_affiliation_spells SET affected_at=$2,updated_at=$2 WHERE id=$1`, s.ID, now); e != nil {
			return e
		}
	}
	return r.audit(ctx, tx, actor, s.ID, "consume")
}

// LockRegistrationAdmission acquires existing issuer authority before the
// enrollment transaction takes contact email locks. The new actor row has just
// been inserted and is not yet visible to other transactions. It grants nothing;
// AdmitRegistration must still revalidate and consume after enrollment writes.
func (r *Repository) LockRegistrationAdmission(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, word string) error {
	if r == nil || r.db != db || tx == nil {
		return a.ErrUnavailable
	}
	if !authority.ValidID(actor) || !a.ValidWord(word) {
		return a.ErrInvalid
	}
	var owner string
	if e := tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_affiliation_spells WHERE spell=$1`, word).Scan(&owner); e != nil {
		return a.ErrInvite
	}
	if e := lockAccounts(ctx, tx, owner); e != nil {
		return e
	}
	return active(ctx, tx, owner, r.now())
}

// AdmitRegistration is only called by the owned sign-in repository after it
// creates this new account, credentials and factor in the SAME transaction.
// A preexisting bearer or caller-selected account is not enrollment proof.
func (r *Repository) AdmitRegistration(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, word string) error {
	if r == nil || r.db != db || tx == nil {
		return a.ErrUnavailable
	}
	if !authority.ValidID(actor) || !a.ValidWord(word) {
		return a.ErrInvalid
	}
	var owner string
	if e := tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_affiliation_spells WHERE spell=$1`, word).Scan(&owner); e != nil {
		return a.ErrInvite
	}
	if e := lockAccounts(ctx, tx, owner); e != nil {
		return e
	}
	if e := active(ctx, tx, owner, r.now()); e != nil {
		return e
	}
	// Tracking codes remain tracking and cannot bypass admission tests.
	var typ int
	if e := tx.QueryRowContext(ctx, `SELECT type FROM owned_affiliation_spells WHERE spell=$1`, word).Scan(&typ); e != nil {
		return mapped(e)
	}
	if typ == a.Tracking {
		_, e := r.record(ctx, tx, word, "account:"+actor)
		return mapped(e)
	}
	if r.cfg.Activation == nil {
		return a.ErrUnavailable
	}
	if e := r.consume(ctx, tx, word, actor, false); e != nil {
		return mapped(e)
	}
	_, e := r.cfg.Activation.ActivationInTransaction(ctx, db, tx, actor, true)
	if e != nil {
		return e
	}
	return active(ctx, tx, owner, r.now())
}

// TestsBypassed serializes with spell revocation. Admission expiry is checked
// when consumed; it does not retroactively revoke an already earned receipt.
func (r *Repository) TestsBypassed(ctx context.Context, tx *sql.Tx, actor string) (bool, error) {
	var skip bool
	e := tx.QueryRowContext(ctx, `SELECT s.skip_tests FROM owned_affiliation_results r JOIN owned_affiliation_spells s ON s.id=r.spell_id WHERE r.recipient_id=$1 AND s.type=1 AND s.revoked_at IS NULL FOR SHARE OF s`, actor).Scan(&skip)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	return skip, e
}

var _ a.Repository = (*Repository)(nil)
