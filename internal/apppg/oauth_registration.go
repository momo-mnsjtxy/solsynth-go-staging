// SPDX-License-Identifier: AGPL-3.0-only
package apppg

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"solsynth.local/go-migration/ports/authority/oauth"
	"solsynth.local/go-migration/ports/develop/apps"
	"strings"
	"time"
)

// RegisterOAuth creates both client and initial confidential credential in one
// canonical owner transaction. A failure returns neither identifier nor secret.
func (r *Repository) RegisterOAuth(ctx context.Context, s apps.Scope, req oauth.Registration, scopes []string) (out oauth.RegistrationResult, e error) {
	e = r.write(ctx, s.Actor, "developers.apps.create", func(tx *sql.Tx, now time.Time) error {
		d, e := scope(ctx, tx, s, 50, true)
		if e != nil {
			return e
		}
		var owner string
		e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_publishers WHERE id=$1`, d.PublisherID).Scan(&owner)
		if e != nil {
			return e
		}
		appID, e := id()
		if e != nil {
			return e
		}
		slug := "oauth-" + appID
		c := apps.OAuthConfig{ClientURI: req.ClientURI, RedirectURIs: req.RedirectURIs, AllowedScopes: append([]string{}, scopes...), AllowedGrantTypes: req.GrantTypes, RequirePKCE: true, AllowOfflineAccess: oauth.Contains(scopes, "offline_access"), IsPublicClient: req.TokenEndpointAuthMethod == "none"}
		if !c.IsPublicClient {
			c.AllowedScopes = append(c.AllowedScopes, apps.OAuthClientAuthScope)
			if _, e = r.guard.Authorize(ctx, r.db, tx, s.Actor, "developers.apps.secrets.manage"); e != nil {
				return e
			}
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_custom_apps(id,project_id,slug,name,status,oauth_config,created_at,updated_at)VALUES($1,$2,$3,$4,1,$5::jsonb,$6,$6)`, appID, s.Project, slug, req.ClientName, encoded(c), now)
		if e != nil {
			return e
		}
		s.App = appID
		out = oauth.RegistrationResult{ClientID: slug, ClientIDIssuedAt: now.Unix(), ClientName: req.ClientName, ClientURI: req.ClientURI, RedirectURIs: req.RedirectURIs, GrantTypes: req.GrantTypes, ResponseTypes: req.ResponseTypes, Scope: strings.Join(scopes, " "), TokenEndpointAuthMethod: req.TokenEndpointAuthMethod}
		if !c.IsPublicClient {
			secretID, e := id()
			if e != nil {
				return e
			}
			raw := make([]byte, 32)
			if _, e = rand.Read(raw); e != nil {
				return e
			}
			token := "oa1." + secretID + "." + base64.RawURLEncoding.EncodeToString(raw)
			expiry := now.Add(24 * time.Hour)
			_, e = tx.ExecContext(ctx, `INSERT INTO owned_app_secrets(id,app_id,secret_hash,scopes,expires_at,created_at,updated_at)VALUES($1,$2,$3,$4::jsonb,$5,$6,$6)`, secretID, appID, secretDigest(appID, token), encoded([]string{apps.OAuthClientAuthScope}), expiry, now)
			if e != nil {
				return e
			}
			out.ClientSecret = &token
			out.ClientSecretExpiresAt = expiry.Unix()
			if _, e = r.guard.Authorize(ctx, r.db, tx, s.Actor, "developers.apps.secrets.manage"); e != nil {
				return e
			}
		}
		if e = audit(ctx, tx, s.Actor, appID, "oauth.client.register", appID, now); e != nil {
			return e
		}
		if !c.IsPublicClient {
			if _, e = r.guard.Authorize(ctx, r.db, tx, s.Actor, "developers.apps.secrets.manage"); e != nil {
				return e
			}
		}
		if _, e = scope(ctx, tx, s, 50, true); e != nil {
			return e
		}
		freshOwner, e := secretIssuerOwner(ctx, tx, s, d)
		if e != nil {
			return e
		}
		if owner != freshOwner {
			return apps.ErrForbidden
		}
		return nil
	})
	if e != nil {
		out = oauth.RegistrationResult{}
	}
	return out, e
}
