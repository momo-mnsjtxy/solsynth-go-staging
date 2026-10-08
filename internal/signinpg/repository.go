// SPDX-License-Identifier: AGPL-3.0-only
package signinpg

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
	"strings"
	"time"
)

// Schema is an explicit migration input; constructors never install it.
//
//go:embed schema.sql
var baseSchema string

//go:embed payment_pin_schema.sql
var PaymentPINSchema string

//go:embed factor_setup_schema.sql
var FactorSetupSchema string
var Schema = baseSchema + "\n" + PaymentPINSchema + "\n" + FactorSetupSchema

type Repository struct {
	admission  RegistrationAdmission
	activation ContactActivation
	pin        PaymentPINStore
	db         *sql.DB
	authority  *authoritypg.Repository
	guard      *authoritypg.TransactionGuard
}

func New(db *sql.DB, canonical *authoritypg.Repository, guard *authoritypg.TransactionGuard) *Repository {
	return &Repository{db: db, authority: canonical, guard: guard}
}

var _ signin.Store = (*Repository)(nil)

func (r *Repository) Bind(ctx context.Context, p authority.Principal) context.Context {
	if r.guard == nil {
		return ctx
	}
	return r.guard.Bind(ctx, p)
}
func conflict(e error) error {
	var p *pgconn.PgError
	if errors.As(e, &p) && p.Code == "23505" {
		return signin.ErrConflict
	}
	return e
}
func (r *Repository) Register(ctx context.Context, a authority.Account, c signin.Credentials, f signin.Factor, ch signin.Challenge, permissions []string) error {
	return r.register(ctx, a, c, f, ch, permissions, "")
}
func (r *Repository) RegisterWithAffiliation(ctx context.Context, a authority.Account, c signin.Credentials, f signin.Factor, ch signin.Challenge, permissions []string, word string) error {
	if r.admission == nil || word == "" {
		return signin.ErrInput
	}
	return r.register(ctx, a, c, f, ch, permissions, word)
}
func (r *Repository) register(ctx context.Context, a authority.Account, c signin.Credentials, f signin.Factor, ch signin.Challenge, permissions []string, word string) error {
	if r == nil || r.db == nil || r.authority == nil || a.ID != c.AccountID || a.ID != ch.AccountID || !authority.ValidID(a.ID) {
		return authority.ErrInvalid
	}
	if len(permissions) > 100 {
		return authority.ErrInvalid
	}
	for _, key := range permissions {
		if !authority.ValidPermissionPattern(key) || strings.Contains(key, "*") {
			return authority.ErrInvalid
		}
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_authority_accounts(id,name,nick,language,created_at) VALUES($1,$2,$3,$4,$5)`, a.ID, a.Name, a.Nick, a.Language, a.CreatedAt); e != nil {
		return conflict(e)
	}
	// The newly inserted account cannot be held by another transaction. Lock
	// any existing invite issuer before email advisory locks, matching ordinary
	// account/contact writers and avoiding owner-account <-> email inversion.
	if word != "" {
		if e = r.admission.LockRegistrationAdmission(ctx, r.db, tx, a.ID, word); e != nil {
			return e
		}
	}
	if e = lockEmails(ctx, tx, c.Email); e != nil {
		return e
	}
	if used, err := emailOwned(ctx, tx, c.Email, ""); err != nil {
		return err
	} else if used {
		return signin.ErrConflict
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_authority_credentials(account_id,email) VALUES($1,$2)`, a.ID, c.Email); e != nil {
		return conflict(e)
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_authority_contacts(id,account_id,type,credential_account_id,created_at,updated_at)VALUES($1::uuid,$1::uuid,0,$1::uuid,$2::timestamptz,$2::timestamptz)`, a.ID, a.CreatedAt); e != nil {
		return conflict(e)
	}
	at, e := r.authority.JoinTransaction(ctx, r.db, tx, a.ID)
	if e != nil {
		return e
	}
	t := &transaction{repo: r, tx: tx, ctx: ctx, canonical: at}
	if e = t.SaveFactor(f); e != nil {
		return e
	}
	if e = t.SaveChallenge(ch); e != nil {
		return e
	}
	for _, key := range permissions {
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_authority_grants(account_id,pattern) VALUES($1,$2)`, a.ID, key); e != nil {
			return e
		}
	}
	if word != "" {
		if e = r.admission.AdmitRegistration(ctx, r.db, tx, a.ID, word); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (r *Repository) Lookup(ctx context.Context, ref string) (string, error) {
	var id string
	if r == nil || r.db == nil {
		return id, authority.ErrUnavailable
	}
	e := r.db.QueryRowContext(ctx, `SELECT a.id::text FROM owned_authority_accounts a JOIN owned_authority_credentials c ON c.account_id=a.id WHERE lower(a.name)=lower($1::text) OR c.email=$2::text`, ref, strings.ToLower(ref)).Scan(&id)
	if errors.Is(e, sql.ErrNoRows) {
		e = authority.ErrNotFound
	}
	return id, e
}
func (r *Repository) FindChallenge(ctx context.Context, hash []byte) (string, error) {
	var id string
	if r == nil || r.db == nil || len(hash) != 32 {
		return id, authority.ErrInvalid
	}
	e := r.db.QueryRowContext(ctx, `SELECT account_id::text FROM owned_authority_challenges WHERE hash=$1`, hash).Scan(&id)
	if errors.Is(e, sql.ErrNoRows) {
		e = authority.ErrNotFound
	}
	return id, e
}
func (r *Repository) Within(ctx context.Context, id string, fn func(signin.Transaction) error) error {
	if r == nil || r.db == nil || r.authority == nil || fn == nil {
		return authority.ErrUnavailable
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	at, e := r.authority.JoinTransaction(ctx, r.db, tx, id)
	if e != nil {
		return e
	}
	if e = fn(&transaction{repo: r, ctx: ctx, tx: tx, canonical: at}); e != nil {
		return e
	}
	return tx.Commit()
}

// Attempt is deliberately a separate committed write, so failed authentication
// and a later transaction rollback cannot erase the durable rate-limit budget.
func (r *Repository) Attempt(ctx context.Context, bucket []byte, limit int, window time.Duration, now time.Time) error {
	if r == nil || r.db == nil || len(bucket) != 32 || limit < 1 || window <= 0 {
		return authority.ErrInvalid
	}
	var n int
	e := r.db.QueryRowContext(ctx, `INSERT INTO owned_authority_attempts(bucket,attempts,window_end) VALUES($1,1,$3) ON CONFLICT(bucket) DO UPDATE SET attempts=CASE WHEN owned_authority_attempts.window_end<=$2 THEN 1 ELSE LEAST(owned_authority_attempts.attempts::bigint+1,2147483647)::integer END,window_end=CASE WHEN owned_authority_attempts.window_end<=$2 THEN $3 ELSE owned_authority_attempts.window_end END RETURNING attempts`, bucket, now, now.Add(window)).Scan(&n)
	if e != nil {
		return e
	}
	if n > limit {
		return signin.ErrRate
	}
	return nil
}

type transaction struct {
	repo      *Repository
	ctx       context.Context
	tx        *sql.Tx
	canonical authority.Transaction
}

func (t *transaction) Authority() authority.Transaction { return t.canonical }
func (t *transaction) Authenticate() (authority.Principal, error) {
	if t.repo.guard == nil {
		return authority.Principal{}, authority.ErrUnavailable
	}
	return t.repo.guard.Authenticate(t.ctx, t.repo.db, t.tx, t.canonical.Account().ID)
}
func (t *transaction) Credentials() (signin.Credentials, error) {
	c := signin.Credentials{AccountID: t.canonical.Account().ID}
	e := t.tx.QueryRowContext(t.ctx, `SELECT email,verified_at FROM owned_authority_credentials WHERE account_id=$1`, c.AccountID).Scan(&c.Email, &c.VerifiedAt)
	if errors.Is(e, sql.ErrNoRows) {
		e = authority.ErrNotFound
	}
	return c, e
}
func (t *transaction) SaveCredentials(c signin.Credentials) error {
	if c.AccountID != t.canonical.Account().ID {
		return authority.ErrInvalid
	}
	res, e := t.tx.ExecContext(t.ctx, `UPDATE owned_authority_credentials SET verified_at=$2 WHERE account_id=$1 AND email=$3`, c.AccountID, c.VerifiedAt, c.Email)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return signin.ErrConflict
	}
	return nil
}
func (t *transaction) Factors() ([]signin.Factor, error) {
	rows, e := t.tx.QueryContext(t.ctx, `SELECT id::text,type,created_at,enabled_at,secret,last_counter,pending_until,revision FROM owned_authority_factors WHERE account_id=$1 ORDER BY type,id`, t.canonical.Account().ID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []signin.Factor{}
	for rows.Next() {
		f := signin.Factor{Trustworthy: 1}
		if e = rows.Scan(&f.ID, &f.Type, &f.CreatedAt, &f.EnabledAt, &f.Secret, &f.LastCounter, &f.PendingUntil, &f.Revision); e != nil {
			return nil, e
		}
		if f.Type == 4 {
			f.Trustworthy = 0
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
func (t *transaction) SaveFactor(f signin.Factor) error {
	return t.saveFactor(f, false)
}
func (t *transaction) SaveSetupFactor(f signin.Factor) error {
	return t.saveFactor(f, true)
}
func (t *transaction) saveFactor(f signin.Factor, setup bool) error {
	if !authority.ValidID(f.ID) || (f.Type == 4 && len(f.Secret) != 0) || (f.Type != 4 && len(f.Secret) == 0) || f.Type != 0 && f.Type != 3 && f.Type != 4 && f.Type != 5 {
		return authority.ErrInvalid
	}
	res, e := t.tx.ExecContext(t.ctx, `INSERT INTO owned_authority_factors(id,account_id,type,created_at,enabled_at,secret,last_counter,pending_until) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(id) DO UPDATE SET enabled_at=EXCLUDED.enabled_at,secret=EXCLUDED.secret,last_counter=EXCLUDED.last_counter,pending_until=EXCLUDED.pending_until,revision=owned_authority_factors.revision+CASE WHEN (owned_authority_factors.enabled_at,owned_authority_factors.secret,owned_authority_factors.pending_until) IS DISTINCT FROM (EXCLUDED.enabled_at,EXCLUDED.secret,EXCLUDED.pending_until) THEN 1 ELSE 0 END WHERE owned_authority_factors.account_id=EXCLUDED.account_id AND owned_authority_factors.type=EXCLUDED.type AND owned_authority_factors.revision=$9::bigint`, f.ID, t.canonical.Account().ID, f.Type, f.CreatedAt, f.EnabledAt, f.Secret, f.LastCounter, f.PendingUntil, f.Revision)
	if e != nil {
		return conflict(e)
	}
	if e = one(res); e != nil {
		return e
	}
	if !setup {
		var revision int64
		if e = t.tx.QueryRowContext(t.ctx, `SELECT revision FROM owned_authority_factors WHERE id=$1::uuid`, f.ID).Scan(&revision); e != nil {
			return e
		}
		if revision != f.Revision {
			return t.CancelFactorSetup()
		}
	}
	return nil
}
func one(r sql.Result) error {
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return authority.ErrNotFound
	}
	return nil
}
func (t *transaction) DeleteFactor(id string) error {
	if !authority.ValidID(id) {
		return authority.ErrInvalid
	}
	r, e := t.tx.ExecContext(t.ctx, `DELETE FROM owned_authority_factors WHERE account_id=$1 AND id=$2`, t.canonical.Account().ID, id)
	if e != nil {
		return e
	}
	if e = one(r); e != nil {
		return e
	}
	return t.CancelFactorSetup()
}
func (t *transaction) Challenge(hash []byte) (signin.Challenge, error) {
	var c signin.Challenge
	var blacklist, methods []byte
	var contact sql.NullString
	e := t.tx.QueryRowContext(t.ctx, `SELECT hash,account_id::text,account_version,purpose,created_at,expires_at,device_name,platform,password_ok,second_ok,require_second,completed,blacklist,updated_at,device_id,ip_address,user_agent,failed_attempts,contact_id::text,auth_methods FROM owned_authority_challenges WHERE account_id=$1 AND hash=$2`, t.canonical.Account().ID, hash).Scan(&c.Hash, &c.AccountID, &c.Version, &c.Purpose, &c.CreatedAt, &c.ExpiredAt, &c.DeviceName, &c.Platform, &c.PasswordOK, &c.SecondOK, &c.RequireSecond, &c.Completed, &blacklist, &c.UpdatedAt, &c.DeviceID, &c.IPAddress, &c.UserAgent, &c.FailedAttempts, &contact, &methods)
	c.ContactID = contact.String
	if errors.Is(e, sql.ErrNoRows) {
		e = authority.ErrNotFound
	}
	if e == nil {
		e = json.Unmarshal(blacklist, &c.BlacklistFactors)
		if e == nil {
			e = json.Unmarshal(methods, &c.AuthMethods)
		}
	}
	return c, e
}
func (t *transaction) SaveChallenge(c signin.Challenge) error {
	if c.AccountID != t.canonical.Account().ID || len(c.Hash) != 32 {
		return authority.ErrInvalid
	}
	if c.BlacklistFactors == nil {
		c.BlacklistFactors = []string{}
	}
	b, e := json.Marshal(c.BlacklistFactors)
	if e != nil {
		return e
	}
	methods, e := json.Marshal(c.AuthMethods)
	if e != nil {
		return e
	}
	if c.AuthMethods == nil {
		methods = []byte("[]")
	}
	var contact any
	if c.ContactID != "" {
		if !authority.ValidID(c.ContactID) || c.Purpose != "verify_contact" {
			return authority.ErrInvalid
		}
		contact = c.ContactID
	} else if c.Purpose == "verify_contact" {
		return authority.ErrInvalid
	}
	r, e := t.tx.ExecContext(t.ctx, `INSERT INTO owned_authority_challenges(hash,account_id,account_version,purpose,created_at,expires_at,device_name,platform,password_ok,second_ok,require_second,completed,blacklist,updated_at,device_id,ip_address,user_agent,failed_attempts,contact_id,auth_methods) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19::uuid,$20::jsonb) ON CONFLICT(hash) DO UPDATE SET password_ok=EXCLUDED.password_ok,second_ok=EXCLUDED.second_ok,completed=EXCLUDED.completed,blacklist=EXCLUDED.blacklist,updated_at=EXCLUDED.updated_at,failed_attempts=EXCLUDED.failed_attempts,auth_methods=EXCLUDED.auth_methods WHERE owned_authority_challenges.account_id=EXCLUDED.account_id AND owned_authority_challenges.account_version=EXCLUDED.account_version AND owned_authority_challenges.purpose=EXCLUDED.purpose AND owned_authority_challenges.contact_id IS NOT DISTINCT FROM EXCLUDED.contact_id AND (NOT owned_authority_challenges.completed OR EXCLUDED.completed)`, c.Hash, c.AccountID, c.Version, c.Purpose, c.CreatedAt, c.ExpiredAt, c.DeviceName, c.Platform, c.PasswordOK, c.SecondOK, c.RequireSecond, c.Completed, b, c.UpdatedAt, c.DeviceID, c.IPAddress, c.UserAgent, c.FailedAttempts, contact, methods)
	if e != nil {
		return e
	}
	return one(r)
}
func (t *transaction) CancelChallenges(purpose string) error {
	_, e := t.tx.ExecContext(t.ctx, `UPDATE owned_authority_challenges SET completed=true WHERE account_id=$1 AND ($2='' OR purpose=$2)`, t.canonical.Account().ID, purpose)
	return e
}
