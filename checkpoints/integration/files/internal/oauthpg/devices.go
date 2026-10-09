// SPDX-License-Identifier: AGPL-3.0-only
package oauthpg

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"encoding/json"
	"errors"
	"net/url"
	"solsynth.local/go-migration/ports/authority/oauth"
	"solsynth.local/go-migration/ports/develop/apps"
	"strings"
	"time"
)

func deviceScopes(raw string, a apps.App) ([]string, error) {
	scopes, e := oauth.Scopes(raw)
	if e != nil {
		return nil, e
	}
	c := a.OAuthConfig
	if c == nil || !oauth.Contains(c.AllowedGrantTypes, oauth.DeviceGrant) {
		return nil, oauth.ErrClient
	}
	for _, s := range scopes {
		if !oauth.Contains(c.AllowedScopes, s) {
			return nil, oauth.ErrScope
		}
	}
	if oauth.Contains(scopes, "offline_access") && (!c.AllowOfflineAccess || !oauth.Contains(c.AllowedGrantTypes, "refresh_token")) {
		return nil, oauth.ErrScope
	}
	return scopes, nil
}
func normalizeUserCode(s string) (string, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "-", "")
	if len(s) != 10 {
		return "", oauth.ErrRequest
	}
	for _, c := range s {
		if !(c >= 'A' && c <= 'Z' || c >= '2' && c <= '7') {
			return "", oauth.ErrRequest
		}
	}
	return s, nil
}
func (r *Repository) DeviceAuthorize(ctx context.Context, client, secret, rawScopes string) (out oauth.DeviceAuthorization, e error) {
	if r == nil {
		return out, oauth.ErrUnavailable
	}
	if e = r.preflightClient(ctx, client, secret); e != nil {
		return out, e
	}
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	a, epoch, owner, e := r.apps.OAuthRegistration(ctx, r.db, tx, client, true)
	if e != nil {
		return out, oauth.ErrClient
	}
	b := &boundary{tx: tx, app: a}
	credential, e := r.client(ctx, b, secret)
	if e != nil {
		return out, e
	}
	scopes, e := deviceScopes(rawScopes, a)
	if e != nil {
		return out, e
	}
	var now time.Time
	var ownerVersion int64
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return out, e
	}
	if owner != "" {
		if e = tx.QueryRowContext(ctx, `SELECT version FROM owned_authority_accounts WHERE id=$1`, owner).Scan(&ownerVersion); e != nil {
			return out, e
		}
	}
	// Public device clients still have bounded per-client state and issuance rate.
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_oauth_devices WHERE app_id=$1 AND (expires_at>$2 OR created_at>$2-interval '1 minute')`, a.ID, now).Scan(&count); e != nil {
		return out, e
	}
	if count >= 64 {
		return out, oauth.ErrRequest
	}
	raw, e := random()
	if e != nil {
		return out, e
	}
	user := make([]byte, 6)
	if _, e = rand.Read(user); e != nil {
		return out, e
	}
	code := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(user)
	exp := now.Add(10 * time.Minute)
	if credential.id != "" && credential.expires.Before(exp) {
		exp = credential.expires
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_oauth_devices(hash,user_hash,app_id,client_id,app_epoch,owner_id,owner_version,scopes,secret_id,secret_hash,created_at,expires_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, digest("device", raw), digest("device-user", code), a.ID, client, epoch, nullable(owner), ownerVersion, rawJSON(scopes), nullable(credential.id), credential.hash, now, exp)
	if e != nil {
		return out, e
	}
	if _, _, _, e = r.apps.OAuthRegistration(ctx, r.db, tx, client, false); e != nil {
		return out, e
	}
	if _, e = r.client(ctx, b, secret); e != nil {
		return out, e
	}
	if e = tx.Commit(); e != nil {
		return out, e
	}
	verify := strings.TrimSuffix(r.issuer.OAuthIssuer(), "/") + "/auth/open/device/verify"
	display := code[:5] + "-" + code[5:]
	out = oauth.DeviceAuthorization{DeviceCode: raw, UserCode: display, VerificationURI: verify, VerificationURIComplete: verify + "?user_code=" + url.QueryEscape(display), ExpiresIn: int64(exp.Sub(now) / time.Second), Interval: 5}
	return out, nil
}
func (r *Repository) deviceClient(ctx context.Context, code string) (string, []byte, error) {
	normalized, e := normalizeUserCode(code)
	if e != nil {
		return "", nil, e
	}
	hash := digest("device-user", normalized)
	var client string
	e = r.db.QueryRowContext(ctx, `SELECT client_id FROM owned_oauth_devices WHERE user_hash=$1`, hash).Scan(&client)
	if errors.Is(e, sql.ErrNoRows) {
		return "", nil, oauth.ErrGrant
	}
	return client, hash, e
}
func (r *Repository) DevicePreview(ctx context.Context, actor, code string) (out oauth.ClientInfo, e error) {
	client, hash, e := r.deviceClient(ctx, code)
	if e != nil {
		return out, e
	}
	e = r.within(ctx, actor, client, true, func(b *boundary) error {
		var scopesRaw []byte
		var epoch, version int64
		var owner sql.NullString
		var decision *bool
		e := b.tx.QueryRowContext(ctx, `SELECT scopes,expires_at,app_epoch,owner_id::text,owner_version,decision FROM owned_oauth_devices WHERE user_hash=$1 FOR UPDATE`, hash).Scan(&scopesRaw, &out.ExpiresAt, &epoch, &owner, &version, &decision)
		if e != nil {
			return e
		}
		if e = b.tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&b.now); e != nil {
			return e
		}
		b.final = func() error {
			if !b.now.Before(out.ExpiresAt) {
				return oauth.ErrGrant
			}
			return nil
		}
		if decision != nil || !b.now.Before(out.ExpiresAt) || epoch != b.epoch || owner.String != b.owner || version != b.ownerVersion {
			return oauth.ErrGrant
		}
		var scopes []string
		if json.Unmarshal(scopesRaw, &scopes) != nil {
			return oauth.ErrGrant
		}
		if _, e = deviceScopes(strings.Join(scopes, " "), b.app); e != nil {
			return e
		}
		out.ClientID = client
		out.ClientName = b.app.Name
		out.Description = b.app.Description
		out.HomeURI = b.app.OAuthConfig.ClientURI
		out.Scopes = scopes
		_, e = b.tx.ExecContext(ctx, `UPDATE owned_oauth_devices SET preview_account_id=$2,preview_parent_id=$3,previewed_at=$4 WHERE user_hash=$1`, hash, actor, b.proof.Session.ID, b.now)
		return e
	})
	return out, e
}
func (r *Repository) DeviceDecide(ctx context.Context, actor, code string, approve bool) error {
	client, hash, e := r.deviceClient(ctx, code)
	if e != nil {
		return e
	}
	return r.within(ctx, actor, client, true, func(b *boundary) error {
		var scopesRaw []byte
		var epoch, ownerVersion int64
		var owner sql.NullString
		var exp time.Time
		var decision *bool
		var reviewed bool
		e := b.tx.QueryRowContext(ctx, `SELECT scopes,expires_at,app_epoch,owner_id::text,owner_version,decision FROM owned_oauth_devices WHERE user_hash=$1 FOR UPDATE`, hash).Scan(&scopesRaw, &exp, &epoch, &owner, &ownerVersion, &decision)
		if e != nil {
			return e
		}
		if e = b.tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&b.now); e != nil {
			return e
		}
		b.final = func() error {
			if !b.now.Before(exp) {
				return oauth.ErrGrant
			}
			var fresh bool
			e := b.tx.QueryRowContext(ctx, `SELECT COALESCE(preview_account_id=$2 AND preview_parent_id=$3 AND previewed_at>$4-interval '5 minutes',false)FROM owned_oauth_devices WHERE user_hash=$1`, hash, actor, b.proof.Session.ID, b.now).Scan(&fresh)
			if e != nil {
				return e
			}
			if !fresh {
				return oauth.ErrGrant
			}
			return nil
		}
		if decision != nil || !b.now.Before(exp) || epoch != b.epoch || owner.String != b.owner || ownerVersion != b.ownerVersion {
			return oauth.ErrGrant
		}
		if e = b.tx.QueryRowContext(ctx, `SELECT COALESCE(preview_account_id=$2 AND preview_parent_id=$3 AND previewed_at>$4-interval '5 minutes',false) FROM owned_oauth_devices WHERE user_hash=$1`, hash, actor, b.proof.Session.ID, b.now).Scan(&reviewed); e != nil {
			return e
		}
		if !reviewed {
			return oauth.ErrGrant
		}
		var scopes []string
		if json.Unmarshal(scopesRaw, &scopes) != nil {
			return oauth.ErrGrant
		}
		if _, e = deviceScopes(strings.Join(scopes, " "), b.app); e != nil {
			return e
		}
		var version int64
		if approve {
			// A reviewed decision replaces the same client's consent atomically. Older
			// grants bind its version and cannot keep permissions after a scope change.
			e = b.tx.QueryRowContext(ctx, `INSERT INTO owned_app_consents(account_id,app_id,scopes,expires_at,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$5)ON CONFLICT(account_id,app_id)DO UPDATE SET scopes=EXCLUDED.scopes,expires_at=EXCLUDED.expires_at,updated_at=EXCLUDED.updated_at,revoked_at=NULL,version=owned_app_consents.version+1 RETURNING version`, actor, b.app.ID, scopesRaw, b.now.Add(24*time.Hour), b.now).Scan(&version)
			if e != nil {
				return e
			}
		}
		_, e = b.tx.ExecContext(ctx, `UPDATE owned_oauth_devices SET account_id=$2,parent_id=$3,consent_version=$4,decision=$5,decided_at=$6 WHERE user_hash=$1`, hash, actor, b.proof.Session.ID, version, approve, b.now)
		if e != nil {
			return e
		}
		return audit(ctx, b.tx, actor, b.app.ID, "device.decided", b.now)
	})
}
func (r *Repository) deviceToken(ctx context.Context, req oauth.TokenRequest) (out oauth.Tokens, e error) {
	hash := digest("device", req.DeviceCode)
	// A client-authenticated poll uses the same complete owner -> ancestry ->
	// secret -> device lock order as redemption. No status or poll-state mutation
	// can survive credential expiry during a device-row wait.
	lookup, e := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if e != nil {
		return out, e
	}
	_, _, pollOwner, e := r.apps.OAuthRegistration(ctx, r.db, lookup, req.ClientID, false)
	lookup.Rollback()
	if e != nil {
		return out, oauth.ErrClient
	}
	var actor, parent string
	client := req.ClientID
	var exp time.Time
	var outcome error
	e = r.within(ctx, pollOwner, client, false, func(b *boundary) error {
		if b.owner != pollOwner {
			return oauth.ErrClient
		}
		credential, e := r.client(ctx, b, req.ClientSecret)
		if e != nil {
			return e
		}
		var account, parentID, owner, secretID sql.NullString
		var last, used *time.Time
		var interval int
		var decision *bool
		var epoch, ownerVersion int64
		var storedHash []byte
		e = b.tx.QueryRowContext(ctx, `SELECT account_id::text,parent_id::text,expires_at,last_polled_at,interval_seconds,decision,used_at,app_epoch,owner_id::text,owner_version,secret_id::text,secret_hash FROM owned_oauth_devices WHERE hash=$1 AND app_id=$2 AND client_id=$3 FOR UPDATE`, hash, b.app.ID, client).Scan(&account, &parentID, &exp, &last, &interval, &decision, &used, &epoch, &owner, &ownerVersion, &secretID, &storedHash)
		if errors.Is(e, sql.ErrNoRows) {
			return oauth.ErrGrant
		}
		if e != nil {
			return e
		}
		if e = b.tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&b.now); e != nil {
			return e
		}
		if epoch != b.epoch || owner.String != b.owner || ownerVersion != b.ownerVersion || secretID.String != credential.id || !equal(storedHash, credential.hash) {
			return oauth.ErrClient
		}
		b.final = func() error {
			fresh, e := r.client(ctx, b, req.ClientSecret)
			if e != nil {
				return e
			}
			if fresh.id != credential.id || !equal(fresh.hash, credential.hash) {
				return oauth.ErrClient
			}
			return nil
		}
		if !b.now.Before(exp) {
			outcome = oauth.DeviceFailure("expired_token")
			return nil
		}
		if used != nil {
			return oauth.ErrGrant
		}
		if last != nil && b.now.Before(last.Add(time.Duration(interval)*time.Second)) {
			_, e = b.tx.ExecContext(ctx, `UPDATE owned_oauth_devices SET interval_seconds=LEAST(interval_seconds+5,3600),last_polled_at=$2 WHERE hash=$1`, hash, b.now)
			outcome = oauth.DeviceFailure("slow_down")
			return e
		}
		if _, e = b.tx.ExecContext(ctx, `UPDATE owned_oauth_devices SET last_polled_at=$2 WHERE hash=$1`, hash, b.now); e != nil {
			return e
		}
		if decision == nil {
			outcome = oauth.DeviceFailure("authorization_pending")
			return nil
		}
		if !*decision {
			outcome = oauth.DeviceFailure("access_denied")
			return nil
		}
		actor, parent = account.String, parentID.String
		return nil
	})
	if e != nil {
		return out, e
	}
	if outcome != nil {
		return out, outcome
	}
	e = r.within(ctx, actor, client, false, func(b *boundary) error {
		credential, e := r.client(ctx, b, req.ClientSecret)
		if e != nil {
			return e
		}
		var scopesRaw, storedHash []byte
		var epoch, ownerVersion, version int64
		var owner, secretID sql.NullString
		var allowed bool
		e = b.tx.QueryRowContext(ctx, `SELECT scopes,app_epoch,owner_id::text,owner_version,consent_version,secret_id::text,secret_hash,expires_at,decision AND used_at IS NULL AND expires_at>$2 FROM owned_oauth_devices WHERE hash=$1 AND account_id=$3 AND parent_id=$4 FOR UPDATE`, hash, b.now, actor, parent).Scan(&scopesRaw, &epoch, &owner, &ownerVersion, &version, &secretID, &storedHash, &exp, &allowed)
		if e != nil {
			return e
		}
		if !allowed || epoch != b.epoch || owner.String != b.owner || ownerVersion != b.ownerVersion {
			return oauth.ErrGrant
		}
		if secretID.String != credential.id || !equal(storedHash, credential.hash) {
			return oauth.ErrClient
		}
		var scopes []string
		if json.Unmarshal(scopesRaw, &scopes) != nil {
			return oauth.ErrGrant
		}
		if _, e = deviceScopes(strings.Join(scopes, " "), b.app); e != nil {
			return e
		}
		var consentExp time.Time
		var active bool
		e = b.tx.QueryRowContext(ctx, `SELECT expires_at,revoked_at IS NULL AND expires_at>$4 AND version=$3 AND scopes @> $5::jsonb FROM owned_app_consents WHERE account_id=$1 AND app_id=$2 FOR UPDATE`, actor, b.app.ID, version, b.now, scopesRaw).Scan(&consentExp, &active)
		if e != nil {
			return e
		}
		if !active {
			return oauth.ErrGrant
		}
		if credential.id != "" && credential.expires.Before(consentExp) {
			consentExp = credential.expires
		}
		var session string
		out, session, e = r.issueGrant(ctx, b, actor, parent, scopes, consentExp, credential, "", version)
		if e != nil {
			return e
		}
		grantFinal := b.final
		b.final = func() error {
			if !b.now.Before(exp) {
				return oauth.ErrGrant
			}
			if _, e := r.client(ctx, b, req.ClientSecret); e != nil {
				return e
			}
			return grantFinal()
		}
		if _, e = b.tx.ExecContext(ctx, `UPDATE owned_oauth_devices SET used_at=$2,session_id=$3 WHERE hash=$1`, hash, b.now, session); e != nil {
			return e
		}
		return audit(ctx, b.tx, actor, b.app.ID, "device.exchanged", b.now)
	})
	if e != nil {
		out = oauth.Tokens{}
	}
	return out, e
}
