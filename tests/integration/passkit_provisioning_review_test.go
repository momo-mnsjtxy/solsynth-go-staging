// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/passkitpg"
	"solsynth.local/go-migration/internal/testpasskit"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/passkit"
)

func TestPGPasskitProvisioningAuthorizationAndWaitRevocation(t *testing.T) {
	t.Run("oauth_origin", func(t *testing.T) {
		f := newOAuthFixture(t, false)
		tokens := oauthExchange(t, f)
		proof, e := f.verifier.AuthenticateOAuth(context.Background(), tokens.AccessToken)
		if e != nil {
			t.Fatal("fixture OAuth proof must be valid", e)
		}
		signing := testpasskit.New(t)
		generator, e := passkit.NewGenerator(signing.Config)
		if e != nil {
			t.Fatal(e)
		}
		repo, e := passkitpg.New(f.db, f.transactionGuard, passkitpg.Config{Generator: generator})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = repo.Member(f.transactionGuard.Bind(context.Background(), proof), f.subject); e == nil {
			t.Fatal("valid OAuth proof exchanged for an enduring pass credential")
		}
		service := (&passkit.Service{Repository: repo, Authority: f.verifier}).Handler()
		passCall(t, service, "GET", "/api/accounts/me/passbook/member", "Bearer "+tokens.AccessToken, "", 401)
		if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_apple_passes WHERE account_id=$1`, f.subject); n != 0 {
			t.Fatal("OAuth denial retained a pass token", n)
		}
	})
	t.Run("scope_and_root_origin", func(t *testing.T) {
		f := newPasskitFixture(t)
		parent, e := f.verifier.Authenticate(f.ctx, f.token)
		if e != nil {
			t.Fatal(e)
		}
		var tokens []string
		for _, delegated := range []bool{false, true} {
			scopes := []string{"media.files.read"}
			if delegated {
				// Even the needed scope cannot authorize this enduring credential
				// through a delegated child of the owner's first-party session.
				scopes = []string{passkit.ProvisionPermission}
			}
			pair, e := f.issuer.IssueSession(f.ctx, f.actor, authority.SessionOptions{Label: "Synthetic pass provisioning boundary", Scopes: scopes})
			if e != nil {
				t.Fatal(e)
			}
			if delegated {
				child, e := f.verifier.Authenticate(f.ctx, pair.Token)
				if e != nil {
					t.Fatal(e)
				}
				query(t, f.db, `UPDATE owned_authority_sessions SET parent_id=$1::uuid WHERE id=$2::uuid`, parent.Session.ID, child.Session.ID)
			}
			tokens = append(tokens, pair.Token)
		}
		for phase := range 2 {
			for _, token := range tokens {
				proof, e := f.verifier.Authenticate(context.Background(), token)
				if e != nil {
					t.Fatal("fixture proof must be valid before the provisioning boundary", e)
				}
				if _, e = f.passes.Member(f.guard.Bind(context.Background(), proof), f.actor); !errors.Is(e, authority.ErrForbidden) {
					t.Fatal("scoped/delegated proof provisioned or retrieved a token", e)
				}
				passCall(t, f.app, "GET", "/passport/accounts/me/passbook/member", "Bearer "+token, "", 403)
			}
			if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_apple_passes WHERE account_id=$1`, f.actor); n != phase {
				t.Fatal("denied provisioning changed pass records", n, phase)
			}
			if phase == 0 {
				if _, e := f.passes.Member(f.ctx, f.actor); e != nil {
					t.Fatal("first-party authorized provisioning failed", e)
				}
			}
		}
	})
	for _, wait := range []string{"signing", "media"} {
		t.Run(wait, func(t *testing.T) {
			f := newPasskitFixture(t)
			var now atomic.Int64
			now.Store(f.config.Now().Unix())
			cfg := f.config
			cfg.Now = func() time.Time { return time.Unix(now.Load(), 0).UTC() }
			revokedAt := cfg.Now().Add(5 * time.Second)
			if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.actor, []authority.Rule{{Pattern: "media.files.*"}, {Pattern: passkit.ProvisionPermission}}, []authority.Punishment{{Kind: "permission_modification", Pattern: passkit.ProvisionPermission, StartsAt: &revokedAt}}); e != nil {
				t.Fatal(e)
			}
			verifier, e := authority.NewVerifier(authoritypg.New(f.db), cfg)
			if e != nil {
				t.Fatal(e)
			}
			guard := authoritypg.NewTransactionGuard(authoritypg.New(f.db), verifier)
			proof, e := verifier.Authenticate(context.Background(), f.token)
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(guard.Bind(context.Background(), proof), 10*time.Second)
			defer cancel()
			config := f.signing.Config
			var release, entered func()
			if wait == "signing" {
				blocked := blockingPassSigner{ManifestSigner: config.Signer, entered: make(chan struct{}), release: make(chan struct{})}
				var once sync.Once
				release = func() { once.Do(func() { close(blocked.release) }) }
				entered = func() {
					select {
					case <-blocked.entered:
					case <-ctx.Done():
						t.Fatal("signing wait was not reached", ctx.Err())
					}
				}
				config.Signer = blocked
			} else {
				data := profilePNG(t)
				file, _, e := f.repo.Upload(f.ctx, f.actor, media.Upload{Name: "provision-wait.png", MIMEType: "image/png", Size: int64(len(data)), SHA256: mediaDigest(string(data)), IdempotencyKey: "pass-provision-media-wait"}, bytes.NewReader(data))
				if e != nil {
					t.Fatal(e)
				}
				query(t, f.db, `INSERT INTO owned_account_profile_media(account_id,slot,file_id,attached_at)VALUES($1::uuid,'picture',$2::uuid,clock_timestamp())`, f.actor, file.ID)
				locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
				blocker, pid := atomicBlocker(t, locks, "owned_media_files", file.ID)
				defer blocker.Rollback()
				release = func() {
					if e := blocker.Commit(); e != nil {
						t.Fatal(e)
					}
				}
				entered = func() { waitAtomicBlocked(t, locks, pid, "owned_media_files") }
			}
			if wait == "signing" {
				defer release()
			}
			generator, e := passkit.NewGenerator(config)
			if e != nil {
				t.Fatal(e)
			}
			repo, e := passkitpg.New(f.db, guard, passkitpg.Config{Generator: generator, Media: f.store})
			if e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() { _, e := repo.Member(ctx, f.actor); done <- e }()
			entered()
			now.Add(6) // Current permission revokes while the credential is still valid.
			release()
			if e = <-done; !errors.Is(e, authority.ErrForbidden) {
				t.Fatal("permission revocation during wait published pass secret", e)
			}
			if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_apple_passes WHERE account_id=$1`, f.actor); n != 0 {
				t.Fatal("revoked issuance retained a token record", n)
			}
		})
	}
}
