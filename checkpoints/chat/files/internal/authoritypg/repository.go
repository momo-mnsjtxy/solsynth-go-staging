// SPDX-License-Identifier: AGPL-3.0-only
package authoritypg

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"solsynth.local/go-migration/ports/authority"
)

// Schema is an explicit migration input. New and the server never execute it.
//
//go:embed schema.sql
var baseSchema string

//go:embed assurance_schema.sql
var AssuranceSchema string

//go:embed continuity_schema.sql
var ContinuitySchema string

//go:embed activation_schema.sql
var ActivationSchema string

//go:embed stream_ticket_schema.sql
var StreamTicketSchema string

var Schema = baseSchema + "\n" + AssuranceSchema + "\n" + ContinuitySchema + "\n" + ActivationSchema + "\n" + StreamTicketSchema

type Repository struct{ db *sql.DB }

func New(db *sql.DB) *Repository { return &Repository{db: db} }

var _ authority.Store = (*Repository)(nil)

func (r *Repository) WithinAccount(ctx context.Context, id string, fn func(authority.Transaction) error) error {
	if r == nil || r.db == nil || fn == nil || !authority.ValidID(id) {
		return authority.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var a authority.Account
	e = tx.QueryRowContext(ctx, `SELECT id::text,name,nick,language,region,is_superuser,disabled,version,created_at FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, id).Scan(&a.ID, &a.Name, &a.Nick, &a.Language, &a.Region, &a.Superuser, &a.Disabled, &a.Version, &a.CreatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		return authority.ErrNotFound
	}
	if e != nil {
		return e
	}
	t := &transaction{tx: tx, ctx: ctx, account: a}
	if e = fn(t); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *Repository) FindRefresh(ctx context.Context, hash []byte) (string, string, error) {
	if r == nil || r.db == nil || len(hash) != 32 {
		return "", "", authority.ErrInvalid
	}
	var a, s string
	e := r.db.QueryRowContext(ctx, `SELECT account_id::text,id::text FROM owned_authority_sessions WHERE refresh_hash=$1 OR previous_hash=$1`, hash).Scan(&a, &s)
	if errors.Is(e, sql.ErrNoRows) {
		e = authority.ErrNotFound
	}
	return a, s, e
}
func (r *Repository) FindCode(ctx context.Context, hash []byte) (string, error) {
	if r == nil || r.db == nil || len(hash) != 32 {
		return "", authority.ErrInvalid
	}
	var a string
	e := r.db.QueryRowContext(ctx, `SELECT account_id::text FROM owned_authority_codes WHERE hash=$1`, hash).Scan(&a)
	if errors.Is(e, sql.ErrNoRows) {
		e = authority.ErrNotFound
	}
	return a, e
}

// ReadAccount uses one read-only repeatable-read snapshot. A database role with
// SELECT-only grants can back domain-service public-key verifiers.
func (r *Repository) ReadAccount(ctx context.Context, id string, fn func(authority.Transaction) error) error {
	if r == nil || r.db == nil || fn == nil || !authority.ValidID(id) {
		return authority.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var a authority.Account
	e = tx.QueryRowContext(ctx, `SELECT id::text,name,nick,language,region,is_superuser,disabled,version,created_at FROM owned_authority_accounts WHERE id=$1`, id).Scan(&a.ID, &a.Name, &a.Nick, &a.Language, &a.Region, &a.Superuser, &a.Disabled, &a.Version, &a.CreatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		return authority.ErrNotFound
	}
	if e != nil {
		return e
	}
	if e = fn(&transaction{tx: tx, ctx: ctx, account: a, readOnly: true}); e != nil {
		return e
	}
	return tx.Commit()
}

type transaction struct {
	tx       *sql.Tx
	ctx      context.Context
	account  authority.Account
	readOnly bool
}

func (t *transaction) Account() authority.Account { return t.account }
func (t *transaction) SaveAccount(a authority.Account) error {
	if t.readOnly {
		return authority.ErrForbidden
	}
	if a.ID != t.account.ID || a.Version < t.account.Version {
		return authority.ErrInvalid
	}
	res, e := t.tx.ExecContext(t.ctx, `UPDATE owned_authority_accounts SET name=$2,nick=$3,language=$4,region=$5,is_superuser=$6,disabled=$7,version=$8 WHERE id=$1`, a.ID, a.Name, a.Nick, a.Language, a.Region, a.Superuser, a.Disabled, a.Version)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return authority.ErrNotFound
	}
	t.account = a
	return nil
}

const sessionColumns = `id::text,account_id::text,COALESCE(parent_id::text,''),label,type,client_id,created_at,last_granted_at,category,trusted,epoch,authorization_epoch,account_version,expires_at,revoked_at,scopes,refresh_hash,previous_hash,previous_until,sealed_pair`

type scanner interface{ Scan(...any) error }

func scanSession(row scanner) (authority.Session, error) {
	var s authority.Session
	var scopes []byte
	var previous sql.NullTime
	e := row.Scan(&s.ID, &s.AccountID, &s.ParentID, &s.Label, &s.Type, &s.ClientID, &s.CreatedAt, &s.LastGrantedAt, &s.Category, &s.Trusted, &s.Epoch, &s.AuthorizationEpoch, &s.AccountVersion, &s.ExpiresAt, &s.RevokedAt, &scopes, &s.RefreshHash, &s.PreviousHash, &previous, &s.SealedPair)
	if errors.Is(e, sql.ErrNoRows) {
		return s, authority.ErrNotFound
	}
	if e != nil {
		return s, e
	}
	if e = json.Unmarshal(scopes, &s.Scopes); e != nil {
		return s, e
	}
	if previous.Valid {
		s.PreviousUntil = previous.Time
	}
	return s, nil
}
func (t *transaction) Session(id string) (authority.Session, error) {
	if !authority.ValidID(id) {
		return authority.Session{}, authority.ErrNotFound
	}
	return scanSession(t.tx.QueryRowContext(t.ctx, `SELECT `+sessionColumns+` FROM owned_authority_sessions WHERE account_id=$1 AND id=$2`, t.account.ID, id))
}
func (t *transaction) Sessions() ([]authority.Session, error) {
	rows, e := t.tx.QueryContext(t.ctx, `SELECT `+sessionColumns+` FROM owned_authority_sessions WHERE account_id=$1 ORDER BY created_at DESC,id ASC`, t.account.ID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []authority.Session{}
	for rows.Next() {
		s, e := scanSession(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
func (t *transaction) SaveSession(s authority.Session) error {
	if t.readOnly {
		return authority.ErrForbidden
	}
	if s.AccountID != t.account.ID || !authority.ValidID(s.ID) || s.Epoch < 1 || s.AuthorizationEpoch < 1 || s.AuthorizationEpoch > s.Epoch || s.AccountVersion < 1 {
		return authority.ErrInvalid
	}
	var previous *authority.Session
	old, e := t.Session(s.ID)
	if e == nil {
		previous = &old
	} else if !errors.Is(e, authority.ErrNotFound) {
		return e
	}
	s, e = authority.PrepareSessionWrite(previous, s)
	if e != nil {
		return e
	}
	scopes, e := json.Marshal(s.Scopes)
	if e != nil {
		return e
	}
	var parent any
	if s.ParentID != "" {
		if !authority.ValidID(s.ParentID) {
			return authority.ErrInvalid
		}
		parent = s.ParentID
	}
	var until any
	if !s.PreviousUntil.IsZero() {
		until = s.PreviousUntil
	}
	res, e := t.tx.ExecContext(t.ctx, `INSERT INTO owned_authority_sessions(id,account_id,parent_id,label,type,client_id,created_at,last_granted_at,category,trusted,epoch,authorization_epoch,account_version,expires_at,revoked_at,scopes,refresh_hash,previous_hash,previous_until,sealed_pair) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20) ON CONFLICT(id) DO UPDATE SET label=EXCLUDED.label,last_granted_at=EXCLUDED.last_granted_at,epoch=EXCLUDED.epoch,authorization_epoch=EXCLUDED.authorization_epoch,expires_at=EXCLUDED.expires_at,revoked_at=EXCLUDED.revoked_at,scopes=EXCLUDED.scopes,refresh_hash=EXCLUDED.refresh_hash,previous_hash=EXCLUDED.previous_hash,previous_until=EXCLUDED.previous_until,sealed_pair=EXCLUDED.sealed_pair WHERE owned_authority_sessions.account_id=EXCLUDED.account_id AND EXCLUDED.epoch>=owned_authority_sessions.epoch AND EXCLUDED.authorization_epoch>=owned_authority_sessions.authorization_epoch AND (owned_authority_sessions.revoked_at IS NULL OR EXCLUDED.revoked_at IS NOT NULL) AND owned_authority_sessions.account_version=EXCLUDED.account_version AND owned_authority_sessions.type=EXCLUDED.type AND owned_authority_sessions.client_id=EXCLUDED.client_id`, s.ID, s.AccountID, parent, s.Label, s.Type, s.ClientID, s.CreatedAt, s.LastGrantedAt, s.Category, s.Trusted, s.Epoch, s.AuthorizationEpoch, s.AccountVersion, s.ExpiresAt, s.RevokedAt, scopes, s.RefreshHash, s.PreviousHash, until, s.SealedPair)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return authority.ErrInvalid
	}
	return nil
}
func (t *transaction) Rules() ([]authority.Rule, error) {
	rows, e := t.tx.QueryContext(t.ctx, `SELECT pattern,deny,starts_at,expires_at FROM owned_authority_grants WHERE account_id=$1 UNION ALL SELECT g.pattern,g.deny,g.starts_at,g.expires_at FROM owned_authority_group_grants g JOIN owned_authority_group_members m ON m.group_id=g.group_id WHERE m.account_id=$1`, t.account.ID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []authority.Rule{}
	for rows.Next() {
		var r authority.Rule
		if e = rows.Scan(&r.Pattern, &r.Deny, &r.StartsAt, &r.ExpiresAt); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (t *transaction) Punishments() ([]authority.Punishment, error) {
	rows, e := t.tx.QueryContext(t.ctx, `SELECT kind,pattern,starts_at,expires_at FROM owned_authority_punishments WHERE account_id=$1`, t.account.ID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []authority.Punishment{}
	for rows.Next() {
		var p authority.Punishment
		if e = rows.Scan(&p.Kind, &p.Pattern, &p.StartsAt, &p.ExpiresAt); e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (t *transaction) Code(hash []byte) (authority.LoginCode, error) {
	var c authority.LoginCode
	var scopes []byte
	e := t.tx.QueryRowContext(t.ctx, `SELECT hash,account_id::text,account_version,expires_at,used,label,scopes FROM owned_authority_codes WHERE account_id=$1 AND hash=$2`, t.account.ID, hash).Scan(&c.Hash, &c.AccountID, &c.AccountVersion, &c.ExpiresAt, &c.Used, &c.Label, &scopes)
	if errors.Is(e, sql.ErrNoRows) {
		return c, authority.ErrNotFound
	}
	if e != nil {
		return c, e
	}
	e = json.Unmarshal(scopes, &c.Scopes)
	return c, e
}
func (t *transaction) SaveCode(c authority.LoginCode) error {
	if t.readOnly {
		return authority.ErrForbidden
	}
	if c.AccountID != t.account.ID || len(c.Hash) != 32 {
		return authority.ErrInvalid
	}
	scopes, e := json.Marshal(c.Scopes)
	if e != nil {
		return e
	}
	res, e := t.tx.ExecContext(t.ctx, `INSERT INTO owned_authority_codes(hash,account_id,account_version,expires_at,used,label,scopes) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(hash) DO UPDATE SET used=EXCLUDED.used WHERE owned_authority_codes.account_id=EXCLUDED.account_id AND NOT owned_authority_codes.used`, c.Hash, c.AccountID, c.AccountVersion, c.ExpiresAt, c.Used, c.Label, scopes)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return authority.ErrInvalid
	}
	return nil
}

// SeedSynthetic is only a fixture helper. The caller must first verify its
// disposable database. It cannot seed realistic account names or superusers.
func SeedSynthetic(ctx context.Context, db *sql.DB, id, name string, now time.Time) error {
	if db == nil || !authority.ValidID(id) || !strings.HasPrefix(name, "synthetic_") {
		return errors.New("authoritypg: synthetic fixture name and ID required")
	}
	_, e := db.ExecContext(ctx, `INSERT INTO owned_authority_accounts(id,name,nick,created_at) VALUES($1,$2,$2,$3)`, id, name, now)
	return e
}

// ReplacePolicy is a trusted provisioning hook, never an HTTP endpoint. It
// serializes with requests/rotation/revocation and atomically replaces direct
// account rules and punishments. No default grants are created.
func (r *Repository) ReplacePolicy(ctx context.Context, id string, rules []authority.Rule, punishments []authority.Punishment) error {
	return r.WithinAccount(ctx, id, func(raw authority.Transaction) error {
		t := raw.(*transaction)
		if _, e := t.tx.ExecContext(ctx, `DELETE FROM owned_authority_grants WHERE account_id=$1`, id); e != nil {
			return e
		}
		if _, e := t.tx.ExecContext(ctx, `DELETE FROM owned_authority_punishments WHERE account_id=$1`, id); e != nil {
			return e
		}
		for _, v := range rules {
			if _, e := t.tx.ExecContext(ctx, `INSERT INTO owned_authority_grants(account_id,pattern,deny,starts_at,expires_at) VALUES($1,$2,$3,$4,$5)`, id, v.Pattern, v.Deny, v.StartsAt, v.ExpiresAt); e != nil {
				return e
			}
		}
		for _, v := range punishments {
			if _, e := t.tx.ExecContext(ctx, `INSERT INTO owned_authority_punishments(account_id,kind,pattern,starts_at,expires_at) VALUES($1,$2,$3,$4,$5)`, id, v.Kind, v.Pattern, v.StartsAt, v.ExpiresAt); e != nil {
				return e
			}
		}
		return nil
	})
}

// LookupAccount is a trusted domain lookup, never proof of authentication or
// permission. UUID references are exact canonical IDs; names use the canonical
// case-folded uniqueness constraint shared by account lookup and registration.
// It does not return private account factors or credentials.
func (r *Repository) LookupAccount(ctx context.Context, reference string) (authority.Account, error) {
	var a authority.Account
	if r == nil || r.db == nil || reference == "" || len(reference) > 256 {
		return a, authority.ErrNotFound
	}
	match := "lower(name)=lower($1::text)"
	if authority.ValidID(reference) {
		match = "id=$1::uuid"
	}
	e := r.db.QueryRowContext(ctx, `SELECT id::text,name,nick,language,region,is_superuser,disabled,version,created_at FROM owned_authority_accounts WHERE `+match, reference).Scan(&a.ID, &a.Name, &a.Nick, &a.Language, &a.Region, &a.Superuser, &a.Disabled, &a.Version, &a.CreatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		e = authority.ErrNotFound
	}
	return a, e
}
