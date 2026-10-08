// SPDX-License-Identifier: AGPL-3.0-only
package apppg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/passport/board"
	"strings"
	"time"
)

func secretDigest(app, token string) []byte {
	h := sha256.Sum256([]byte("solsynth.owned-app.v1\x00" + app + "\x00" + token))
	return h[:]
}
func secretScan(row scanner) (apps.Secret, error) {
	var s apps.Secret
	var scopes []byte
	e := row.Scan(&s.ID, &s.Description, &scopes, &s.ExpiresAt, &s.CreatedAt, &s.UpdatedAt)
	if e == nil {
		e = json.Unmarshal(scopes, &s.Scopes)
	}
	return s, mapped(e)
}

const secretColumns = `id::text,description,scopes,expires_at,created_at,updated_at`

func (r *Repository) Secrets(ctx context.Context, s apps.Scope, ref string, req *apps.SecretRequest, action string) ([]apps.Secret, error) {
	out := []apps.Secret{}
	if r == nil || r.db == nil {
		return nil, apps.ErrUnavailable
	}
	if ref != "" && !authority.ValidID(ref) {
		return nil, apps.ErrInvalid
	}
	if action == "read" {
		if _, e := scope(ctx, r.db, s, 50, false); e != nil {
			return nil, e
		}
		rows, e := r.db.QueryContext(ctx, `SELECT `+secretColumns+` FROM owned_app_secrets WHERE app_id=$1 AND revoked_at IS NULL AND($2='' OR id::text=$2) ORDER BY created_at DESC,id`, s.App, ref)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		for rows.Next() {
			v, e := secretScan(rows)
			if e != nil {
				return nil, e
			}
			out = append(out, v)
		}
		return out, rows.Err()
	}
	if req != nil && (req.Type != 0 || !apps.ValidText(req.Description, 4096, false)) {
		return nil, apps.ErrProvider
	}
	e := r.write(ctx, s.Actor, "developers.apps.secrets.manage", func(tx *sql.Tx, now time.Time) error {
		if _, e := scope(ctx, tx, s, 50, true); e != nil {
			return e
		}
		a, e := appScan(tx.QueryRowContext(ctx, `SELECT `+appColumns+appJoins+` WHERE a.id=$1 AND `+activeApp, s.App))
		if e != nil {
			return e
		}
		var v apps.Secret
		if action == "create" {
			uid, e := id()
			if e != nil {
				return e
			}
			v = apps.Secret{ID: uid, CreatedAt: now}
		} else {
			v, e = secretScan(tx.QueryRowContext(ctx, `SELECT `+secretColumns+` FROM owned_app_secrets WHERE id=$1 AND app_id=$2 AND revoked_at IS NULL FOR UPDATE`, ref, s.App))
			if e != nil {
				return e
			}
		}
		if action == "delete" {
			_, e = tx.ExecContext(ctx, `UPDATE owned_app_secrets SET revoked_at=$3,updated_at=$3 WHERE id=$1 AND app_id=$2`, v.ID, s.App, now)
		} else {
			if req == nil {
				req = &apps.SecretRequest{}
			}
			ttl, e := apps.Duration(req.ExpiresIn, 24*time.Hour, 30*24*time.Hour)
			if e != nil {
				return e
			}
			scopes := req.Scopes
			if len(scopes) == 0 {
				if action == "rotate" {
					scopes = v.Scopes
				} else {
					scopes = []string{apps.BoardScope}
				}
			}
			if len(scopes) == 0 || len(scopes) > 4 || a.OAuthConfig == nil {
				return apps.ErrForbidden
			}
			seen := map[string]bool{}
			for _, scope := range scopes {
				if !apps.SupportedClientScope(scope) || seen[scope] || !scopeContains(a.OAuthConfig.AllowedScopes, scope) {
					return apps.ErrForbidden
				}
				seen[scope] = true
			}
			b := make([]byte, 32)
			if _, e = rand.Read(b); e != nil {
				return e
			}
			token := "oa1." + v.ID + "." + base64.RawURLEncoding.EncodeToString(b)
			v.Secret = &token
			v.Scopes = scopes
			v.ExpiresAt = now.Add(ttl)
			v.UpdatedAt = now
			if req.Description != nil {
				v.Description = req.Description
			}
			if action == "create" {
				_, e = tx.ExecContext(ctx, `INSERT INTO owned_app_secrets(id,app_id,secret_hash,description,scopes,expires_at,created_at,updated_at)VALUES($1,$2,$3,$4,$5::jsonb,$6,$7,$7)`, v.ID, s.App, secretDigest(s.App, token), v.Description, encoded(v.Scopes), v.ExpiresAt, now)
			} else {
				_, e = tx.ExecContext(ctx, `UPDATE owned_app_secrets SET secret_hash=$3,description=$4,scopes=$5::jsonb,expires_at=$6,updated_at=$7 WHERE id=$1 AND app_id=$2`, v.ID, s.App, secretDigest(s.App, token), v.Description, encoded(v.Scopes), v.ExpiresAt, now)
			}
		}
		if e != nil {
			return e
		}
		out = append(out, v)
		return audit(ctx, tx, s.Actor, s.App, "developers.apps.secrets."+action, v.ID, now)
	})
	return out, e
}
func (r *Repository) Consent(ctx context.Context, actor, slug string, req *apps.ConsentRequest, revoke bool) (apps.Consent, error) {
	var out apps.Consent
	if req == nil && !revoke {
		if r == nil || r.db == nil {
			return out, apps.ErrUnavailable
		}
		a, e := r.PublicApp(ctx, slug)
		if e != nil {
			return out, e
		}
		var raw []byte
		e = r.db.QueryRowContext(ctx, `SELECT app_id::text,scopes,expires_at FROM owned_app_consents WHERE account_id=$1 AND app_id=$2 AND revoked_at IS NULL AND expires_at>statement_timestamp()`, actor, a.ID).Scan(&out.AppID, &raw, &out.ExpiresAt)
		if e == nil {
			e = json.Unmarshal(raw, &out.Scopes)
		}
		return out, mapped(e)
	}
	e := r.write(ctx, actor, apps.BoardScope, func(tx *sql.Tx, now time.Time) error {
		a, e := appScan(tx.QueryRowContext(ctx, `SELECT `+appColumns+appJoins+` WHERE lower(a.slug)=lower($1) AND `+activeApp+` FOR SHARE OF pub,p,a`, slug))
		if e != nil {
			return e
		}
		out.AppID = a.ID
		if revoke {
			_, e = tx.ExecContext(ctx, `UPDATE owned_app_consents SET revoked_at=$3,updated_at=$3 WHERE account_id=$1 AND app_id=$2 AND revoked_at IS NULL`, actor, a.ID, now)
		} else {
			if !boardCapable(a) || len(req.Scopes) != 1 || req.Scopes[0] != apps.BoardScope {
				return apps.ErrForbidden
			}
			ttl, e := apps.Duration(req.ExpiresIn, 24*time.Hour, 30*24*time.Hour)
			if e != nil {
				return e
			}
			out.Scopes = req.Scopes
			out.ExpiresAt = now.Add(ttl)
			_, e = tx.ExecContext(ctx, `INSERT INTO owned_app_consents(account_id,app_id,scopes,expires_at,created_at,updated_at)VALUES($1,$2,$3::jsonb,$4,$5,$5) ON CONFLICT(account_id,app_id)DO UPDATE SET scopes=EXCLUDED.scopes,expires_at=EXCLUDED.expires_at,updated_at=EXCLUDED.updated_at,revoked_at=NULL`, actor, a.ID, encoded(out.Scopes), out.ExpiresAt, now)
		}
		if e != nil {
			return e
		}
		action := "developers.apps.consent.grant"
		if revoke {
			action = "developers.apps.consent.revoke"
		}
		return audit(ctx, tx, actor, a.ID, action, a.ID, now)
	})
	return out, e
}
func authenticateApp(ctx context.Context, tx *sql.Tx, app, token string) error {
	return authenticateClient(ctx, tx, app, token, apps.BoardScope)
}
func authenticateClient(ctx context.Context, tx *sql.Tx, app, token, permission string) error {
	if !authority.ValidID(app) || len(token) > 256 {
		return authority.ErrInvalid
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "oa1" || !authority.ValidID(parts[1]) {
		return authority.ErrInvalid
	}
	raw, e := base64.RawURLEncoding.DecodeString(parts[2])
	if e != nil || len(raw) != 32 {
		return authority.ErrInvalid
	}
	a, e := activeClientByID(ctx, tx, app, true)
	if e != nil || !apps.SupportedClientScope(permission) || a.OAuthConfig == nil || !scopeContains(a.OAuthConfig.AllowedScopes, permission) {
		return authority.ErrInvalid
	}
	var stored, scopes []byte
	var active bool
	e = tx.QueryRowContext(ctx, `SELECT secret_hash,scopes,expires_at>statement_timestamp() AND revoked_at IS NULL FROM owned_app_secrets WHERE id=$1 AND app_id=$2 FOR SHARE`, parts[1], app).Scan(&stored, &scopes, &active)
	if e != nil {
		return authority.ErrInvalid
	}
	// statement_timestamp() in a locking SELECT predates any lock wait. Read
	// the clock again after the lock is owned before authorizing the operation.
	if e = tx.QueryRowContext(ctx, `SELECT expires_at>clock_timestamp() AND revoked_at IS NULL FROM owned_app_secrets WHERE id=$1 AND app_id=$2`, parts[1], app).Scan(&active); e != nil {
		return authority.ErrInvalid
	}
	var declared []string
	if json.Unmarshal(scopes, &declared) != nil || !active || !scopeContains(declared, permission) || subtle.ConstantTimeCompare(stored, secretDigest(app, token)) != 1 {
		return authority.ErrInvalid
	}
	return nil
}
func (r *Repository) AppWidgets(ctx context.Context, app, token string) ([]board.Manifest, error) {
	if r == nil || r.db == nil {
		return nil, apps.ErrUnavailable
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if e = authenticateApp(ctx, tx, app, token); e != nil {
		return nil, e
	}
	out, e := widgets(ctx, tx, app, "")
	if e != nil {
		return nil, e
	}
	return out, tx.Commit()
}

// PushPayload holds account, app, secret and consent locks through the canonical
// board update; revocation/rotation cannot commit between verification and write.
func (r *Repository) PushPayload(ctx context.Context, app, token string, req apps.PayloadRequest) (board.Item, error) {
	var out board.Item
	if r == nil || r.db == nil {
		return out, apps.ErrUnavailable
	}
	if !authority.ValidID(req.AccountID) || !apps.ValidKey(req.WidgetKey) || req.BoardItemID != nil && !authority.ValidID(*req.BoardItemID) {
		return out, apps.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	var disabled bool
	if e = tx.QueryRowContext(ctx, `SELECT disabled FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, req.AccountID).Scan(&disabled); e != nil || disabled {
		return out, apps.ErrForbidden
	}
	if e = authenticateApp(ctx, tx, app, token); e != nil {
		return out, e
	}
	var raw []byte
	var active bool
	if e = tx.QueryRowContext(ctx, `SELECT scopes,expires_at>statement_timestamp() AND revoked_at IS NULL FROM owned_app_consents WHERE account_id=$1 AND app_id=$2 FOR UPDATE`, req.AccountID, app).Scan(&raw, &active); e != nil {
		return out, apps.ErrForbidden
	}
	var scopes []string
	if json.Unmarshal(raw, &scopes) != nil || !active || !scopeContains(scopes, apps.BoardScope) {
		return out, apps.ErrForbidden
	}
	ws, e := widgets(ctx, tx, app, req.WidgetKey)
	if e != nil {
		return out, e
	}
	if len(ws) != 1 {
		return out, apps.ErrNotFound
	}
	payload, e := board.ValidateManifestPayload(ctx, ws[0], req.Payload, board.DocumentedEnvelope{})
	if e != nil {
		return out, e
	}
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('solsynth.board:'||$1::text,0))`, req.AccountID); e != nil {
		return out, e
	}
	itemID := ""
	if req.BoardItemID != nil {
		itemID = *req.BoardItemID
	}
	rows, e := tx.QueryContext(ctx, `SELECT id::text,account_id::text,"order",kind,widget_key,custom_app_id::text,custom_app_widget_key,is_enabled,payload FROM account_board_items WHERE account_id=$1 AND custom_app_id=$2 AND lower(custom_app_widget_key)=lower($3) AND kind=1 AND deleted_at IS NULL AND($4='' OR id::text=$4) ORDER BY "order",id LIMIT 2 FOR UPDATE`, req.AccountID, app, ws[0].Key, itemID)
	if e != nil {
		return out, e
	}
	count := 0
	for rows.Next() {
		count++
		var data []byte
		if e = rows.Scan(&out.ID, &out.AccountID, &out.Order, &out.Kind, &out.WidgetKey, &out.CustomAppID, &out.CustomAppWidgetKey, &out.IsEnabled, &data); e != nil {
			rows.Close()
			return out, e
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if count == 0 {
		return out, apps.ErrNotFound
	}
	if count > 1 {
		return out, apps.ErrConflict
	}
	if !out.IsEnabled {
		return out, apps.ErrForbidden
	}
	// Expiry is wall-clock based: a board-lock wait must not extend a token or consent.
	if e = authenticateApp(ctx, tx, app, token); e != nil {
		return out, e
	}
	if e = tx.QueryRowContext(ctx, `SELECT expires_at>statement_timestamp() AND revoked_at IS NULL FROM owned_app_consents WHERE account_id=$1 AND app_id=$2`, req.AccountID, app).Scan(&active); e != nil || !active {
		return out, apps.ErrForbidden
	}
	out.Payload = payload
	var now time.Time
	if e = tx.QueryRowContext(ctx, `SELECT statement_timestamp()`).Scan(&now); e != nil {
		return out, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE account_board_items SET payload=$3::jsonb,updated_at=$4 WHERE id=$1 AND account_id=$2`, out.ID, req.AccountID, encoded(payload), now); e != nil {
		return out, e
	}
	if e = audit(ctx, tx, req.AccountID, app, "developers.apps.board.payload", out.ID, now); e != nil {
		return out, e
	}
	if e = tx.Commit(); e != nil {
		return out, e
	}
	if r.cache != nil {
		e = r.cache.InvalidateGroup(ctx, "account:"+req.AccountID)
	}
	return out, e
}
