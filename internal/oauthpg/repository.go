// SPDX-License-Identifier: AGPL-3.0-only
package oauthpg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"solsynth.local/go-migration/internal/apppg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/oauth"
	"solsynth.local/go-migration/ports/develop/apps"
	"sort"
	"time"
)

//go:embed schema.sql
var Schema string

type Repository struct {
	db     *sql.DB
	store  *authoritypg.Repository
	issuer *authority.Service
	guard  *authoritypg.TransactionGuard
	apps   *apppg.Repository
}

func NewWithAuthority(db *sql.DB, store *authoritypg.Repository, issuer *authority.Service, guard *authoritypg.TransactionGuard, appStore *apppg.Repository) (*Repository, error) {
	if db == nil || store == nil || issuer == nil || guard == nil || appStore == nil {
		return nil, oauth.ErrUnavailable
	}
	u, e := url.Parse(issuer.OAuthIssuer())
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, oauth.ErrUnavailable
	}
	return &Repository{db, store, issuer, guard, appStore}, nil
}
func (r *Repository) Ready(ctx context.Context) error {
	if r == nil || r.db == nil {
		return oauth.ErrUnavailable
	}
	_, e := r.db.ExecContext(ctx, `SELECT oauth_epoch FROM owned_custom_apps LIMIT 0; SELECT version FROM owned_app_consents LIMIT 0; SELECT email,verified_at FROM owned_authority_credentials LIMIT 0; SELECT request_hash,owner_id,app_epoch FROM owned_oauth_requests LIMIT 0; SELECT hash,consent_version,session_id FROM owned_oauth_codes LIMIT 0; SELECT session_id,owner_id,secret_hash FROM owned_oauth_grants LIMIT 0; SELECT hash,epoch,used_at FROM owned_oauth_refresh LIMIT 0; SELECT account_id,action FROM owned_oauth_audit LIMIT 0`)
	return e
}

var _ oauth.Repository = (*Repository)(nil)

func random() (string, error) {
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
func digest(kind, raw string) []byte {
	h := sha256.Sum256([]byte("solsynth.oauth.v1\x00" + kind + "\x00" + raw))
	return h[:]
}
func rawJSON(v any) []byte               { b, _ := json.Marshal(v); return b }
func requestHash(v oauth.Request) []byte { return digest("request", string(rawJSON(v))) }
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func audit(ctx context.Context, tx *sql.Tx, account, app, action string, now time.Time) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_oauth_audit(account_id,app_id,action,occurred_at)VALUES($1,$2,$3,$4)`, account, app, action, now)
	return e
}

type boundary struct {
	commitRevocation    bool
	final               func() error
	tx                  *sql.Tx
	authority           authority.Transaction
	app                 apps.App
	epoch, ownerVersion int64
	owner               string
	proof               authority.Principal
	now                 time.Time
}

func (r *Repository) within(ctx context.Context, actor, client string, proof bool, fn func(*boundary) error) error {
	if r == nil || r.db == nil || !authority.ValidID(actor) {
		return oauth.ErrGrant
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	a, _, owner, e := r.apps.OAuthRegistration(ctx, r.db, tx, client, false)
	if e != nil {
		return oauth.ErrClient
	}
	ids := []string{actor}
	if owner != "" && owner != actor {
		ids = append(ids, owner)
	}
	sort.Strings(ids)
	for _, id := range ids {
		var found string
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, id).Scan(&found); e != nil {
			return oauth.ErrGrant
		}
	}
	b := &boundary{tx: tx, owner: owner}
	if b.authority, e = r.store.JoinTransaction(ctx, r.db, tx, actor); e != nil {
		return e
	}
	if proof {
		if b.proof, e = r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
			return e
		}
		if b.proof.Session.Type != 0 {
			return oauth.ErrDenied
		}
	}
	var freshOwner string
	b.app, b.epoch, freshOwner, e = r.apps.OAuthRegistration(ctx, r.db, tx, client, true)
	if e != nil || freshOwner != owner || b.app.ID != a.ID {
		return oauth.ErrClient
	}
	if owner != "" {
		if e = tx.QueryRowContext(ctx, `SELECT version FROM owned_authority_accounts WHERE id=$1`, owner).Scan(&b.ownerVersion); e != nil {
			return e
		}
	}
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&b.now); e != nil {
		return e
	}
	b.now = b.now.UTC()
	if proof {
		if _, e = r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
			return e
		}
	}
	if e = fn(b); e != nil {
		return e
	}
	// A proven replay/revocation must remain revoked even if its client expires
	// while this operation is completing; returning a denial must not undo it.
	if b.commitRevocation {
		return tx.Commit()
	}
	// Revalidate time-dependent user/client state after all downstream waits.
	if proof {
		if _, e = r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
			return e
		}
	}
	if _, epoch, currentOwner, x := r.apps.OAuthRegistration(ctx, r.db, tx, client, false); x != nil || epoch != b.epoch || currentOwner != b.owner {
		return oauth.ErrClient
	}
	if b.final != nil {
		if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&b.now); e != nil {
			return e
		}
		if e = b.final(); e != nil {
			return e
		}
	}
	return tx.Commit()
}

func (r *Repository) Preview(ctx context.Context, actor string, req oauth.Request) (out oauth.ClientInfo, e error) {
	e = r.within(ctx, actor, req.ClientID, true, func(b *boundary) error {
		scopes, e := oauth.Validate(req, b.app)
		if e != nil {
			return e
		}
		var id string
		var hash []byte
		var exp time.Time
		var decided *time.Time
		var appEpoch, ownerVersion int64
		var ownerID sql.NullString
		e = b.tx.QueryRowContext(ctx, `SELECT id::text,request_hash,expires_at,decided_at,app_epoch,owner_id::text,owner_version FROM owned_oauth_requests WHERE account_id=$1::uuid AND parent_id=$2::uuid AND app_id=$3::uuid AND state_hash=$4::bytea FOR UPDATE`, actor, b.proof.Session.ID, b.app.ID, digest("state", req.State)).Scan(&id, &hash, &exp, &decided, &appEpoch, &ownerID, &ownerVersion)
		if errors.Is(e, sql.ErrNoRows) {
			var n int
			if e = b.tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_oauth_requests WHERE account_id=$1 AND expires_at>$2 AND decided_at IS NULL`, actor, b.now).Scan(&n); e != nil {
				return e
			}
			if n >= 32 {
				return oauth.ErrRequest
			}
			id, e = uuid()
			if e != nil {
				return e
			}
			exp = b.now.Add(5 * time.Minute)
			if exp.After(b.proof.Session.ExpiresAt) {
				exp = b.proof.Session.ExpiresAt
			}
			_, e = b.tx.ExecContext(ctx, `INSERT INTO owned_oauth_requests(id,account_id,parent_id,app_id,state_hash,request_hash,app_epoch,owner_id,owner_version,created_at,expires_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id, actor, b.proof.Session.ID, b.app.ID, digest("state", req.State), requestHash(req), b.epoch, nullable(b.owner), b.ownerVersion, b.now, exp)
		} else if e != nil {
			return e
		} else if decided != nil || !b.now.Before(exp) || !equal(hash, requestHash(req)) || appEpoch != b.epoch || ownerID.String != b.owner || ownerVersion != b.ownerVersion {
			return oauth.ErrGrant
		}
		if e != nil {
			return e
		}
		out = oauth.ClientInfo{ClientName: b.app.Name, ClientID: b.app.Slug, Description: b.app.Description, Scopes: scopes, ExpiresAt: exp}
		if b.app.OAuthConfig != nil {
			out.HomeURI = b.app.OAuthConfig.ClientURI
		}
		return nil
	})
	return
}
func equal(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

func (r *Repository) Decide(ctx context.Context, actor string, req oauth.Request, approve bool) (out oauth.Decision, e error) {
	e = r.within(ctx, actor, req.ClientID, true, func(b *boundary) error {
		scopes, e := oauth.Validate(req, b.app)
		if e != nil {
			return e
		}
		var id string
		var valid bool
		e = b.tx.QueryRowContext(ctx, `SELECT id::text,request_hash=$5 AND app_epoch=$6 AND owner_id IS NOT DISTINCT FROM $7::uuid AND owner_version=$8 AND expires_at>$9 AND decided_at IS NULL FROM owned_oauth_requests WHERE account_id=$1 AND parent_id=$2 AND app_id=$3 AND state_hash=$4 FOR UPDATE`, actor, b.proof.Session.ID, b.app.ID, digest("state", req.State), requestHash(req), b.epoch, nullable(b.owner), b.ownerVersion, b.now).Scan(&id, &valid)
		if e != nil || !valid {
			return oauth.ErrGrant
		}
		if _, e = b.tx.ExecContext(ctx, `UPDATE owned_oauth_requests SET decided_at=$2 WHERE id=$1`, id, b.now); e != nil {
			return e
		}
		u, _ := url.Parse(req.RedirectURI)
		q := u.Query()
		q.Set("state", req.State)
		q.Set("iss", r.issuer.OAuthIssuer())
		if !approve {
			q.Set("error", "access_denied")
			u.RawQuery = q.Encode()
			out.RedirectURI = u.String()
			return audit(ctx, b.tx, actor, b.app.ID, "authorization.declined", b.now)
		}
		var version int64
		var prior []byte
		var expires time.Time
		var revoked *time.Time
		e = b.tx.QueryRowContext(ctx, `SELECT version,scopes,expires_at,revoked_at FROM owned_app_consents WHERE account_id=$1 AND app_id=$2 FOR UPDATE`, actor, b.app.ID).Scan(&version, &prior, &expires, &revoked)
		var previous []string
		_ = json.Unmarshal(prior, &previous)
		same := len(previous) == len(scopes)
		for _, s := range scopes {
			same = same && oauth.Contains(previous, s)
		}
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if errors.Is(e, sql.ErrNoRows) || revoked != nil || !b.now.Before(expires) || !same {
			expires = b.now.Add(24 * time.Hour)
			e = b.tx.QueryRowContext(ctx, `INSERT INTO owned_app_consents(account_id,app_id,scopes,expires_at,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$5)ON CONFLICT(account_id,app_id)DO UPDATE SET scopes=EXCLUDED.scopes,expires_at=EXCLUDED.expires_at,updated_at=EXCLUDED.updated_at,revoked_at=NULL,version=owned_app_consents.version+1 RETURNING version`, actor, b.app.ID, rawJSON(scopes), expires, b.now).Scan(&version)
			if e != nil {
				return e
			}
		}
		raw, e := random()
		if e != nil {
			return e
		}
		exp := b.now.Add(time.Minute)
		if exp.After(b.proof.Session.ExpiresAt) {
			exp = b.proof.Session.ExpiresAt
		}
		_, e = b.tx.ExecContext(ctx, `INSERT INTO owned_oauth_codes(hash,request_id,account_id,parent_id,app_id,client_id,redirect_uri,challenge,nonce,scopes,app_epoch,owner_id,owner_version,consent_version,created_at,expires_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, digest("code", raw), id, actor, b.proof.Session.ID, b.app.ID, req.ClientID, req.RedirectURI, req.CodeChallenge, req.Nonce, rawJSON(scopes), b.epoch, nullable(b.owner), b.ownerVersion, version, b.now, exp)
		if e != nil {
			return e
		}
		q.Set("code", raw)
		u.RawQuery = q.Encode()
		out.RedirectURI = u.String()
		return audit(ctx, b.tx, actor, b.app.ID, "authorization.approved", b.now)
	})
	return
}
