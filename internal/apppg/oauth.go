// SPDX-License-Identifier: AGPL-3.0-only
package apppg

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	"strings"
	"time"
)

// OAuthRegistration uses the caller's transaction and the canonical app store.
// For lock=true callers first lock the complete user/owner account set in order.
func (r *Repository) OAuthRegistration(ctx context.Context, db *sql.DB, tx *sql.Tx, client string, lock bool) (a apps.App, epoch int64, owner string, e error) {
	if r == nil || r.db != db || tx == nil || len(client) < 1 || len(client) > 128 {
		return a, 0, "", apps.ErrInvalid
	}
	var app string
	if e = tx.QueryRowContext(ctx, `SELECT a.id::text`+appJoins+` WHERE a.slug=$1 AND `+activeApp, client).Scan(&app); e != nil {
		return a, 0, "", mapped(e)
	}
	a, e = activeClientByID(ctx, tx, app, lock)
	if e != nil {
		return a, 0, "", e
	}
	var nullable sql.NullString
	e = tx.QueryRowContext(ctx, `SELECT a.oauth_epoch,pub.account_id::text`+appJoins+` WHERE a.id=$1 AND `+activeApp, a.ID).Scan(&epoch, &nullable)
	return a, epoch, nullable.String, e
}

// OAuthSecret reuses existing opaque app credentials in a distinct purpose
// scope. No user principal or broader resource access follows from this check.
func (r *Repository) OAuthSecret(ctx context.Context, db *sql.DB, tx *sql.Tx, app, token string) (id string, hash []byte, expires time.Time, e error) {
	if r == nil || r.db != db || tx == nil || !authority.ValidID(app) || len(token) > 256 {
		return "", nil, expires, authority.ErrInvalid
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "oa1" || !authority.ValidID(parts[1]) {
		return "", nil, expires, authority.ErrInvalid
	}
	raw, x := base64.RawURLEncoding.DecodeString(parts[2])
	if x != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != parts[2] {
		return "", nil, expires, authority.ErrInvalid
	}
	var scopes []byte
	var valid bool
	if e = tx.QueryRowContext(ctx, `SELECT secret_hash,scopes,expires_at FROM owned_app_secrets WHERE id=$1 AND app_id=$2 AND revoked_at IS NULL FOR SHARE`, parts[1], app).Scan(&hash, &scopes, &expires); e != nil {
		return "", nil, expires, authority.ErrInvalid
	}
	if e = tx.QueryRowContext(ctx, `SELECT revoked_at IS NULL AND expires_at>clock_timestamp() FROM owned_app_secrets WHERE id=$1`, parts[1]).Scan(&valid); e != nil {
		return "", nil, expires, authority.ErrInvalid
	}
	var declared []string
	if json.Unmarshal(scopes, &declared) != nil || !valid || !scopeContains(declared, apps.OAuthClientAuthScope) || subtle.ConstantTimeCompare(hash, secretDigest(app, token)) != 1 {
		return "", nil, expires, authority.ErrInvalid
	}
	return parts[1], hash, expires, nil
}
