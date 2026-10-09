// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"solsynth.local/go-migration/internal/affiliationpg"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/signinpg"
	"solsynth.local/go-migration/internal/ticketpg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
	a "solsynth.local/go-migration/ports/passport/affiliation"
	"solsynth.local/go-migration/ports/passport/tickets"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"strings"
	"testing"
	"time"
)

type affiliationFixture struct {
	ticketFixture
	aff *affiliationpg.Repository
}

func newAffiliationFixture(t *testing.T) affiliationFixture {
	t.Helper()
	f := newTicketFixture(t)
	f.config.Activation.Completion = &ticketpg.ActivationCompletion{PolicyID: "synthetic-member-v1", Permissions: []string{"posts.create"}}
	f = ticketPool(t, f, f.db)
	var cancel context.CancelFunc
	f.ctx, cancel = context.WithTimeout(f.ctx, 45*time.Second)
	t.Cleanup(cancel)
	f.otherCtx, cancel = context.WithTimeout(f.otherCtx, 45*time.Second)
	t.Cleanup(cancel)
	f.adminCtx, cancel = context.WithTimeout(f.adminCtx, 45*time.Second)
	t.Cleanup(cancel)
	ar, e := affiliationpg.New(f.db, f.guard, affiliationpg.Config{Now: f.config.Now, Activation: f.repo})
	if e != nil {
		t.Fatal(e)
	}
	f.repo.WithInviteAdmission(ar)
	if e = ar.Ready(context.Background()); e != nil {
		t.Fatal("runner affiliation schema required", e)
	}
	return affiliationFixture{f, ar}
}
func affiliationPool(t *testing.T, f affiliationFixture, db *sql.DB) affiliationFixture {
	t.Helper()
	f.ticketFixture = ticketPool(t, f.ticketFixture, db)
	var e error
	f.aff, e = affiliationpg.New(db, f.guard, affiliationpg.Config{Now: f.config.Now, Activation: f.repo})
	if e != nil {
		t.Fatal(e)
	}
	f.repo.WithInviteAdmission(f.aff)
	return f
}
func affiliationCreate(t *testing.T, f affiliationFixture, invite bool, max int, skip bool) a.Spell {
	t.Helper()
	c := a.Command{Operation: "create", Create: &a.Create{MaxUsages: &max, SkipTests: &skip}}
	if invite {
		c.Operation = "invite.create"
		expiry := f.config.Now().Add(time.Hour)
		c.ExpiresAt = &expiry
	}
	out, e := f.aff.Execute(f.adminCtx, f.admin, c)
	if e != nil {
		t.Fatal(e)
	}
	return out.Value.(a.Spell)
}
func affiliationConsume(t *testing.T, f affiliationFixture, ctx context.Context, actor, word string) tickets.ActivationState {
	t.Helper()
	out, e := f.aff.Execute(ctx, actor, a.Command{Operation: "consume", Word: word})
	if e != nil {
		t.Fatal(e)
	}
	return out.Value.(tickets.ActivationState)
}
func affiliationVerified(t *testing.T, f affiliationFixture, actor string) {
	t.Helper()
	query(t, f.db, `INSERT INTO owned_authority_credentials(account_id,email,verified_at)VALUES($1,$2,$3)ON CONFLICT(account_id)DO UPDATE SET verified_at=EXCLUDED.verified_at`, actor, "synthetic_"+actor+"@example.invalid", f.config.Now())
}
func TestPGAffiliationTrackingIsolationAndRevocation(t *testing.T) {
	t.Run("tracking-management-does-not-issue-invites", func(t *testing.T) {
		f := newAffiliationFixture(t)
		store := authoritypg.New(f.db)
		if e := store.ReplacePolicy(context.Background(), f.admin, []authority.Rule{{Pattern: "affiliations.manage"}}, nil); e != nil {
			t.Fatal(e)
		}
		max := 1
		skip := true
		expiry := f.config.Now().Add(time.Hour)
		if _, e := f.aff.Execute(f.adminCtx, f.admin, a.Command{Operation: "create", Create: &a.Create{MaxUsages: &max, SkipTests: &skip}}); e != nil {
			t.Fatal("tracking manager lost tracking creation", e)
		}
		command := a.Command{Operation: "invite.create", Create: &a.Create{MaxUsages: &max, SkipTests: &skip}, ExpiresAt: &expiry}
		if _, e := f.aff.Execute(f.adminCtx, f.admin, command); !errors.Is(e, authority.ErrForbidden) {
			t.Fatal("tracking permission issued test-bypass invite", e)
		}
		if e := store.ReplacePolicy(context.Background(), f.admin, []authority.Rule{{Pattern: "affiliations.manage"}, {Pattern: a.InviteIssuePermission}}, nil); e != nil {
			t.Fatal(e)
		}
		if _, e := f.aff.Execute(f.adminCtx, f.admin, command); e != nil {
			t.Fatal("explicit root invite issuer denied", e)
		}
	})

	f := newAffiliationFixture(t)
	s := affiliationCreate(t, f, false, 2, true)
	h := (&a.Service{Repository: f.aff, Authority: f.verifier}).Handler()
	backendCall(t, h, "POST", "/api/affiliations/"+s.Spell+"/results", "", `{"resource_identifier":"account:`+f.actor+`"}`, 200)
	state := ticketExec[tickets.ActivationState](t, f.ticketFixture, f.ctx, f.actor, tickets.Command{Operation: "activation.get"})
	if state.TestsBypassed || state.IsActivated {
		t.Fatal("anonymous tracking granted admission", state)
	}
	for _, op := range []string{"get", "results", "delete"} {
		if _, e := f.aff.Execute(f.ctx, f.actor, a.Command{Operation: op, ID: s.ID}); e == nil {
			t.Fatal("cross-account operation", op)
		}
	}
	backendCall(t, h, "POST", "/api/affiliations/"+s.Spell+"/results", "bad-token", `{"resource_identifier":"synthetic:bad"}`, 401)
	backendCall(t, h, "POST", "/api/affiliations/"+s.Spell+"/results", "", `{"resource_identifier":"synthetic:second"}`, 200)
	backendCall(t, h, "POST", "/api/affiliations/"+s.Spell+"/results", "", `{"resource_identifier":"synthetic:third"}`, 400)
	listed, e := f.aff.Execute(f.adminCtx, f.admin, a.Command{Operation: "list", Query: a.Query{Take: 10, Order: "usage", Desc: true}})
	if e != nil || listed.Total == nil || *listed.Total != 1 {
		t.Fatal(listed, e)
	}
	results, e := f.aff.Execute(f.adminCtx, f.admin, a.Command{Operation: "results", ID: s.ID})
	if e != nil || *results.Total != 2 {
		t.Fatal(results, e)
	}
	if _, e = f.aff.Execute(f.adminCtx, f.admin, a.Command{Operation: "delete", ID: s.ID}); e != nil {
		t.Fatal(e)
	}
	backendCall(t, h, "POST", "/api/affiliations/"+s.Spell+"/results", "", `{"resource_identifier":"synthetic:revoked"}`, 404)
	if _, e = f.aff.Execute(f.adminCtx, f.admin, a.Command{Operation: "create", Create: &a.Create{Spell: &s.Spell}}); !errors.Is(e, a.ErrConflict) {
		t.Fatal("revoked spell word reused", e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_affiliation_results WHERE spell_id=$1`, s.ID); n != 2 {
		t.Fatal(n)
	}
	backendCall(t, h, "POST", "/api/affiliations/purchase", f.token, "", 503)
}

func TestPGAffiliationPaidInviteUsesFundedTransferAndExactReplay(t *testing.T) {
	f := newAffiliationFixture(t)
	query(t, f.db, affiliationpg.PaidSchema)
	treasuryActor := authorityUUID(t)
	if e := authoritypg.SeedSynthetic(f.ctx, f.db, treasuryActor, "synthetic_affiliation_treasury_"+strings.ReplaceAll(treasuryActor, "-", ""), f.config.Now()); e != nil {
		t.Fatal(e)
	}
	wallets := walletpg.New(f.db)
	payer := walletCreate(t, wallets, f.admin)
	treasury := walletCreate(t, wallets, treasuryActor)
	currency := "synthetic-affiliation-points"
	walletFund(t, f.db, payer, currency, "50.000")
	price, e := wallet.ParseAmount("12.500")
	if e != nil {
		t.Fatal(e)
	}
	paid, e := affiliationpg.New(f.db, f.guard, affiliationpg.Config{Now: f.config.Now, Activation: f.repo, Purchase: &affiliationpg.PurchaseConfig{Currency: currency, TreasuryAccountID: treasuryActor, TreasuryWalletID: treasury.ID, Price: price, MaxUsages: 5, MaxLifetime: 24 * time.Hour}})
	if e != nil {
		t.Fatal(e)
	}
	if e = authoritypg.New(f.db).ReplacePolicy(f.ctx, f.admin, []authority.Rule{{Pattern: a.InviteIssuePermission}}, nil); e != nil {
		t.Fatal(e)
	}
	expires := f.config.Now().Add(time.Hour)
	command := a.Command{Operation: "purchase", Purchase: &a.InvitePurchase{PayerWalletID: payer.ID, MaxUsages: 2, ExpiresAt: expires, Key: "paid-invite-one"}}
	first, e := paid.Execute(f.adminCtx, f.admin, command)
	if e != nil {
		t.Fatal(e)
	}
	second, e := paid.Execute(f.adminCtx, f.admin, command)
	if e != nil {
		t.Fatal(e)
	}
	a1, ok1 := first.Value.(a.InvitePurchaseReceipt)
	a2, ok2 := second.Value.(a.InvitePurchaseReceipt)
	if !ok1 || !ok2 || a1.Spell.ID != a2.Spell.ID || a1.Transaction.ID != a2.Transaction.ID {
		t.Fatal("purchase did not replay exactly", first.Value, second.Value)
	}
	walletAssertBalance(t, wallets, payer.ID, currency, "37.500", "0.000", "37.500")
	walletAssertBalance(t, wallets, treasury.ID, currency, "12.500", "0.000", "12.500")
	changed := command
	changed.Purchase = &a.InvitePurchase{PayerWalletID: payer.ID, MaxUsages: 3, ExpiresAt: expires, Key: "paid-invite-one"}
	if _, e = paid.Execute(f.adminCtx, f.admin, changed); !errors.Is(e, a.ErrConflict) {
		t.Fatal("changed request replay accepted", e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_affiliation_purchases WHERE actor_id=$1::uuid`, f.admin); n != 1 {
		t.Fatal(n)
	}
}
func TestPGAffiliationConcurrentInviteConsumptionAndReplay(t *testing.T) {
	f := newAffiliationFixture(t)
	fresh := affiliationPool(t, f, database(t))
	s := affiliationCreate(t, f, true, 2, true)
	actors := []struct {
		repo *affiliationpg.Repository
		ctx  context.Context
		id   string
	}{{f.aff, f.ctx, f.actor}, {fresh.aff, fresh.otherCtx, f.other}, {f.aff, f.adminCtx, f.admin}}
	type result struct {
		actor string
		e     error
	}
	done := make(chan result, 3)
	start := make(chan struct{})
	for _, v := range actors {
		go func(v struct {
			repo *affiliationpg.Repository
			ctx  context.Context
			id   string
		}) {
			<-start
			_, e := v.repo.Execute(v.ctx, v.id, a.Command{Operation: "consume", Word: s.Spell})
			done <- result{v.id, e}
		}(v)
	}
	close(start)
	var winner string
	success := 0
	for range actors {
		v := <-done
		if v.e == nil {
			success++
			winner = v.actor
		} else if !errors.Is(v.e, a.ErrInvite) {
			t.Fatal(v.e)
		}
	}
	if success != 2 {
		t.Fatal("exact usage bound", success)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_affiliation_results WHERE spell_id=$1`, s.ID); n != 2 {
		t.Fatal(n)
	}
	query(t, f.db, `UPDATE owned_affiliation_spells SET expires_at=$2 WHERE id=$1`, s.ID, f.config.Now().Add(time.Second))
	f.clock.Add(1)
	for _, v := range actors {
		if v.id == winner {
			for range 3 {
				if _, e := v.repo.Execute(v.ctx, v.id, a.Command{Operation: "consume", Word: s.Spell}); e != nil {
					t.Fatal(e)
				}
			}
		}
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_affiliation_results WHERE spell_id=$1`, s.ID); n != 2 {
		t.Fatal("replay consumed quota", n)
	}
	if _, e := f.aff.Execute(f.adminCtx, f.admin, a.Command{Operation: "delete", ID: s.ID}); e != nil {
		t.Fatal(e)
	}
	for _, v := range actors {
		if v.id == winner {
			if _, e := v.repo.Execute(v.ctx, v.id, a.Command{Operation: "consume", Word: s.Spell}); !errors.Is(e, a.ErrInvite) {
				t.Fatal("revocation ignored", e)
			}
		}
	}
}
func TestPGAffiliationCanonicalActivationOnceOnly(t *testing.T) {
	f := newAffiliationFixture(t)
	s := affiliationCreate(t, f, true, 1, true)
	state := affiliationConsume(t, f, f.ctx, f.actor, s.Spell)
	if !state.TestsBypassed || state.HasVerifiedContact || state.IsActivated || state.IsSatisfied {
		t.Fatal(state)
	}
	affiliationVerified(t, f, f.actor)
	// Existing deny policy is retained even when a configured allow is recorded.
	if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.actor, []authority.Rule{{Pattern: "tests.take"}, {Pattern: "posts.create", Deny: true}}, nil); e != nil {
		t.Fatal(e)
	}
	state = ticketExec[tickets.ActivationState](t, f.ticketFixture, f.ctx, f.actor, tickets.Command{Operation: "activation.recheck"})
	if !state.IsActivated || state.ActivationPending || !state.IsSatisfied {
		t.Fatal(state)
	}
	if _, e := f.verifier.Authorize(context.Background(), f.token, "posts.create"); !errors.Is(e, authority.ErrForbidden) {
		t.Fatal("activation overwrote deny", e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_authority_grants WHERE account_id=$1 AND pattern='posts.create' AND deny`, f.actor); n != 1 {
		t.Fatal(n)
	}
	var original string
	if e := f.db.QueryRow(`SELECT policy_sha256 FROM owned_authority_activations WHERE account_id=$1`, f.actor).Scan(&original); e != nil {
		t.Fatal(e)
	}
	if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.actor, []authority.Rule{{Pattern: "tests.take"}}, nil); e != nil {
		t.Fatal(e)
	}
	f.config.Activation.Completion = &ticketpg.ActivationCompletion{PolicyID: "synthetic-member-v2", Permissions: []string{"posts.create", "posts.react"}}
	fresh := affiliationPool(t, f, database(t))
	for range 2 {
		state = ticketExec[tickets.ActivationState](t, fresh.ticketFixture, fresh.ctx, f.actor, tickets.Command{Operation: "activation.recheck"})
		if !state.IsActivated {
			t.Fatal(state)
		}
	}
	var hash, policy string
	if e := f.db.QueryRow(`SELECT policy_sha256,policy_id FROM owned_authority_activations WHERE account_id=$1`, f.actor).Scan(&hash, &policy); e != nil || hash != original || policy != "synthetic-member-v1" {
		t.Fatal(hash, policy, e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_authority_grants WHERE account_id=$1 AND pattern IN('posts.create','posts.react')`, f.actor); n != 0 {
		t.Fatal("replay restored revoked grants", n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_authority_activations WHERE account_id=$1`, f.other); n != 0 {
		t.Fatal("activation crossed account", n)
	}
}
func TestPGAffiliationInviteExpiryRootAndWaitRevocation(t *testing.T) {
	t.Run("expiry_and_root", func(t *testing.T) {
		f := newAffiliationFixture(t)
		s := affiliationCreate(t, f, true, 1, true)
		parent, e := f.verifier.Authenticate(context.Background(), f.token)
		if e != nil {
			t.Fatal(e)
		}
		pair := pgIssue(t, f.issuer, f.actor)
		child, e := f.verifier.Authenticate(context.Background(), pair.Token)
		if e != nil {
			t.Fatal(e)
		}
		query(t, f.db, `UPDATE owned_authority_sessions SET parent_id=$1 WHERE id=$2`, parent.Session.ID, child.Session.ID)
		proof, e := f.verifier.Authenticate(context.Background(), pair.Token)
		if e != nil {
			t.Fatal("child fixture must authenticate", e)
		}
		if _, e = f.aff.Execute(f.guard.Bind(context.Background(), proof), f.actor, a.Command{Operation: "consume", Word: s.Spell}); !errors.Is(e, authority.ErrForbidden) {
			t.Fatal("delegated admission", e)
		}
		if _, e = f.repo.Execute(f.guard.Bind(context.Background(), proof), f.actor, tickets.Command{Operation: "activation.recheck"}); !errors.Is(e, authority.ErrForbidden) {
			t.Fatal("delegated activation", e)
		}
		adminParent, e := f.verifier.Authenticate(context.Background(), f.adminToken)
		if e != nil {
			t.Fatal(e)
		}
		adminPair, e := f.issuer.IssueSession(context.Background(), f.admin, authority.SessionOptions{Label: "Synthetic delegated invite admin", Scopes: []string{"affiliations.manage", a.InviteIssuePermission}})
		if e != nil {
			t.Fatal(e)
		}
		adminChild, e := f.verifier.Authenticate(context.Background(), adminPair.Token)
		if e != nil {
			t.Fatal(e)
		}
		query(t, f.db, `UPDATE owned_authority_sessions SET parent_id=$1 WHERE id=$2`, adminParent.Session.ID, adminChild.Session.ID)
		adminChild, e = f.verifier.Authenticate(context.Background(), adminPair.Token)
		if e != nil {
			t.Fatal(e)
		}
		expiry := f.config.Now().Add(time.Hour)
		max := 1
		if _, e = f.aff.Execute(f.guard.Bind(context.Background(), adminChild), f.admin, a.Command{Operation: "invite.create", Create: &a.Create{MaxUsages: &max}, ExpiresAt: &expiry}); !errors.Is(e, authority.ErrForbidden) {
			t.Fatal("delegated enduring invite issuance", e)
		}
		query(t, f.db, `UPDATE owned_affiliation_spells SET expires_at=$2 WHERE id=$1`, s.ID, f.config.Now().Add(time.Second))
		f.clock.Add(1)
		if _, e = f.aff.Execute(f.ctx, f.actor, a.Command{Operation: "consume", Word: s.Spell}); !errors.Is(e, a.ErrInvite) {
			t.Fatal("inclusive expiry", e)
		}
		if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_affiliation_results WHERE spell_id=$1`, s.ID); n != 0 {
			t.Fatal(n)
		}
	})
	t.Run("session_revoked_while_waiting_for_account", func(t *testing.T) {
		f := newAffiliationFixture(t)
		s := affiliationCreate(t, f, true, 1, true)
		ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
		defer cancel()
		locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
		blocker, pid := atomicBlocker(t, locks, "owned_authority_accounts", f.actor)
		if _, e := blocker.ExecContext(ctx, `UPDATE owned_authority_sessions SET revoked_at=$2 WHERE account_id=$1`, f.actor, f.config.Now()); e != nil {
			t.Fatal(e)
		}
		done := make(chan error, 1)
		go func() { _, e := f.aff.Execute(ctx, f.actor, a.Command{Operation: "consume", Word: s.Spell}); done <- e }()
		waitAtomicBlocked(t, locks, pid, "owned_authority_accounts")
		if e := blocker.Commit(); e != nil {
			t.Fatal(e)
		}
		if e := <-done; e == nil {
			t.Fatal("revoked proof admitted")
		}
		if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_affiliation_results WHERE spell_id=$1`, s.ID); n != 0 {
			t.Fatal(n)
		}
	})
	t.Run("expiry_after_spell_wait", func(t *testing.T) {
		f := newAffiliationFixture(t)
		s := affiliationCreate(t, f, true, 1, true)
		query(t, f.db, `UPDATE owned_affiliation_spells SET expires_at=$2 WHERE id=$1`, s.ID, f.config.Now().Add(2*time.Second))
		ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
		defer cancel()
		locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
		blocker, pid := atomicBlocker(t, locks, "owned_affiliation_spells", s.ID)
		done := make(chan error, 1)
		go func() { _, e := f.aff.Execute(ctx, f.actor, a.Command{Operation: "consume", Word: s.Spell}); done <- e }()
		waitAtomicBlocked(t, locks, pid, "owned_affiliation_spells")
		f.clock.Add(3)
		if e := blocker.Commit(); e != nil {
			t.Fatal(e)
		}
		if e := <-done; !errors.Is(e, a.ErrInvite) {
			t.Fatal("wait crossed expiry", e)
		}
	})
}
func TestPGAffiliationActivationRollbackAndInviteRevocation(t *testing.T) {
	f := newAffiliationFixture(t)
	s := affiliationCreate(t, f, true, 1, true)
	affiliationVerified(t, f, f.actor)
	query(t, f.db, `CREATE FUNCTION synthetic_affiliation_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.account_id::text=TG_ARGV[0] AND NEW.pattern='posts.create' THEN RAISE EXCEPTION 'synthetic activation rollback'; END IF; RETURN NEW; END $$`)
	query(t, f.db, fmt.Sprintf(`CREATE TRIGGER synthetic_affiliation_fail BEFORE INSERT ON owned_authority_grants FOR EACH ROW EXECUTE FUNCTION synthetic_affiliation_fail('%s')`, f.actor))
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DROP TRIGGER IF EXISTS synthetic_affiliation_fail ON owned_authority_grants;DROP FUNCTION IF EXISTS synthetic_affiliation_fail()`)
	})
	if _, e := f.aff.Execute(f.ctx, f.actor, a.Command{Operation: "consume", Word: s.Spell}); e == nil {
		t.Fatal("activation failure hidden")
	}
	for _, q := range []string{`SELECT count(*) FROM owned_affiliation_results WHERE recipient_id=$1`, `SELECT count(*) FROM owned_authority_activations WHERE account_id=$1`, `SELECT count(*) FROM owned_authority_grants WHERE account_id=$1 AND pattern='posts.create'`} {
		if n := commerceCount(t, f.db, q, f.actor); n != 0 {
			t.Fatal("partial admission", n)
		}
	}
	query(t, f.db, `DROP TRIGGER synthetic_affiliation_fail ON owned_authority_grants;DROP FUNCTION synthetic_affiliation_fail()`)
	state := affiliationConsume(t, f, f.otherCtx, f.other, s.Spell)
	if !state.TestsBypassed || state.IsActivated {
		t.Fatal(state)
	}
	if _, e := f.aff.Execute(f.adminCtx, f.admin, a.Command{Operation: "delete", ID: s.ID}); e != nil {
		t.Fatal(e)
	}
	affiliationVerified(t, f, f.other)
	state = ticketExec[tickets.ActivationState](t, f.ticketFixture, f.otherCtx, f.other, tickets.Command{Operation: "activation.recheck"})
	if state.TestsBypassed || state.IsActivated || state.IsSatisfied {
		t.Fatal("revoked invite still bypassed tests", state)
	}
}
func TestPGAffiliationRegistrationAndContactActivation(t *testing.T) {
	f := newAffiliationFixture(t)
	s := affiliationCreate(t, f, true, 1, true)
	store := signinpg.New(f.db, authoritypg.New(f.db), f.guard).WithAffiliation(f.aff).WithActivation(f.repo)
	delivery := &syntheticDelivery{tokens: map[string]string{}}
	seal := sha256.Sum256([]byte("synthetic affiliation sign-in seal only"))
	service, e := signin.New(store, f.issuer, f.verifier, signin.Config{SealKey: seal[:], Issuer: "Synthetic affiliations", Delivery: delivery, Captcha: syntheticCaptcha{}, Now: f.config.Now, InitialMemberPermissions: []string{"tests.take"}})
	if e != nil {
		t.Fatal(e)
	}
	name := "synthetic_" + strings.ReplaceAll(authorityUUID(t), "-", "")[:20]
	request := signin.RegisterRequest{Name: name, Nick: "Synthetic", Email: name + "@example.invalid", Password: syntheticPassword, AffiliationSpell: s.Spell, CaptchaToken: "bad"}
	if _, e = service.Register(context.Background(), request, name); e == nil {
		t.Fatal("invite bypassed CAPTCHA")
	}
	request.CaptchaToken = "synthetic-captcha"
	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	locks := atomicAuthorityFixture{db: f.db, ctx: waitCtx}
	blocker, pid := atomicBlocker(t, locks, "owned_authority_accounts", f.admin)
	type registration struct {
		account authority.Account
		err     error
	}
	done := make(chan registration, 1)
	go func() { account, err := service.Register(waitCtx, request, name); done <- registration{account, err} }()
	waitAtomicBlocked(t, locks, pid, "owned_authority_accounts")
	// A contact writer already holding the issuer account must still be able
	// to acquire this email key while enrollment waits for that account.
	emailHash := sha256.Sum256([]byte("owned-canonical-email\x00" + request.Email))
	emailKey := int64(binary.BigEndian.Uint64(emailHash[:8]))
	var gotEmail bool
	if e = blocker.QueryRowContext(waitCtx, `SELECT pg_try_advisory_xact_lock($1::bigint)`, emailKey).Scan(&gotEmail); e != nil || !gotEmail {
		t.Fatal("enrollment inverted account/email lock order", gotEmail, e)
	}
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	registered := <-done
	account := registered.account
	if registered.err != nil {
		t.Fatal(registered.err)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_authority_activations WHERE account_id=$1`, account.ID); n != 0 {
		t.Fatal("activated without contact", n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_affiliation_results WHERE recipient_id=$1`, account.ID); n != 1 {
		t.Fatal(n)
	}
	other := request
	other.Name = name + "x"
	other.Email = other.Name + "@example.invalid"
	if _, e = service.Register(context.Background(), other, other.Name); e == nil {
		t.Fatal("exhausted invite registered account")
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_authority_accounts WHERE name=$1`, other.Name); n != 0 {
		t.Fatal("account survived failed admission", n)
	}
	code := delivery.code(request.Email, "verify_email")
	if code == "" {
		t.Fatal("missing synthetic contact challenge")
	}
	if e = service.VerifyEmail(context.Background(), code); e != nil {
		t.Fatal(e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_authority_activations WHERE account_id=$1`, account.ID); n != 1 {
		t.Fatal(n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_authority_grants WHERE account_id=$1 AND pattern='posts.create'`, account.ID); n != 1 {
		t.Fatal(n)
	}
	if e = service.VerifyEmail(context.Background(), code); e == nil {
		t.Fatal("one-use contact replay accepted")
	}
}
func TestPGBackendAffiliationRoutesAndActivation(t *testing.T) {
	f := newAffiliationFixture(t)
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.authorityConfig, PublicVisibility: &visibility, MessagerPolicy: &policy, Tickets: &f.config})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	raw := backendCall(t, app, "POST", "/passport/affiliations/registration-invites", f.adminToken, fmt.Sprintf(`{"max_usages":1,"skip_tests":true,"expires_at":%q}`, f.config.Now().Add(time.Hour).Format(time.RFC3339)), 200)
	s := backendDecode[a.Spell](t, raw)
	backendCall(t, app, "POST", "/api/affiliations/"+s.Spell+"/results", "", `{"resource_identifier":"account:`+f.actor+`"}`, 400)
	affiliationVerified(t, f, f.actor)
	raw = backendCall(t, app, "POST", "/passport/affiliations/registration-invites/consume", f.token, fmt.Sprintf(`{"spell":%q}`, s.Spell), 200)
	state := backendDecode[tickets.ActivationState](t, raw)
	if !state.IsActivated || !state.TestsBypassed {
		t.Fatal(state)
	}
	raw = backendCall(t, app, "GET", "/passport/accounts/me/activation/progress", f.token, "", 200)
	if !backendDecode[tickets.ActivationState](t, raw).IsActivated {
		t.Fatal(raw.Body.String())
	}
	backendCall(t, app, "GET", "/passport/affiliations/"+s.ID, f.otherToken, "", 404)
	backendCall(t, app, "GET", "/sphere/affiliations/"+s.ID, f.adminToken, "", 404)
}
func TestPGAffiliationNonBypassInviteRequiresCanonicalExam(t *testing.T) {
	f := newAffiliationFixture(t)
	s := affiliationCreate(t, f, true, 1, false)
	affiliationVerified(t, f, f.actor)
	state := affiliationConsume(t, f, f.ctx, f.actor, s.Spell)
	if state.TestsBypassed || state.IsSatisfied || state.IsActivated {
		t.Fatal("non-bypass invite skipped exam", state)
	}
	exam, qs := seedExam(t, f.ticketFixture, false)
	parent, e := f.verifier.Authenticate(context.Background(), f.token)
	if e != nil {
		t.Fatal(e)
	}
	pair, e := f.issuer.IssueSession(context.Background(), f.actor, authority.SessionOptions{Label: "Synthetic delegated exam", Scopes: []string{"tests.take"}})
	if e != nil {
		t.Fatal(e)
	}
	proof, e := f.verifier.Authenticate(context.Background(), pair.Token)
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_authority_sessions SET parent_id=$1 WHERE id=$2`, parent.Session.ID, proof.Session.ID)
	proof, e = f.verifier.Authenticate(context.Background(), pair.Token)
	if e != nil {
		t.Fatal(e)
	}
	childCtx := f.guard.Bind(context.Background(), proof)
	attempt := ticketExec[tickets.Attempt](t, f.ticketFixture, childCtx, f.actor, tickets.Command{Operation: "attempt.start", Key: exam.Key})
	result := ticketExec[tickets.Attempt](t, f.ticketFixture, childCtx, f.actor, tickets.Command{Operation: "attempt.submit", ID: attempt.ID, Answers: []tickets.AnswerInput{correctInput(qs[0])}})
	if result.Status != tickets.Passed {
		t.Fatal(result)
	}
	state = ticketExec[tickets.ActivationState](t, f.ticketFixture, f.ctx, f.actor, tickets.Command{Operation: "activation.get"})
	if state.IsActivated || !state.ActivationPending || !state.IsSatisfied {
		t.Fatal("delegated exam completed canonical activation", state)
	}
	state = ticketExec[tickets.ActivationState](t, f.ticketFixture, f.ctx, f.actor, tickets.Command{Operation: "activation.recheck"})
	if !state.IsActivated || !state.IsSatisfied || state.TestsBypassed {
		t.Fatal(state)
	}
	if _, e := f.verifier.Authorize(context.Background(), f.token, "posts.create"); e != nil {
		t.Fatal("canonical grant not effective", e)
	}
}
func TestPGAffiliationIssuerRevocationAfterWaitRollsBackActivation(t *testing.T) {
	f := newAffiliationFixture(t)
	s := affiliationCreate(t, f, true, 1, true)
	affiliationVerified(t, f, f.actor)
	starts := f.config.Now().Add(2 * time.Second)
	if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.admin, []authority.Rule{{Pattern: "*"}}, []authority.Punishment{{Kind: "disable_account", StartsAt: &starts}}); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
	blocker, pid := atomicBlocker(t, locks, "owned_affiliation_spells", s.ID)
	done := make(chan error, 1)
	go func() { _, e := f.aff.Execute(ctx, f.actor, a.Command{Operation: "consume", Word: s.Spell}); done <- e }()
	waitAtomicBlocked(t, locks, pid, "owned_affiliation_spells")
	f.clock.Add(3)
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, authority.ErrForbidden) {
		t.Fatal("issuer restriction after wait", e)
	}
	for _, q := range []string{`SELECT count(*) FROM owned_affiliation_results WHERE recipient_id=$1`, `SELECT count(*) FROM owned_authority_activations WHERE account_id=$1`, `SELECT count(*) FROM owned_authority_grants WHERE account_id=$1 AND pattern='posts.create'`} {
		if n := commerceCount(t, f.db, q, f.actor); n != 0 {
			t.Fatal("issuer revocation retained partial activation", n)
		}
	}
}
func equalAffiliationAdmissionJSON(left, right []byte) bool {
	var a, b any
	return json.Unmarshal(left, &a) == nil && json.Unmarshal(right, &b) == nil && reflect.DeepEqual(a, b)
}

func TestPGAffiliationRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	type saved struct {
		Account, Spell, PolicyHash string
		Activated                  time.Time
		Result                     json.RawMessage
	}
	const marker = "affiliation-activation.json"
	if phase == "before" {
		f := newAffiliationFixture(t)
		s := affiliationCreate(t, f, true, 1, true)
		affiliationVerified(t, f, f.actor)
		state := affiliationConsume(t, f, f.ctx, f.actor, s.Spell)
		if !state.IsActivated {
			t.Fatal(state)
		}
		v := saved{Account: f.actor, Spell: s.ID}
		if e := f.db.QueryRow(`SELECT activated_at,policy_sha256 FROM owned_authority_activations WHERE account_id=$1`, f.actor).Scan(&v.Activated, &v.PolicyHash); e != nil {
			t.Fatal(e)
		}
		if e := f.db.QueryRow(`SELECT to_jsonb(r) FROM owned_affiliation_results r WHERE recipient_id=$1`, f.actor).Scan(&v.Result); e != nil {
			t.Fatal(e)
		}
		saveRestartFixture(t, marker, v)
		return
	}
	var v saved
	loadRestartFixture(t, marker, &v)
	db := database(t)
	var current saved
	var result []byte
	if e := db.QueryRow(`SELECT activated_at,policy_sha256 FROM owned_authority_activations WHERE account_id=$1`, v.Account).Scan(&current.Activated, &current.PolicyHash); e != nil || !current.Activated.Equal(v.Activated) || current.PolicyHash != v.PolicyHash {
		t.Fatal("activation changed across restart", e)
	}
	if e := db.QueryRow(`SELECT to_jsonb(r) FROM owned_affiliation_results r WHERE recipient_id=$1 AND spell_id=$2`, v.Account, v.Spell).Scan(&result); e != nil || !equalAffiliationAdmissionJSON(result, v.Result) {
		t.Fatal("admission not durable", e)
	}
	if n := commerceCount(t, db, `SELECT count(*) FROM owned_authority_grants WHERE account_id=$1 AND pattern='posts.create'`, v.Account); n != 1 {
		t.Fatal(n)
	}
}
