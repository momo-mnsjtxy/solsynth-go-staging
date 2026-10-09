// SPDX-License-Identifier: AGPL-3.0-only
package oauthpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/oauth"
	"solsynth.local/go-migration/ports/develop/apps"
	"strings"
	"time"
)

type clientSecret struct {
	id      string
	hash    []byte
	expires time.Time
}

func (r *Repository) preflightClient(ctx context.Context, client, secret string) error {
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	a, _, _, e := r.apps.OAuthRegistration(ctx, r.db, tx, client, true)
	if e != nil {
		return oauth.ErrClient
	}
	if _, e = r.client(ctx, &boundary{tx: tx, app: a}, secret); e != nil {
		return e
	}
	return tx.Commit()
}
func finalSession(ctx context.Context, b *boundary, se authority.Session) func() error {
	return func() error {
		var active bool
		if e := b.tx.QueryRowContext(ctx, `SELECT NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind IN('disable_account','block_login') AND(p.starts_at IS NULL OR p.starts_at<=$2)AND(p.expires_at IS NULL OR p.expires_at>$2))FROM owned_authority_accounts a WHERE a.id=$1`, se.AccountID, b.now).Scan(&active); e != nil || !active {
			return oauth.ErrGrant
		}
		return b.authority.(authority.SessionBindingTransaction).CheckSessionBinding(se, b.now)
	}
}

func (r *Repository) client(ctx context.Context, b *boundary, raw string) (s clientSecret, e error) {
	c := b.app.OAuthConfig
	if c == nil {
		return s, oauth.ErrClient
	}
	if c.IsPublicClient {
		if raw != "" {
			return s, oauth.ErrClient
		}
		return s, nil
	}
	if raw == "" || !oauth.Contains(c.AllowedScopes, apps.OAuthClientAuthScope) {
		return s, oauth.ErrClient
	}
	s.id, s.hash, s.expires, e = r.apps.OAuthSecret(ctx, r.db, b.tx, b.app.ID, raw)
	if e != nil {
		return s, oauth.ErrClient
	}
	return
}
func revokeFamily(ctx context.Context, b *boundary, session string) error {
	se, e := b.authority.Session(session)
	if e != nil {
		return e
	}
	if se.Type != 1 || se.Category != authority.OAuthSessionCategory {
		return oauth.ErrGrant
	}
	if se.RevokedAt == nil {
		se.RevokedAt = &b.now
		se.Epoch++
		se.RefreshHash = nil
		se.PreviousHash = nil
		se.SealedPair = nil
		if e = b.authority.SaveSession(se); e != nil {
			return e
		}
	}
	_, e = b.tx.ExecContext(ctx, `UPDATE owned_oauth_grants SET revoked_at=COALESCE(revoked_at,$2) WHERE session_id=$1`, session, b.now)
	if e == nil {
		b.commitRevocation = true
	}
	return e
}
func (r *Repository) Token(ctx context.Context, req oauth.TokenRequest) (out oauth.Tokens, e error) {
	if r == nil || req.ClientID == "" || len(req.ClientID) > 128 {
		return out, oauth.ErrClient
	}
	if e = r.preflightClient(ctx, req.ClientID, req.ClientSecret); e != nil {
		return out, e
	}
	switch req.GrantType {
	case "authorization_code":
		if !oauth.Opaque(req.Code, 43, 43) || req.RefreshToken != "" {
			return out, oauth.ErrGrant
		}
		return r.exchange(ctx, req)
	case "refresh_token":
		if !oauth.Opaque(req.RefreshToken, 43, 43) || req.Code != "" || req.RedirectURI != "" || req.CodeVerifier != "" {
			return out, oauth.ErrGrant
		}
		return r.refresh(ctx, req)
	default:
		return out, &oauth.Failure{Code: "unsupported_grant_type", Description: "Only authorization_code and refresh_token are supported.", Status: 400}
	}
}
func (r *Repository) exchange(ctx context.Context, req oauth.TokenRequest) (out oauth.Tokens, e error) {
	var actor, client string
	if e = r.db.QueryRowContext(ctx, `SELECT account_id::text,client_id FROM owned_oauth_codes WHERE hash=$1`, digest("code", req.Code)).Scan(&actor, &client); e != nil || client != req.ClientID {
		return out, oauth.ErrGrant
	}
	var outcome error
	e = r.within(ctx, actor, client, false, func(b *boundary) error {
		secret, e := r.client(ctx, b, req.ClientSecret)
		if e != nil {
			return e
		}
		var parent, redirect, challenge, nonce string
		var scopesRaw []byte
		var epoch, version, ownerVersion int64
		var owner, session sql.NullString
		var exp time.Time
		var used *time.Time
		e = b.tx.QueryRowContext(ctx, `SELECT parent_id::text,redirect_uri,challenge,nonce,scopes,app_epoch,owner_id::text,owner_version,consent_version,expires_at,used_at,session_id::text FROM owned_oauth_codes WHERE hash=$1 AND account_id=$2 AND app_id=$3 FOR UPDATE`, digest("code", req.Code), actor, b.app.ID).Scan(&parent, &redirect, &challenge, &nonce, &scopesRaw, &epoch, &owner, &ownerVersion, &version, &exp, &used, &session)
		if e != nil || redirect != req.RedirectURI || !oauth.PKCE(req.CodeVerifier, challenge) {
			return oauth.ErrGrant
		}
		if used != nil {
			if session.Valid {
				if e = revokeFamily(ctx, b, session.String); e != nil {
					return e
				}
			}
			outcome = oauth.ErrGrant
			return audit(ctx, b.tx, actor, b.app.ID, "code.reuse", b.now)
		}
		if !b.now.Before(exp) || epoch != b.epoch || owner.String != b.owner || ownerVersion != b.ownerVersion {
			return oauth.ErrGrant
		}
		var scopes []string
		if json.Unmarshal(scopesRaw, &scopes) != nil {
			return oauth.ErrGrant
		}
		check := oauth.Request{ClientID: client, RedirectURI: redirect, ResponseType: "code", Scope: strings.Join(scopes, " "), State: strings.Repeat("x", 16), CodeChallenge: challenge, CodeChallengeMethod: "S256", Nonce: nonce}
		if _, e = oauth.Validate(check, b.app); e != nil {
			return e
		}
		var consentExp time.Time
		var active bool
		e = b.tx.QueryRowContext(ctx, `SELECT expires_at,revoked_at IS NULL AND expires_at>$4 AND version=$3 AND scopes @> $5::jsonb FROM owned_app_consents WHERE account_id=$1 AND app_id=$2 FOR UPDATE`, actor, b.app.ID, version, b.now, scopesRaw).Scan(&consentExp, &active)
		if e != nil || !active {
			return oauth.ErrGrant
		}
		if secret.id != "" && secret.expires.Before(consentExp) {
			consentExp = secret.expires
		}
		var count int
		if e = b.tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_oauth_grants WHERE account_id=$1 AND revoked_at IS NULL AND expires_at>$2`, actor, b.now).Scan(&count); e != nil {
			return e
		}
		if count >= 128 {
			return oauth.ErrGrant
		}
		se, pair, e := r.issuer.IssueOAuthTransaction(ctx, b.authority, parent, b.app.ID, scopes, consentExp)
		if e != nil {
			return oauth.ErrGrant
		}
		_, e = b.tx.ExecContext(ctx, `INSERT INTO owned_oauth_grants(session_id,account_id,parent_id,app_id,client_id,app_epoch,owner_id,owner_version,consent_version,secret_id,secret_hash,scopes,nonce,created_at,expires_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, se.ID, actor, parent, b.app.ID, client, b.epoch, nullable(b.owner), b.ownerVersion, version, nullable(secret.id), secret.hash, scopesRaw, nonce, b.now, se.ExpiresAt)
		if e != nil {
			return e
		}
		if _, e = b.tx.ExecContext(ctx, `UPDATE owned_oauth_codes SET used_at=$2,session_id=$3 WHERE hash=$1`, digest("code", req.Code), b.now, se.ID); e != nil {
			return e
		}
		if pair.RefreshToken != "" {
			if _, e = b.tx.ExecContext(ctx, `INSERT INTO owned_oauth_refresh(hash,session_id,epoch,created_at,expires_at)VALUES($1,$2,$3,$4,$5)`, digest("refresh", pair.RefreshToken), se.ID, se.Epoch, b.now, se.ExpiresAt); e != nil {
				return e
			}
		}
		if e = b.authority.(authority.SessionBindingTransaction).CheckSessionBinding(se, b.now); e != nil {
			return e
		}
		idToken, e := r.issuer.OAuthIDToken(ctx, b.authority, se.ID, client, nonce)
		if e != nil {
			return e
		}
		out = oauth.Tokens{AccessToken: pair.Token, TokenType: "Bearer", ExpiresIn: pair.ExpiresIn, RefreshToken: pair.RefreshToken, RefreshExpiresIn: pair.RefreshExpiresIn, IDToken: idToken, Scope: strings.Join(scopes, " ")}
		b.final = finalSession(ctx, b, se)
		return audit(ctx, b.tx, actor, b.app.ID, "code.exchanged", b.now)
	})
	if e == nil && outcome != nil {
		e = outcome
	}
	if e != nil {
		out = oauth.Tokens{}
	}
	return
}
func (r *Repository) refresh(ctx context.Context, req oauth.TokenRequest) (out oauth.Tokens, e error) {
	var actor, client, session string
	if e = r.db.QueryRowContext(ctx, `SELECT g.account_id::text,g.client_id,g.session_id::text FROM owned_oauth_refresh token JOIN owned_oauth_grants g ON g.session_id=token.session_id WHERE token.hash=$1`, digest("refresh", req.RefreshToken)).Scan(&actor, &client, &session); e != nil || client != req.ClientID {
		return out, oauth.ErrGrant
	}
	var outcome error
	e = r.within(ctx, actor, client, false, func(b *boundary) error {
		secret, e := r.client(ctx, b, req.ClientSecret)
		if e != nil {
			return e
		}
		var used *time.Time
		var exp time.Time
		var epoch int64
		if e = b.tx.QueryRowContext(ctx, `SELECT epoch,expires_at,used_at FROM owned_oauth_refresh WHERE hash=$1 AND session_id=$2 FOR UPDATE`, digest("refresh", req.RefreshToken), session).Scan(&epoch, &exp, &used); e != nil {
			return oauth.ErrGrant
		}
		var nonce string
		var secretID sql.NullString
		var hash []byte
		if e = b.tx.QueryRowContext(ctx, `SELECT nonce,secret_id::text,secret_hash FROM owned_oauth_grants WHERE session_id=$1 FOR UPDATE`, session).Scan(&nonce, &secretID, &hash); e != nil {
			return oauth.ErrGrant
		}
		if secretID.String != secret.id || !equal(hash, secret.hash) {
			return oauth.ErrClient
		}
		se, e := b.authority.Session(session)
		if e != nil {
			return oauth.ErrGrant
		}
		if used != nil || epoch != se.Epoch {
			if e = revokeFamily(ctx, b, session); e != nil {
				return e
			}
			outcome = oauth.ErrGrant
			return audit(ctx, b.tx, actor, b.app.ID, "refresh.reuse", b.now)
		}
		if !b.now.Before(exp) || !oauth.Contains(se.Scopes, "offline_access") || b.app.OAuthConfig == nil || !b.app.OAuthConfig.AllowOfflineAccess || !oauth.Contains(b.app.OAuthConfig.AllowedGrantTypes, "refresh_token") {
			return oauth.ErrGrant
		}
		var count int
		if e = b.tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_oauth_refresh WHERE session_id=$1`, session).Scan(&count); e != nil {
			return e
		}
		if count >= 1024 {
			if e = revokeFamily(ctx, b, session); e != nil {
				return e
			}
			outcome = oauth.ErrGrant
			return nil
		}
		se, pair, e := r.issuer.RotateOAuthTransaction(ctx, b.authority, session)
		if e != nil {
			return oauth.ErrGrant
		}
		if _, e = b.tx.ExecContext(ctx, `UPDATE owned_oauth_refresh SET used_at=$2 WHERE hash=$1`, digest("refresh", req.RefreshToken), b.now); e != nil {
			return e
		}
		if _, e = b.tx.ExecContext(ctx, `INSERT INTO owned_oauth_refresh(hash,session_id,epoch,created_at,expires_at)VALUES($1,$2,$3,$4,$5)`, digest("refresh", pair.RefreshToken), session, se.Epoch, b.now, se.ExpiresAt); e != nil {
			return e
		}
		idToken, e := r.issuer.OAuthIDToken(ctx, b.authority, session, client, nonce)
		if e != nil {
			return e
		}
		out = oauth.Tokens{AccessToken: pair.Token, TokenType: "Bearer", ExpiresIn: pair.ExpiresIn, RefreshToken: pair.RefreshToken, RefreshExpiresIn: pair.RefreshExpiresIn, IDToken: idToken, Scope: strings.Join(se.Scopes, " ")}
		b.final = finalSession(ctx, b, se)
		return audit(ctx, b.tx, actor, b.app.ID, "refresh.rotated", b.now)
	})
	if e == nil && outcome != nil {
		e = outcome
	}
	if e != nil {
		out = oauth.Tokens{}
	}
	return
}

func (r *Repository) UserInfo(ctx context.Context, actor string) (out map[string]any, e error) {
	if r == nil || !authority.ValidID(actor) {
		return nil, oauth.ErrGrant
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	p, e := r.guard.Authenticate(ctx, r.db, tx, actor)
	if e != nil {
		return nil, e
	}
	if p.Session.Type != 1 || p.Session.Category != authority.OAuthSessionCategory || !oauth.Contains(p.Session.Scopes, "openid") {
		return nil, oauth.ErrGrant
	}
	out = map[string]any{"sub": p.Account.ID}
	if oauth.Contains(p.Session.Scopes, "profile") {
		out["name"] = p.Account.Nick
		out["preferred_username"] = p.Account.Name
		out["locale"] = p.Account.Language
	}
	if oauth.Contains(p.Session.Scopes, "email") {
		var email string
		var verified bool
		e = tx.QueryRowContext(ctx, `SELECT email,verified_at IS NOT NULL FROM owned_authority_credentials WHERE account_id=$1`, actor).Scan(&email, &verified)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return nil, e
		}
		if e == nil {
			out["email"] = email
			out["email_verified"] = verified
		}
	}
	if _, e = r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
		return nil, e
	}
	return out, tx.Commit()
}
func (r *Repository) RevokeConsent(ctx context.Context, actor, client string) error {
	if r == nil || !authority.ValidID(actor) {
		return oauth.ErrDenied
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	p, e := r.guard.Authenticate(ctx, r.db, tx, actor)
	if e != nil {
		return e
	}
	if p.Session.Type != 0 {
		return oauth.ErrDenied
	}
	var app string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_custom_apps WHERE slug=$1`, client).Scan(&app); e != nil {
		return oauth.ErrClient
	}
	var now time.Time
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_app_consents SET revoked_at=$3,updated_at=$3,version=version+1 WHERE account_id=$1 AND app_id=$2 AND revoked_at IS NULL`, actor, app, now); e != nil {
		return e
	}
	if e = audit(ctx, tx, actor, app, "consent.revoked", now); e != nil {
		return e
	}
	if _, e = r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *Repository) RevokeToken(ctx context.Context, client, secret, token string) error {
	if r == nil || !oauth.Opaque(token, 43, 43) {
		return oauth.ErrRequest
	}
	if e := r.preflightClient(ctx, client, secret); e != nil {
		return e
	}
	var actor, actual, session string
	e := r.db.QueryRowContext(ctx, `SELECT g.account_id::text,g.client_id,g.session_id::text FROM owned_oauth_refresh token JOIN owned_oauth_grants g ON g.session_id=token.session_id WHERE token.hash=$1`, digest("refresh", token)).Scan(&actor, &actual, &session)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	if actual != client {
		return nil
	}
	return r.within(ctx, actor, client, false, func(b *boundary) error {
		if _, e := r.client(ctx, b, secret); e != nil {
			return e
		}
		if e := revokeFamily(ctx, b, session); e != nil {
			return e
		}
		return audit(ctx, b.tx, actor, b.app.ID, "token.revoked", b.now)
	})
}
