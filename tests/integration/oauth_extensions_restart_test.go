// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/apppg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/oauthpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/oauth"
)

func TestPGOAuthDeviceAndIntrospectionSavedRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	root := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "oauth-device-extension-keys")
	type state struct {
		Actor, Client, App, Secret, Access, Used, Pending, Denied string
		Parent                                                    string
		Clock                                                     int64
		CapturedWall                                              time.Time
	}
	var saved state
	if phase == "before" {
		f := newOAuthFixture(t, true)
		enableDevice(t, &f)
		cfg := authorityConfig(t, f.keyDir, f.clock)
		cfg.AccessTTL, cfg.SessionTTL = time.Hour, time.Hour
		initial, wall := cfg.Now().UTC(), time.Now()
		cfg.Now = func() time.Time { return initial.Add(time.Since(wall)) }
		var e error
		f.issuer, e = authority.New(authoritypg.New(db), cfg)
		if e != nil {
			t.Fatal(e)
		}
		f.verifier, e = authority.NewVerifier(authoritypg.New(db), cfg)
		if e != nil {
			t.Fatal(e)
		}
		f.transactionGuard = authoritypg.NewTransactionGuard(authoritypg.New(db), f.verifier)
		f.repo, e = oauthpg.NewWithAuthority(db, authoritypg.New(db), f.issuer, f.transactionGuard, apppg.NewWithAuthority(db, f.transactionGuard))
		if e != nil {
			t.Fatal(e)
		}
		parent := pgIssue(t, f.issuer, f.subject)
		p, e := f.verifier.Authenticate(f.ctx, parent.Token)
		if e != nil {
			t.Fatal(e)
		}
		f.ctx = f.transactionGuard.Bind(f.ctx, p)
		saved = state{Actor: f.subject, Client: f.app.Slug, App: f.app.ID, Secret: f.secret, Parent: p.Session.ID, Clock: initial.Unix(), CapturedWall: wall}
		for _, kind := range []string{"used", "pending", "denied"} {
			d, e := f.repo.DeviceAuthorize(f.ctx, f.app.Slug, f.secret, "openid profile")
			if e != nil {
				t.Fatal(e)
			}
			if kind != "pending" {
				if _, e = f.repo.DevicePreview(f.ctx, f.subject, d.UserCode); e != nil {
					t.Fatal(e)
				}
				if e = f.repo.DeviceDecide(f.ctx, f.subject, d.UserCode, kind == "used"); e != nil {
					t.Fatal(e)
				}
			}
			switch kind {
			case "used":
				tokens, e := f.repo.Token(f.ctx, oauth.TokenRequest{GrantType: oauth.DeviceGrant, ClientID: saved.Client, ClientSecret: saved.Secret, DeviceCode: d.DeviceCode})
				if e != nil {
					t.Fatal(e)
				}
				saved.Used, saved.Access = d.DeviceCode, tokens.AccessToken
			case "pending":
				saved.Pending = d.DeviceCode
			case "denied":
				saved.Denied = d.DeviceCode
			}
		}
		if e = os.Mkdir(root, 0700); e != nil {
			t.Fatal(e)
		}
		// Synthetic private service keys remain only in this exclusive CI directory.
		// After restart no credentials are reissued and no new grant is minted.
		for _, name := range []string{"synthetic-signing.pem", "synthetic-hash.bin", "synthetic-seal.bin"} {
			raw, e := os.ReadFile(filepath.Join(f.keyDir, name))
			if e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(filepath.Join(root, name), raw, 0600); e != nil {
				t.Fatal(e)
			}
		}
		saveRestartFixture(t, "oauth-device-extensions.json", saved)
	} else {
		loadRestartFixture(t, "oauth-device-extensions.json", &saved)
	}
	clock := &atomic.Int64{}
	clock.Store(saved.Clock)
	cfg := authorityConfig(t, root, clock)
	cfg.AccessTTL, cfg.SessionTTL = time.Hour, time.Hour
	cfg.Now = func() time.Time { return time.Unix(saved.Clock, 0).Add(time.Since(saved.CapturedWall)) }
	store := authoritypg.New(db)
	issuer, e := authority.New(store, cfg)
	if e != nil {
		t.Fatal(e)
	}
	v, e := authority.NewVerifier(store, cfg)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(store, v)
	repo, e := oauthpg.NewWithAuthority(db, store, issuer, guard, apppg.NewWithAuthority(db, guard))
	if e != nil {
		t.Fatal(e)
	}
	active, e := repo.Introspect(context.Background(), saved.Client, saved.Secret, saved.Access)
	if e != nil || active["active"] != true || active["sub"] != saved.Actor {
		t.Fatal("original device grant introspection", active, e)
	}
	var grants int
	if e = db.QueryRow(`SELECT count(*) FROM owned_oauth_grants WHERE account_id=$1 AND app_id=$2`, saved.Actor, saved.App).Scan(&grants); e != nil || grants != 1 {
		t.Fatal("original one-use grant count", grants, e)
	}
	if phase != "after" {
		return
	}
	for _, item := range []struct{ code, outcome string }{{saved.Pending, "authorization_pending"}, {saved.Denied, "access_denied"}, {saved.Used, "invalid_grant"}} {
		var now, expiry time.Time
		if e = db.QueryRow(`SELECT clock_timestamp(),expires_at FROM owned_oauth_devices WHERE hash=$1`, oauthDeviceHash(item.code)).Scan(&now, &expiry); e != nil {
			t.Fatal(e)
		}
		if !now.Add(time.Second).Before(expiry) {
			organizationWaitExpired(t, context.Background(), db, expiry)
			item.outcome = "expired_token"
		}
		_, e = repo.Token(context.Background(), oauth.TokenRequest{GrantType: oauth.DeviceGrant, ClientID: saved.Client, ClientSecret: saved.Secret, DeviceCode: item.code})
		if item.outcome == "invalid_grant" {
			if !errors.Is(e, oauth.ErrGrant) {
				t.Fatal("consumed device replay after restart", e)
			}
		} else {
			var failure *oauth.Failure
			if !errors.As(e, &failure) || failure.Code != item.outcome {
				t.Fatal("durable device state after restart", item.outcome, e)
			}
		}
	}
	query(t, db, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE id=$1`, saved.Parent)
	inactive, e := repo.Introspect(context.Background(), saved.Client, saved.Secret, saved.Access)
	if e != nil || inactive["active"] != false || len(inactive) != 1 {
		t.Fatal("restart parent revocation did not deactivate introspection", inactive, e)
	}
	if e = db.QueryRow(`SELECT count(*) FROM owned_oauth_grants WHERE account_id=$1 AND app_id=$2`, saved.Actor, saved.App).Scan(&grants); e != nil || grants != 1 {
		t.Fatal("restart replay minted another grant", grants, e)
	}
}
