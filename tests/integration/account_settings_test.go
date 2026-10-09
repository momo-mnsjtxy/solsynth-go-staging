// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/accountsettingspg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
	"solsynth.local/go-migration/ports/notifications"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/accountsettings"
	"strings"
	"testing"
	"time"
)

type settingsFixture struct {
	*signinFixture
	actor          authority.Account
	email, token   string
	accountRepo    *accountpg.Repository
	accountHandler http.Handler
}

func newSettingsFixture(t *testing.T) settingsFixture {
	t.Helper()
	base := newSigninFixture(t)
	a, email := base.register(t, true)
	if e := authoritypg.New(base.db).ReplacePolicy(base.ctx, a.ID, []authority.Rule{{Pattern: "account.contacts.manage"}, {Pattern: "accounts.manage"}}, nil); e != nil {
		t.Fatal(e)
	}
	f := settingsFixture{signinFixture: base, actor: a, email: email}
	f.token = f.login(t, a.Name, syntheticPassword).Token
	return settingsAccountHandler(t, f)
}
func settingsAccountHandler(t *testing.T, f settingsFixture) settingsFixture {
	t.Helper()
	query(t, f.db, accountpg.Schema)
	guard := authoritypg.NewTransactionGuard(authoritypg.New(f.db), f.verifier)
	f.accountRepo = accountpg.NewWithAuthority(f.db, guard).WithPublicContacts(f.store)
	f.accountHandler = (&accounts.Service{Repository: f.accountRepo, Authority: f.verifier}).Handler()
	return f
}
func settingsCall(t *testing.T, h http.Handler, method, path, token, body string, status int) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = fixtureNoBodyRequest(method, path, token)
	} else {
		r = fixtureJSONRequest(t, method, path, token, json.RawMessage(body))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
	}
	return w
}
func secondaryEmail(t *testing.T, f settingsFixture, label string) signin.Contact {
	t.Helper()
	email := "synthetic_" + label + "_" + strings.ReplaceAll(authorityUUID(t), "-", "") + "@example.invalid"
	v, e := f.service.CreateContact(f.ctx, f.token, signin.ContactInput{Type: 0, Content: email})
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func verifySecondary(t *testing.T, f settingsFixture, c signin.Contact) string {
	t.Helper()
	if e := f.service.SendContactVerification(f.ctx, f.token, c.ID); e != nil {
		t.Fatal(e)
	}
	code := f.delivery.code(c.Content, "verify_contact")
	if code == "" {
		t.Fatal("no synthetic verification delivery")
	}
	if e := f.service.VerifyEmail(f.ctx, code); e != nil {
		t.Fatal(e)
	}
	return code
}

func TestPGProfileAuthoredAliasesAndPrivateBirthday(t *testing.T) {
	f := newSettingsFixture(t)
	body := `{"first_name":"Synthetic","middle_name":"Profile","last_name":"Fixture","gender":"unspecified","bio":"Owned profile","time_zone":"America/New_York","birthday":"2000-02-29T05:00:00Z","username_color":{"type":"plain"}}`
	v := backendDecode[accounts.Account](t, settingsCall(t, f.accountHandler, "PATCH", "/stargate/accounts/me/profile", f.token, body, 200))
	if v.Profile == nil || v.Profile.Birthday == nil || v.Profile.FirstName != "Synthetic" {
		t.Fatal(v)
	}
	public := settingsCall(t, f.accountHandler, "GET", "/stargate/accounts/"+f.actor.Name, "", "", 200)
	if strings.Contains(public.Body.String(), "birthday") || !strings.Contains(public.Body.String(), "first_name") {
		t.Fatal("private/public profile projection", public.Body.String())
	}
	own := settingsCall(t, f.accountHandler, "GET", "/stargate/accounts/me", f.token, "", 200)
	if !strings.Contains(own.Body.String(), "2000-02-29") {
		t.Fatal(own.Body.String())
	}
	settingsCall(t, f.accountHandler, "PATCH", "/stargate/accounts/me", f.token, fmt.Sprintf(`{"name":%q,"nick":"Unchanged name accepted"}`, f.actor.Name), 200)
	settingsCall(t, f.accountHandler, "PATCH", "/stargate/accounts/me", f.token, `{"name":"free_rename"}`, 409)
	settingsCall(t, f.accountHandler, "PATCH", "/stargate/accounts/me/profile", f.token, `{"birthday":"2199-01-01T00:00:00Z"}`, 400)
	settingsCall(t, f.accountHandler, "PATCH", "/stargate/accounts/me/profile", f.token, `{"username_color":{"type":"plain","value":"#ffffff"}}`, 501)
	settingsCall(t, f.accountHandler, "PATCH", "/stargate/accounts/me/profile", f.token, `{"birthday":null,"birthday":"2000-01-01T00:00:00Z"}`, 400)
	settingsCall(t, f.accountHandler, "PATCH", "/stargate/accounts/me/profile", f.token, `{"birthday":null}`, 200)
	own = settingsCall(t, f.accountHandler, "GET", "/api/accounts/me", f.token, "", 200)
	if strings.Contains(own.Body.String(), "birthday") {
		t.Fatal("explicit clearing failed", own.Body.String())
	}
	if _, e := f.accountRepo.LookupSelf(context.Background(), f.actor.ID); e == nil {
		t.Fatal("private birthday projection accepted asserted identity")
	}
}

func TestPGContactVerificationPrimaryAndPublicLifecycle(t *testing.T) {
	f := newSettingsFixture(t)
	h := f.service.ContactHandler()
	initial := backendDecode[[]signin.Contact](t, settingsCall(t, h, "GET", "/stargate/contacts", f.token, "", 200))
	if len(initial) != 1 || !initial[0].IsPrimary || initial[0].Content != f.email || initial[0].VerifiedAt == nil {
		t.Fatal(initial)
	}
	email := secondaryEmail(t, f, "lifecycle")
	withoutDelivery := f.config
	withoutDelivery.Delivery = nil
	noMail, e := signin.New(f.store, f.issuer, f.verifier, withoutDelivery)
	if e != nil {
		t.Fatal(e)
	}
	if e = noMail.SendContactVerification(f.ctx, f.token, email.ID); !errors.Is(e, signin.ErrDelivery) {
		t.Fatal("unconfigured delivery claimed success", e)
	}
	settingsCall(t, h, "POST", "/stargate/contacts/"+email.ID+"/primary", f.token, "", 403)
	settingsCall(t, h, "POST", "/stargate/contacts/"+email.ID+"/public", f.token, "", 403)
	if _, e := f.service.Start(f.ctx, signin.StartRequest{Account: email.Content}, email.Content); e == nil {
		t.Fatal("unverified secondary became login")
	}
	settingsCall(t, h, "POST", "/stargate/contacts/"+email.ID+"/verify", f.token, "", 202)
	code := f.delivery.code(email.Content, "verify_contact")
	if e := f.service.ResetPassword(f.ctx, code, "different synthetic password"); !errors.Is(e, signin.ErrInvalid) {
		t.Fatal("contact proof crossed purpose", e)
	}
	settingsCall(t, f.service.Handler(), "POST", "/stargate/accounts/verify", "", fmt.Sprintf(`{"code":%q}`, code), 204)
	if e := f.service.VerifyEmail(f.ctx, code); !errors.Is(e, signin.ErrInvalid) {
		t.Fatal("verification replay", e)
	}
	settingsCall(t, h, "POST", "/stargate/contacts/"+email.ID+"/public", f.token, "", 200)
	public := settingsCall(t, f.accountHandler, "GET", "/api/accounts/"+f.actor.ID, "", "", 200)
	if !strings.Contains(public.Body.String(), email.Content) || strings.Contains(public.Body.String(), f.email) {
		t.Fatal("contact privacy", public.Body.String())
	}
	if _, e = f.store.PublicContacts(f.ctx, database(t), f.actor.ID); !errors.Is(e, authority.ErrUnavailable) {
		t.Fatal("public projection accepted a different pool", e)
	}
	settingsCall(t, h, "DELETE", "/stargate/contacts/"+email.ID+"/public", f.token, "", 200)
	public = settingsCall(t, f.accountHandler, "GET", "/api/accounts/"+f.actor.ID, "", "", 200)
	if strings.Contains(public.Body.String(), email.Content) {
		t.Fatal("visibility revocation stale")
	}
	g := newSettingsFixture(t)
	if _, e := g.service.PublishContact(g.ctx, g.token, email.ID, true); e == nil {
		t.Fatal("cross-owner contact write")
	}
	settingsCall(t, h, "POST", "/stargate/contacts/"+email.ID+"/primary", f.token, "", 200)
	if _, e := f.verifier.Authenticate(f.ctx, f.token); e == nil {
		t.Fatal("email promotion retained stale authority")
	}
	f.token = f.login(t, email.Content, syntheticPassword).Token
	contacts, e := f.service.ListContacts(f.ctx, f.token)
	if e != nil {
		t.Fatal(e)
	}
	primaryCount := 0
	for _, c := range contacts {
		if c.IsPrimary {
			primaryCount++
			if c.ID != email.ID || c.Content != email.Content {
				t.Fatal("primary identity changed", c)
			}
		}
	}
	if primaryCount != 1 {
		t.Fatal(contacts)
	}
	if _, e = f.store.Lookup(f.ctx, f.email); !errors.Is(e, authority.ErrNotFound) {
		t.Fatal("secondary old email retained authentication authority", e)
	}
	if e = f.service.RemoveContact(f.ctx, f.token, email.ID); !errors.Is(e, signin.ErrConflict) {
		t.Fatal("last primary removed", e)
	}
	if e = f.service.RemoveContact(f.ctx, f.token, initial[0].ID); e != nil {
		t.Fatal(e)
	}
	for _, typ := range []int{1, 2} {
		c, e := f.service.CreateContact(f.ctx, f.token, signin.ContactInput{Type: typ, Content: "Synthetic non-authentication contact"})
		if e != nil {
			t.Fatal(e)
		}
		if e = f.service.SendContactVerification(f.ctx, f.token, c.ID); !errors.Is(e, signin.ErrUnsupported) {
			t.Fatal("unconfigured contact verified", e)
		}
		if _, e = f.service.MakeContactPrimary(f.ctx, f.token, c.ID); !errors.Is(e, signin.ErrUnsupported) {
			t.Fatal("non-email authentication grant", e)
		}
	}
}

func TestPGContactEmailOwnershipAndRegistrationRace(t *testing.T) {
	f := newSettingsFixture(t)
	g := newSettingsFixture(t)
	email := "synthetic_shared_" + strings.ReplaceAll(authorityUUID(t), "-", "") + "@example.invalid"
	a, e := f.service.CreateContact(f.ctx, f.token, signin.ContactInput{Type: 0, Content: email})
	if e != nil {
		t.Fatal(e)
	}
	b, e := g.service.CreateContact(g.ctx, g.token, signin.ContactInput{Type: 0, Content: email})
	if e != nil {
		t.Fatal("pending contact reserved email", e)
	}
	if e = f.service.SendContactVerification(f.ctx, f.token, a.ID); e != nil {
		t.Fatal(e)
	}
	codeA := f.delivery.code(email, "verify_contact")
	if e = g.service.SendContactVerification(g.ctx, g.token, b.ID); e != nil {
		t.Fatal(e)
	}
	codeB := g.delivery.code(email, "verify_contact")
	start := make(chan struct{})
	done := make(chan error, 2)
	go func() { <-start; done <- f.service.VerifyEmail(f.ctx, codeA) }()
	go func() { <-start; done <- g.service.VerifyEmail(g.ctx, codeB) }()
	close(start)
	success := 0
	for i := 0; i < 2; i++ {
		e := <-done
		if e == nil {
			success++
		} else if !errors.Is(e, signin.ErrConflict) {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatal("email acquired by multiple accounts", success)
	}
	newName := "synthetic_dup_" + strings.ReplaceAll(authorityUUID(t), "-", "")[:18]
	if _, e = f.service.Register(f.ctx, signin.RegisterRequest{Name: newName, Email: email, Password: syntheticPassword, CaptchaToken: "synthetic-captcha"}, newName); !errors.Is(e, signin.ErrConflict) {
		t.Fatal("verified secondary duplicated by signup", e)
	}
	pending := secondaryEmail(t, f, "registration_race")
	if e = f.service.SendContactVerification(f.ctx, f.token, pending.ID); e != nil {
		t.Fatal(e)
	}
	code := f.delivery.code(pending.Content, "verify_contact")
	name := "synthetic_race_" + strings.ReplaceAll(authorityUUID(t), "-", "")[:18]
	start = make(chan struct{})
	done = make(chan error, 2)
	go func() { <-start; done <- f.service.VerifyEmail(f.ctx, code) }()
	go func() {
		<-start
		_, e := g.service.Register(g.ctx, signin.RegisterRequest{Name: name, Email: pending.Content, Password: syntheticPassword, CaptchaToken: "synthetic-captcha"}, name)
		done <- e
	}()
	close(start)
	success = 0
	for i := 0; i < 2; i++ {
		e := <-done
		if e == nil {
			success++
		} else if !errors.Is(e, signin.ErrConflict) {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatal("registration/verification ownership race", success)
	}
}

func TestPGContactAtomicityExpiryAndAuthority(t *testing.T) {
	f := newSettingsFixture(t)
	secondary := secondaryEmail(t, f, "atomic")
	verifySecondary(t, f, secondary)
	query(t, f.db, `CREATE FUNCTION synthetic_contact_audit_fail()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.account_id::text=TG_ARGV[0] THEN RAISE EXCEPTION 'synthetic contact rollback'; END IF; RETURN NEW; END$$`)
	query(t, f.db, fmt.Sprintf(`CREATE TRIGGER synthetic_contact_audit_fail BEFORE INSERT ON owned_authority_contact_audit FOR EACH ROW EXECUTE FUNCTION synthetic_contact_audit_fail('%s')`, f.actor.ID))
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DROP TRIGGER IF EXISTS synthetic_contact_audit_fail ON owned_authority_contact_audit;DROP FUNCTION IF EXISTS synthetic_contact_audit_fail()`)
	})
	if _, e := f.service.MakeContactPrimary(f.ctx, f.token, secondary.ID); e == nil {
		t.Fatal("audit failure hidden")
	}
	var email string
	if e := f.db.QueryRow(`SELECT email FROM owned_authority_credentials WHERE account_id=$1::uuid`, f.actor.ID).Scan(&email); e != nil || email != f.email {
		t.Fatal("credential promotion partially committed", email, e)
	}
	if _, e := f.verifier.Authenticate(f.ctx, f.token); e != nil {
		t.Fatal("version advance partially committed", e)
	}
	query(t, f.db, `DROP TRIGGER synthetic_contact_audit_fail ON owned_authority_contact_audit;DROP FUNCTION synthetic_contact_audit_fail()`)
	pending := secondaryEmail(t, f, "expiry_wait")
	if e := f.service.SendContactVerification(f.ctx, f.token, pending.ID); e != nil {
		t.Fatal(e)
	}
	code := f.delivery.code(pending.Content, "verify_contact")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
	blocker, pid := atomicBlocker(t, locks, "owned_authority_contacts", pending.ID)
	done := make(chan error, 1)
	go func() { done <- f.service.VerifyEmail(ctx, code) }()
	waitAtomicBlocked(t, locks, pid, "owned_authority_contacts")
	f.now.Add(int64((31 * time.Minute) / time.Second))
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, signin.ErrInvalid) {
		t.Fatal("expired token committed after wait", e)
	}
	var verified *time.Time
	if e := f.db.QueryRow(`SELECT verified_at FROM owned_authority_contacts WHERE id=$1::uuid`, pending.ID).Scan(&verified); e != nil || verified != nil {
		t.Fatal("expired verification partially committed", verified, e)
	}
	r := newSettingsFixture(t)
	toDelete := secondaryEmail(t, r, "recent_wait")
	recentLocks := atomicAuthorityFixture{db: r.db, ctx: ctx}
	recentBlocker, recentPID := atomicBlocker(t, recentLocks, "owned_authority_contacts", toDelete.ID)
	go func() { done <- r.service.RemoveContact(ctx, r.token, toDelete.ID) }()
	waitAtomicBlocked(t, recentLocks, recentPID, "owned_authority_contacts")
	r.now.Add(int64((11 * time.Minute) / time.Second))
	if e := recentBlocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, signin.ErrRecent) {
		t.Fatal("contact wait extended recent login", e)
	}
	var deleted *time.Time
	if e := r.db.QueryRow(`SELECT deleted_at FROM owned_authority_contacts WHERE id=$1::uuid`, toDelete.ID).Scan(&deleted); e != nil || deleted != nil {
		t.Fatal("stale recent-login deletion partially committed", deleted, e)
	}
	g := newSettingsFixture(t)
	queued := secondaryEmail(t, g, "disable")
	if e := g.service.SendContactVerification(g.ctx, g.token, queued.ID); e != nil {
		t.Fatal(e)
	}
	query(t, g.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1::uuid`, g.actor.ID)
	if e := g.service.VerifyEmail(g.ctx, g.delivery.code(queued.Content, "verify_contact")); !errors.Is(e, authority.ErrDisabled) {
		t.Fatal("disabled account verified contact", e)
	}
}

func TestPGNotificationPreferenceProjectionAndQueuedDelivery(t *testing.T) {
	f := newNotificationFixture(t)
	query(t, f.db, accountsettingspg.Schema)
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "notifications.preferences.manage"}}, nil); e != nil {
		t.Fatal(e)
	}
	prefs, e := accountsettingspg.New(f.db, f.guard, f.app)
	if e != nil {
		t.Fatal(e)
	}
	if e = prefs.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	f.repo.WithPreferencePolicy(prefs)
	h := (&accountsettings.Service{Repository: prefs, Authority: f.v}).Handler()
	path := "/api/accounts/me/notification-preferences/owned.integration?app=" + f.app
	settingsCall(t, h, "PUT", path, f.token, `{"preference":"reject","description":"Owned synthetic policy"}`, 200)
	reject := notificationEnqueue(t, f, notificationCommand(f, "preference-reject"))
	if _, e = f.repo.ProcessPending(f.ctx, 100); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = f.db.QueryRow(`SELECT count(*) FROM notification_v1.inbox WHERE id=$1::uuid`, reject).Scan(&count); e != nil || count != 0 {
		t.Fatal("rejected notification projected", count, e)
	}
	settingsCall(t, h, "PUT", path, f.token, `{"preference":"silent"}`, 200)
	command := notificationCommand(f, "preference-silent")
	command.External = true
	silent := notificationEnqueue(t, f, command)
	if _, e = f.repo.ProcessPending(f.ctx, 100); e != nil {
		t.Fatal(e)
	}
	if e = f.db.QueryRow(`SELECT count(*) FROM notification_v1.inbox WHERE id=$1::uuid`, silent).Scan(&count); e != nil || count != 1 {
		t.Fatal("silent inbox absent", count, e)
	}
	if e = f.db.QueryRow(`SELECT count(*) FROM notification_v1.deliveries WHERE notification_id=$1::uuid`, silent).Scan(&count); e != nil || count != 0 {
		t.Fatal("silent external delivery queued", count, e)
	}
	settingsCall(t, h, "DELETE", path, f.token, "", 204)
	command.Key = "preference-normal-before-revoke"
	queued := notificationEnqueue(t, f, command)
	if _, e = f.repo.ProcessPending(f.ctx, 100); e != nil {
		t.Fatal(e)
	}
	settingsCall(t, h, "PUT", path, f.token, `{"preference":"reject"}`, 200)
	sender := &notificationSender{}
	ok, e := f.repo.DispatchOne(f.ctx, sender)
	if e != nil || !ok || sender.calls != 0 {
		t.Fatal("queued delivery ignored current policy", ok, e, sender.calls)
	}
	var suppressed *time.Time
	if e = f.db.QueryRow(`SELECT suppressed_at FROM notification_v1.deliveries WHERE notification_id=$1::uuid`, queued).Scan(&suppressed); e != nil || suppressed == nil {
		t.Fatal("suppression receipt missing", e)
	}
	if own, e := prefs.List(f.otherCtx, f.other, f.app); e != nil || len(own) != 0 {
		t.Fatal("cross-account preference leak", own, e)
	}
	settingsCall(t, h, "PUT", path, f.token, `{"preference":"normal","preference":"reject"}`, 400)
	settingsCall(t, h, "PUT", path, f.token, `{"preference":"invented"}`, 400)
}

func TestPGAccountSettingsRestartPersistence(t *testing.T) {
	_ = database(t)
	path := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "account-settings-record.json")
	type saved struct {
		Account, Name, Email, PublicContact, PublicEmail, PendingContact, PendingCode string
		Now                                                                           int64
	}
	if os.Getenv("PG_TEST_PHASE") == "before" {
		f := newSettingsFixture(t)
		query(t, f.db, accountsettingspg.Schema)
		if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor.ID, []authority.Rule{{Pattern: "account.contacts.manage"}, {Pattern: "accounts.manage"}, {Pattern: "notifications.preferences.manage"}}, nil); e != nil {
			t.Fatal(e)
		}
		guard := authoritypg.NewTransactionGuard(authoritypg.New(f.db), f.verifier)
		prefs, e := accountsettingspg.New(f.db, guard, notifications.DefaultApp)
		if e != nil {
			t.Fatal(e)
		}
		proof, e := f.verifier.Authenticate(f.ctx, f.token)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = prefs.Set(guard.Bind(f.ctx, proof), f.actor.ID, notifications.DefaultApp, "synthetic.restart", accountsettings.PreferenceInput{Preference: "silent"}); e != nil {
			t.Fatal(e)
		}
		settingsCall(t, f.accountHandler, "PATCH", "/stargate/accounts/me/profile", f.token, `{"first_name":"Synthetic restart","birthday":"1990-03-04T00:00:00Z"}`, 200)
		public := secondaryEmail(t, f, "restart_public")
		verifySecondary(t, f, public)
		if _, e := f.service.PublishContact(f.ctx, f.token, public.ID, true); e != nil {
			t.Fatal(e)
		}
		pending := secondaryEmail(t, f, "restart_pending")
		if e := f.service.SendContactVerification(f.ctx, f.token, pending.ID); e != nil {
			t.Fatal(e)
		}
		b, _ := json.Marshal(saved{f.actor.ID, f.actor.Name, f.email, public.ID, public.Content, pending.ID, f.delivery.code(pending.Content, "verify_contact"), f.now.Load()})
		if e := os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
		return
	}
	if os.Getenv("PG_TEST_PHASE") != "after" {
		t.Fatal("explicit restart phase required")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var data saved
	if e = json.Unmarshal(b, &data); e != nil {
		t.Fatal(e)
	}
	f := settingsFixture{signinFixture: newSigninFixture(t), actor: authority.Account{ID: data.Account, Name: data.Name}, email: data.Email}
	f.now.Store(data.Now + 1)
	f.token = f.login(t, data.Name, syntheticPassword).Token
	f = settingsAccountHandler(t, f)
	guard := authoritypg.NewTransactionGuard(authoritypg.New(f.db), f.verifier)
	prefs, e := accountsettingspg.New(f.db, guard, notifications.DefaultApp)
	if e != nil {
		t.Fatal(e)
	}
	proof, e := f.verifier.Authenticate(f.ctx, f.token)
	if e != nil {
		t.Fatal(e)
	}
	values, e := prefs.List(guard.Bind(f.ctx, proof), f.actor.ID, notifications.DefaultApp)
	if e != nil || len(values) != 1 || values[0].Preference != "silent" {
		t.Fatal("preference did not persist", values, e)
	}
	own := settingsCall(t, f.accountHandler, "GET", "/stargate/accounts/me", f.token, "", 200)
	if !strings.Contains(own.Body.String(), "1990-03-04") {
		t.Fatal("private profile lost")
	}
	public := settingsCall(t, f.accountHandler, "GET", "/api/accounts/"+data.Account, "", "", 200)
	if strings.Contains(public.Body.String(), "birthday") || !strings.Contains(public.Body.String(), data.PublicEmail) {
		t.Fatal("public/private state lost", public.Body.String())
	}
	if e = f.service.VerifyEmail(f.ctx, data.PendingCode); e != nil {
		t.Fatal("pending verification did not survive", e)
	}
	list, e := f.service.ListContacts(f.ctx, f.token)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, c := range list {
		if c.ID == data.PendingContact {
			found = c.VerifiedAt != nil
		}
	}
	if !found {
		t.Fatal("verified contact missing")
	}
}
