// SPDX-License-Identifier: AGPL-3.0-only
package connectionpg

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/auditcapture"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	p "solsynth.local/go-migration/ports/passport/connections"
	"solsynth.local/go-migration/ports/passport/history"
	"time"
)

//go:embed schema.sql
var Schema string

type Config struct {
	Providers     []p.Provider
	EncryptionKey []byte
}
type Repository struct {
	db        *sql.DB
	guard     *authoritypg.TransactionGuard
	providers map[string]p.Provider
	seal      cipher.AEAD
}

func New(db *sql.DB, g *authoritypg.TransactionGuard, c Config) (*Repository, error) {
	if db == nil || g == nil || len(c.EncryptionKey) != 32 || len(c.Providers) == 0 {
		return nil, p.ErrUnavailable
	}
	block, e := aes.NewCipher(c.EncryptionKey)
	if e != nil {
		return nil, e
	}
	seal, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	r := &Repository{db: db, guard: g, providers: map[string]p.Provider{}, seal: seal}
	for _, v := range c.Providers {
		if v == nil || !p.ValidProvider(v.Name()) || r.providers[v.Name()] != nil {
			return nil, p.ErrInvalid
		}
		r.providers[v.Name()] = v
	}
	return r, nil
}
func token() (string, error) {
	b := make([]byte, 32)
	_, e := rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b), e
}
func uuid() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func (r *Repository) begin(ctx context.Context, actor string) (tx *sql.Tx, proof authority.Principal, e error) {
	if r == nil || !authority.ValidID(actor) {
		return nil, proof, p.ErrUnavailable
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
	if _, e = r.guard.Preflight(ctx, r.db, tx, actor, p.Permission); e != nil {
		return
	}
	var id string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE`, actor).Scan(&id); e != nil {
		return
	}
	proof, e = r.authorize(ctx, tx, actor)
	return
}
func (r *Repository) authorize(ctx context.Context, tx *sql.Tx, actor string) (authority.Principal, error) {
	v, e := r.guard.Authorize(ctx, r.db, tx, actor, p.Permission)
	if e == nil && (v.Session.Type != 0 || v.Session.ParentID != "" || v.Session.ClientID != "") {
		e = authority.ErrForbidden
	}
	return v, e
}
func (r *Repository) Ready(ctx context.Context) error {
	_, e := r.db.ExecContext(ctx, `SELECT state_hash,verifier FROM owned_account_connection_challenges LIMIT 0`)
	return e
}
func (r *Repository) Start(ctx context.Context, actor, provider string) (out p.Started, e error) {
	if r == nil {
		return out, p.ErrUnavailable
	}
	v := r.providers[provider]
	if v == nil {
		return out, p.ErrUnavailable
	}
	tx, proof, e := r.begin(ctx, actor)
	if e != nil {
		return
	}
	defer tx.Rollback()
	var outstanding int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_account_connection_challenges WHERE account_id=$1::uuid AND consumed_at IS NULL AND expires_at>clock_timestamp()`, actor).Scan(&outstanding); e != nil {
		return
	}
	if outstanding >= 32 {
		return out, p.ErrConflict
	}
	state, e := token()
	if e != nil {
		return
	}
	verifier, e := token()
	if e != nil {
		return
	}
	h := sha256.Sum256([]byte(verifier))
	url, e := v.Start(state, base64.RawURLEncoding.EncodeToString(h[:]))
	if e != nil {
		return
	}
	digest := sha256.Sum256([]byte(state))
	nonce := make([]byte, r.seal.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return
	}
	sealed := r.seal.Seal(nonce, nonce, []byte(verifier), digest[:])
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_account_connection_challenges(state_hash,account_id,session_id,account_version,session_epoch,authorization_epoch,provider,verifier,created_at,expires_at)SELECT $1::bytea,$2::uuid,$3::uuid,$4::bigint,$5::bigint,$6::bigint,$7::text,$8::bytea,instant.now,LEAST(instant.now+interval '10 minutes',$9::timestamptz) FROM(SELECT clock_timestamp()now)instant`, digest[:], actor, proof.Session.ID, proof.Account.Version, proof.Session.Epoch, proof.Session.AuthorizationEpoch, provider, sealed, proof.Session.ExpiresAt)
	if e != nil {
		return
	}
	if _, e = r.authorize(ctx, tx, actor); e != nil {
		return
	}
	e = tx.Commit()
	if e == nil {
		out = p.Started{State: state, AuthorizationURL: url}
	}
	return
}
func (r *Repository) Complete(ctx context.Context, actor, provider string, input p.Completion) (out history.Connection, e error) {
	if r == nil {
		return out, p.ErrUnavailable
	}
	v := r.providers[provider]
	if v == nil {
		return out, p.ErrUnavailable
	}
	b, x := base64.RawURLEncoding.DecodeString(input.State)
	if x != nil || len(b) != 32 || base64.RawURLEncoding.EncodeToString(b) != input.State || len(input.Code) > 4096 || len(input.OpenID) > 32 {
		return out, p.ErrInvalid
	}
	digest := sha256.Sum256([]byte(input.State))
	tx, proof, e := r.begin(ctx, actor)
	if e != nil {
		return
	}
	defer tx.Rollback()
	var sealed []byte
	var expires time.Time
	e = tx.QueryRowContext(ctx, `UPDATE owned_account_connection_challenges SET consumed_at=clock_timestamp()WHERE state_hash=$1::bytea AND account_id=$2::uuid AND session_id=$3::uuid AND account_version=$4::bigint AND session_epoch=$5::bigint AND authorization_epoch=$6::bigint AND provider=$7::text AND consumed_at IS NULL AND expires_at>clock_timestamp() RETURNING verifier,expires_at`, digest[:], actor, proof.Session.ID, proof.Account.Version, proof.Session.Epoch, proof.Session.AuthorizationEpoch, provider).Scan(&sealed, &expires)
	if errors.Is(e, sql.ErrNoRows) {
		return out, p.ErrProof
	}
	if e != nil {
		return
	}
	if _, e = r.authorize(ctx, tx, actor); e != nil {
		return
	}
	if e = tx.Commit(); e != nil {
		return
	}
	if len(sealed) < r.seal.NonceSize() {
		return out, p.ErrProof
	}
	secret, e := r.seal.Open(nil, sealed[:r.seal.NonceSize()], sealed[r.seal.NonceSize():], digest[:])
	if e != nil {
		return out, p.ErrProof
	}
	// State is irreversibly consumed before any external call; original opaque
	// request proof is revalidated after the provider response, under account lock.
	bounded, cancel := context.WithDeadline(ctx, expires)
	defer cancel()
	verified, e := v.Verify(bounded, input, string(secret))
	if e != nil {
		return out, e
	}
	if e = p.ValidateVerified(verified); e != nil {
		return
	}
	tx, after, e := r.begin(ctx, actor)
	if e != nil {
		return
	}
	defer tx.Rollback()
	if after.Session.ID != proof.Session.ID || after.Session.Epoch != proof.Session.Epoch || after.Session.AuthorizationEpoch != proof.Session.AuthorizationEpoch || after.Account.Version != proof.Account.Version {
		return out, p.ErrProof
	}
	var timely bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_account_connection_challenges WHERE state_hash=$1::bytea AND consumed_at IS NOT NULL AND expires_at>clock_timestamp())`, digest[:]).Scan(&timely); e != nil {
		return out, e
	}
	if !timely {
		return out, p.ErrProof
	}
	key, e := uuid()
	if e != nil {
		return
	}
	out.ID = key
	out.AccountID = actor
	out.Provider = provider
	out.ProvidedIdentifier = verified.Identifier
	out.Meta = map[string]string{}
	for k, v := range map[string]string{"email": verified.Email, "username": verified.Username, "display_name": verified.DisplayName} {
		if v != "" {
			out.Meta[k] = v
		}
	}
	e = tx.QueryRowContext(ctx, `INSERT INTO owned_account_connections(id,account_id,provider,provided_identifier,email,username,display_name,is_public,last_used_at,created_at,updated_at,verified_at,source_ref)VALUES($1::uuid,$2::uuid,$3::text,$4::text,NULLIF($5::text,''),NULLIF($6::text,''),NULLIF($7::text,''),false,clock_timestamp(),clock_timestamp(),clock_timestamp(),clock_timestamp(),$8::text)ON CONFLICT(account_id,provider,provided_identifier)DO UPDATE SET email=EXCLUDED.email,username=EXCLUDED.username,display_name=EXCLUDED.display_name,updated_at=EXCLUDED.updated_at,verified_at=EXCLUDED.verified_at,revoked_at=NULL,last_used_at=EXCLUDED.last_used_at,source_ref=EXCLUDED.source_ref RETURNING id::text,created_at,updated_at,last_used_at,is_public`, key, actor, provider, verified.Identifier, verified.Email, verified.Username, verified.DisplayName, "verified-provider:"+provider+":"+hex.EncodeToString(digest[:])).Scan(&out.ID, &out.CreatedAt, &out.UpdatedAt, &out.LastUsedAt, &out.IsPublic)
	if e != nil {
		var pg *pgconn.PgError
		if errors.As(e, &pg) && pg.Code == "23505" {
			e = p.ErrConflict
		}
		return
	}
	if e = auditcapture.Account(ctx, tx, actor, "accounts.connection.verify", out.ID); e != nil {
		return out, e
	}
	if _, e = r.authorize(ctx, tx, actor); e != nil {
		return
	}
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()<$1::timestamptz`, expires).Scan(&timely); e != nil {
		return out, e
	}
	if !timely {
		return out, p.ErrProof
	}
	e = tx.Commit()
	return
}
func (r *Repository) Delete(ctx context.Context, actor, id string) error {
	_, e := r.mutate(ctx, actor, id, nil)
	return e
}
func (r *Repository) SetPublic(ctx context.Context, actor, id string, public bool) (history.Connection, error) {
	return r.mutate(ctx, actor, id, &public)
}
func (r *Repository) mutate(ctx context.Context, actor, id string, public *bool) (out history.Connection, e error) {
	if !authority.ValidID(id) {
		return out, p.ErrInvalid
	}
	tx, _, e := r.begin(ctx, actor)
	if e != nil {
		return
	}
	defer tx.Rollback()
	out.Meta = map[string]string{}
	e = tx.QueryRowContext(ctx, `SELECT id::text,account_id::text,provider,provided_identifier,is_public,last_used_at,created_at,updated_at FROM owned_account_connections WHERE id=$1::uuid AND account_id=$2::uuid AND revoked_at IS NULL FOR UPDATE`, id, actor).Scan(&out.ID, &out.AccountID, &out.Provider, &out.ProvidedIdentifier, &out.IsPublic, &out.LastUsedAt, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		return out, authority.ErrNotFound
	}
	if e != nil {
		return
	}
	if public == nil {
		_, e = tx.ExecContext(ctx, `UPDATE owned_account_connections SET revoked_at=clock_timestamp(),updated_at=clock_timestamp(),is_public=false WHERE id=$1::uuid`, id)
	} else {
		out.IsPublic = *public
		_, e = tx.ExecContext(ctx, `UPDATE owned_account_connections SET is_public=$2::boolean,updated_at=clock_timestamp() WHERE id=$1::uuid`, id, *public)
	}
	if e != nil {
		return
	}
	action := "accounts.connection.visibility"
	if public == nil {
		action = "accounts.connection.unlink"
	}
	if e = auditcapture.Account(ctx, tx, actor, action, id); e != nil {
		return out, e
	}
	if _, e = r.authorize(ctx, tx, actor); e != nil {
		return
	}
	e = tx.Commit()
	return
}
func (r *Repository) ProcessDue(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, p.ErrInvalid
	}
	res, e := r.db.ExecContext(ctx, `DELETE FROM owned_account_connection_challenges WHERE state_hash IN(SELECT state_hash FROM owned_account_connection_challenges WHERE expires_at<clock_timestamp()-interval '24 hours' ORDER BY expires_at,state_hash LIMIT $1::integer)`, limit)
	if e != nil {
		return 0, e
	}
	n, e := res.RowsAffected()
	return int(n), e
}

var _ p.Repository = (*Repository)(nil)
