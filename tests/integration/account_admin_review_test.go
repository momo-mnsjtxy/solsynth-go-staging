// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/adminpg"
	"solsynth.local/go-migration/internal/authoritypg"
	a "solsynth.local/go-migration/ports/admin/accounts"
	c "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
)

func TestPGAccountAdminRootSessionsAndSingleConnection(t *testing.T) {
	f := newAccountAdminFixture(t)
	parent, e := f.issuer.IssueSession(context.Background(), f.actor, authority.SessionOptions{Label: "synthetic parent", Scopes: []string{"*"}})
	if e != nil {
		t.Fatal(e)
	}
	parentProof, e := f.issuer.Authenticate(context.Background(), parent.Token)
	if e != nil {
		t.Fatal(e)
	}
	// Exercise an existing canonical Type-0 child, including a revoked ancestor.
	// No production credential is imported or issued by this fixture.
	query(t, f.db, `UPDATE owned_authority_sessions SET parent_id=$2 WHERE id=$1`, f.proof.Session.ID, parentProof.Session.ID)
	query(t, f.db, `UPDATE owned_authority_sessions SET revoked_at=$2 WHERE id=$1`, parentProof.Session.ID, f.config.Now())
	for _, ref := range []string{f.other, authorityUUID(t)} {
		for _, resource := range []string{"contacts", "factors", "badges"} {
			backendCall(t, f.app, "GET", "/passport/admin/accounts/"+ref+"/"+resource, f.token, "", 403)
		}
		backendJSONCall(t, f.app, "POST", "/api/admin/accounts/"+ref+"/badges", f.token, administrativeBadge("child-denied"), 403)
		backendCall(t, f.app, "POST", "/api/admin/accounts/"+ref+"/badges/"+authorityUUID(t)+"/activate", f.token, "", 403)
		backendCall(t, f.app, "DELETE", "/api/admin/accounts/"+ref+"/badges/"+authorityUUID(t), f.token, "", 403)
	}
	backendCall(t, f.app, "GET", "/passport/admin/accounts/metrics/activity", f.token, "", 403)
	if _, e = f.repo.AccountFactors(f.ctx, f.actor, f.other); !errors.Is(e, c.ErrForbidden) {
		t.Fatal("original bound child accepted", e)
	}
	root, e := f.issuer.IssueSession(context.Background(), f.actor, authority.SessionOptions{Label: "synthetic root", Scopes: []string{"*"}})
	if e != nil {
		t.Fatal(e)
	}
	f.db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	verifier, e := authority.NewVerifier(authoritypg.New(f.db), f.config)
	if e != nil {
		t.Fatal(e)
	}
	proof, e := verifier.Authenticate(ctx, root.Token)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(f.db), verifier)
	repo, e := adminpg.New(f.db, guard, f.config.Now)
	if e != nil {
		t.Fatal(e)
	}
	ctx = guard.Bind(ctx, proof)
	if _, e = repo.AccountContacts(ctx, f.actor, f.other); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.AccountFactors(ctx, f.actor, f.other); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.AccountActivityMetrics(ctx, f.actor); e != nil {
		t.Fatal(e)
	}
	badge, e := repo.GrantAccountBadge(ctx, f.actor, f.other, administrativeBadge("single-connection"))
	if e != nil {
		t.Fatal(e)
	}
	if e = repo.ActivateAccountBadge(ctx, f.actor, f.other, badge.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.AccountBadges(ctx, f.actor, f.other); e != nil {
		t.Fatal(e)
	}
	if e = repo.RevokeAccountBadge(ctx, f.actor, f.other, badge.ID); e != nil {
		t.Fatal(e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*)FROM owned_admin_extension_audit WHERE target_id=$1`, f.other); n != 3 {
		t.Fatal("child wrote audit or root lifecycle incomplete", n)
	}
}

func TestPGAccountAdminReciprocalLocksAndSessionABA(t *testing.T) {
	f := newAccountAdminFixture(t)
	query(t, f.db, `UPDATE owned_authority_accounts SET is_superuser=true WHERE id=$1`, f.other)
	verifier, e := authority.NewVerifier(authoritypg.New(f.db), f.config)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(f.db), verifier)
	repo, e := adminpg.New(f.db, guard, f.config.Now)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, e := verifier.Authenticate(ctx, f.token)
	if e != nil {
		t.Fatal(e)
	}
	other, e := verifier.Authenticate(ctx, f.otherToken)
	if e != nil {
		t.Fatal(e)
	}
	f.db.SetMaxOpenConns(2)
	start := make(chan struct{})
	done := make(chan error, 2)
	for _, pair := range []struct {
		proof  authority.Principal
		target string
	}{{p, f.other}, {other, f.actor}} {
		go func() {
			<-start
			_, e := repo.GrantAccountBadge(guard.Bind(ctx, pair.proof), pair.proof.Account.ID, pair.target, administrativeBadge("reciprocal"))
			done <- e
		}()
	}
	close(start)
	for range 2 {
		if e := <-done; e != nil {
			t.Fatal("reciprocal account locks failed", e)
		}
	}
	// Use the canonical session-write seam: narrowing then restoring scopes must
	// advance epochs and must never revive the original administrator proof.
	store := authoritypg.New(f.db)
	for _, scopes := range [][]string{{a.ViewPermission}, {"*"}} {
		if e = store.WithinAccount(ctx, f.actor, func(tx authority.Transaction) error {
			session, e := tx.Session(p.Session.ID)
			if e != nil {
				return e
			}
			session.Scopes = scopes
			return tx.SaveSession(session)
		}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = repo.AccountFactors(guard.Bind(ctx, p), f.actor, f.other); !errors.Is(e, authority.ErrStale) {
		t.Fatal("scope restoration revived original proof", e)
	}
}
