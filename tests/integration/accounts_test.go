// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/passport/accounts"
	"strings"
	"sync"
	"testing"
)

func accountDB(t *testing.T) *sql.DB {
	t.Helper()
	db := database(t)
	query(t, db, authoritypg.Schema)
	query(t, db, accountpg.Schema)
	return db
}
func accountID(n int) string { return fmt.Sprintf("ac100000-0000-4000-8000-%012d", n) }
func accountFixture(t *testing.T, db *sql.DB, n int) string {
	t.Helper()
	id := accountID(n)
	query(t, db, `INSERT INTO owned_authority_accounts(id,name,nick,created_at) VALUES($1,$2,$3,CURRENT_TIMESTAMP) ON CONFLICT(id) DO NOTHING`, id, fmt.Sprintf("account_fixture_%d", n), "Fixture")
	return id
}
func sp(s string) *string { return &s }
func TestPGAccountsPublisherLifecycle(t *testing.T) {
	db := accountDB(t)
	r := accountpg.New(db)
	ctx := context.Background()
	owner := accountFixture(t, db, 1)
	editor := accountFixture(t, db, 2)
	manager := accountFixture(t, db, 3)
	viewer := accountFixture(t, db, 4)
	other := accountFixture(t, db, 5)
	a, e := r.LookupAccount(ctx, "ACCOUNT_FIXTURE_1")
	if e != nil || a.ID != owner || a.Profile != nil {
		t.Fatalf("real lookup/optional profile: %+v %v", a, e)
	}
	a, e = r.UpdateProfile(ctx, owner, accounts.ProfilePatch{Nick: sp("Updated"), Bio: sp("Owned profile"), Links: &[]accounts.Link{{Name: "Site", URL: "https://example.test"}}})
	if e != nil || a.Profile == nil || a.Profile.Bio != "Owned profile" || a.Profile.ID == owner {
		t.Fatalf("profile: %+v %v", a, e)
	}
	reloaded, e := accountpg.New(database(t)).LookupAccount(ctx, owner)
	if e != nil || reloaded.Profile == nil || reloaded.Profile.ID != a.Profile.ID || reloaded.Nick != "Updated" {
		t.Fatal("persisted profile missing", e)
	}
	if _, e = r.UpdateProfile(ctx, owner, accounts.ProfilePatch{Name: sp("free")}); !errors.Is(e, accounts.ErrNameChange) {
		t.Fatal("paid name gate", e)
	}
	patch := accounts.PublisherPatch{Name: sp("account-lifecycle"), Nick: sp("Account lifecycle")}
	if _, e = r.CreatePublisher(ctx, owner, patch); !errors.Is(e, accounts.ErrPolicy) {
		t.Fatal("implicit quota", e)
	}
	query(t, db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at) VALUES($1,2,CURRENT_TIMESTAMP)`, owner)
	pub, e := r.CreatePublisher(ctx, owner, patch)
	if e != nil || pub.ID == owner || pub.AccountID == nil || *pub.AccountID != owner || pub.Account == nil {
		t.Fatalf("distinct publisher: %+v %v", pub, e)
	}
	if got, e := r.Resolve(ctx, owner, ""); e != nil || got != pub.ID {
		t.Fatal("default only publisher", got, e)
	}
	if _, e = r.CreatePublisher(ctx, owner, patch); !errors.Is(e, accounts.ErrConflict) {
		t.Fatal("duplicate name", e)
	}
	member, e := r.ChangeMember(ctx, owner, pub.ID, accounts.MemberChange{Action: accounts.InviteMember, AccountID: editor, Role: accounts.Editor})
	if e != nil || member.JoinedAt != nil {
		t.Fatal("invite", e)
	}
	if _, e = r.Resolve(ctx, editor, pub.Name); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("pending invite authorized", e)
	}
	if list, e := r.ListInvites(ctx, editor); e != nil || len(list) != 1 || list[0].Publisher == nil {
		t.Fatal("invites", e)
	}
	if _, e = r.ChangeMember(ctx, editor, pub.ID, accounts.MemberChange{Action: accounts.AcceptInvite, AccountID: other}); e != nil {
		t.Fatal("accept self", e)
	}
	if _, e = r.Membership(ctx, pub.ID, other); !errors.Is(e, accounts.ErrNotFound) {
		t.Fatal("target injection", e)
	}
	if got, e := r.Resolve(ctx, editor, "ACCOUNT-LIFECYCLE"); e != nil || got != pub.ID {
		t.Fatal("joined editor resolution", got, e)
	}
	if e = r.RequireManager(ctx, editor, pub.ID); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("editor got manager", e)
	}
	if _, e = r.UpdatePublisher(ctx, editor, pub.ID, accounts.PublisherPatch{Nick: sp("hijack")}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("editor changed profile", e)
	}
	for _, v := range []struct {
		id   string
		role accounts.Role
	}{{manager, accounts.Manager}, {viewer, accounts.Viewer}} {
		if _, e = r.ChangeMember(ctx, owner, pub.ID, accounts.MemberChange{Action: accounts.InviteMember, AccountID: v.id, Role: v.role}); e != nil {
			t.Fatal(e)
		}
		if _, e = r.ChangeMember(ctx, v.id, pub.ID, accounts.MemberChange{Action: accounts.AcceptInvite}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = r.ChangeMember(ctx, manager, pub.ID, accounts.MemberChange{Action: accounts.UpdateRole, AccountID: owner, Role: accounts.Viewer}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("owner demotion", e)
	}
	if _, e = r.ChangeMember(ctx, owner, pub.ID, accounts.MemberChange{Action: accounts.RemoveMember, AccountID: owner}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("owner removal", e)
	}
	if _, e = r.ChangeMember(ctx, manager, pub.ID, accounts.MemberChange{Action: accounts.InviteMember, AccountID: other, Role: accounts.Owner}); !errors.Is(e, accounts.ErrInvalid) {
		t.Fatal("new owner invite", e)
	}
	if _, e = r.ChangeMember(ctx, owner, pub.ID, accounts.MemberChange{Action: accounts.InviteMember, AccountID: other, Role: accounts.Viewer}); e != nil {
		t.Fatal("decline fixture", e)
	}
	if _, e = r.ChangeMember(ctx, other, pub.ID, accounts.MemberChange{Action: accounts.DeclineInvite, AccountID: editor}); e != nil {
		t.Fatal("decline own invite", e)
	}
	if invites, e := r.ListInvites(ctx, other); e != nil || len(invites) != 0 {
		t.Fatal("declined invite retained", e)
	}
	if _, e = r.ChangeMember(ctx, owner, pub.ID, accounts.MemberChange{Action: accounts.InviteMember, AccountID: other, Role: accounts.Viewer}); e != nil {
		t.Fatal("reinvite revoked member", e)
	}
	if _, e = r.ChangeMember(ctx, viewer, pub.ID, accounts.MemberChange{Action: accounts.InviteMember, AccountID: other, Role: accounts.Viewer}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("viewer invite", e)
	}
	members, total, e := r.ListMembers(ctx, pub.ID, 0, 2)
	if e != nil || len(members) != 2 || total != 4 || members[0].Account == nil {
		t.Fatalf("pagination real counts: %d %d %v", len(members), total, e)
	}
	if _, e = r.PublisherEntitlement(ctx, pub.ID); !errors.Is(e, accounts.ErrPolicy) {
		t.Fatal("invented entitlement", e)
	}
	query(t, db, `INSERT INTO owned_publisher_entitlements(publisher_id,protected_tag_total,level,perk_level,starts_at) VALUES($1,7,2,1,CURRENT_TIMESTAMP)`, pub.ID)
	en, e := r.PublisherEntitlement(ctx, pub.ID)
	if e != nil || en.Total != 7 || en.Level != 2 {
		t.Fatal(en, e)
	}
	second, e := r.CreatePublisher(ctx, owner, accounts.PublisherPatch{Name: sp("account-second"), Nick: sp("Second")})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.Resolve(ctx, owner, ""); !errors.Is(e, accounts.ErrAmbiguous) {
		t.Fatal("arbitrary default", e)
	}
	query(t, db, `INSERT INTO owned_publishing_settings(account_id,default_publisher_id) VALUES($1,$2)`, owner, second.ID)
	if id, e := r.Resolve(ctx, owner, ""); e != nil || id != second.ID {
		t.Fatal("persisted default", id, e)
	}
	if e = r.DeletePublisher(ctx, manager, pub.ID); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("manager delete", e)
	}
	if _, e = r.ChangeMember(ctx, editor, pub.ID, accounts.MemberChange{Action: accounts.LeavePublisher}); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Resolve(ctx, editor, pub.Name); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("removed membership authorized", e)
	}
	if e = r.DeletePublisher(ctx, owner, pub.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = r.LookupPublisher(ctx, pub.ID); !errors.Is(e, accounts.ErrNotFound) {
		t.Fatal("soft deleted lookup", e)
	}
	if _, e = r.CreatePublisher(ctx, owner, patch); !errors.Is(e, accounts.ErrConflict) {
		t.Fatal("deleted name not reserved", e)
	}
	if _, e = r.ChangeMember(ctx, owner, second.ID, accounts.MemberChange{Action: accounts.InviteMember, AccountID: other, Role: accounts.Viewer}); e != nil {
		t.Fatal("inactive owner invite fixture", e)
	}
	query(t, db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, owner)
	if invites, e := r.ListInvites(ctx, other); e != nil || len(invites) != 0 {
		t.Fatal("inactive publisher visible in invites", e)
	}
	if _, e = r.LookupAccount(ctx, owner); !errors.Is(e, accounts.ErrNotFound) {
		t.Fatal("disabled public account", e)
	}
	if _, e = r.LookupPublisher(ctx, second.ID); !errors.Is(e, accounts.ErrNotFound) {
		t.Fatal("disabled owner publisher", e)
	}
	if _, e = r.UpdateProfile(ctx, owner, accounts.ProfilePatch{Nick: sp("disabled")}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("disabled update", e)
	}
}

// The shared integration runner invokes this again after an actual database
// restart. It never initializes fixtures in the after phase.
func TestPGAccountsRestartPersistence(t *testing.T) {
	db := accountDB(t)
	r := accountpg.New(db)
	ctx := context.Background()
	owner := accountID(40)
	if os.Getenv("PG_TEST_PHASE") != "after" {
		accountFixture(t, db, 40)
		query(t, db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at) VALUES($1,1,CURRENT_TIMESTAMP)`, owner)
		if _, e := r.UpdateProfile(ctx, owner, accounts.ProfilePatch{Bio: sp("restart profile")}); e != nil {
			t.Fatal(e)
		}
		if _, e := r.CreatePublisher(ctx, owner, accounts.PublisherPatch{Name: sp("account-restart"), Nick: sp("Restart publication")}); e != nil {
			t.Fatal(e)
		}
	}
	a, e := r.LookupAccount(ctx, owner)
	if e != nil || a.Profile == nil || a.Profile.Bio != "restart profile" {
		t.Fatal("restart profile missing", e)
	}
	pub, e := r.LookupPublisher(ctx, "account-restart")
	if e != nil || pub.ID == owner || pub.AccountID == nil || *pub.AccountID != owner {
		t.Fatal("restart publisher identity missing", e)
	}
	m, e := r.Membership(ctx, pub.ID, owner)
	if e != nil || m.JoinedAt == nil || m.Role != accounts.Owner {
		t.Fatal("restart owner membership missing", e)
	}
	var count int
	if e = db.QueryRow(`SELECT count(*) FROM owned_accounts_audit WHERE actor_id=$1`, owner).Scan(&count); e != nil || count != 2 {
		t.Fatal("restart audit missing", count, e)
	}
}

func TestPGAccountsProfileConcurrencyAndCanonicalLookup(t *testing.T) {
	db := accountDB(t)
	r := accountpg.New(db)
	ctx := context.Background()
	actor := accountFixture(t, db, 50)
	shadow := accountFixture(t, db, 51)
	query(t, db, `UPDATE owned_authority_accounts SET name=$2 WHERE id=$1`, shadow, actor)
	if a, e := r.LookupAccount(ctx, actor); e != nil || a.ID != actor {
		t.Fatal("UUID reference selected a username", a.ID, e)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, patch := range []accounts.ProfilePatch{{Bio: sp("concurrent bio")}, {Nick: sp("Concurrent nick")}} {
		go func(p accounts.ProfilePatch) {
			<-start
			_, e := r.UpdateProfile(ctx, actor, p)
			results <- e
		}(patch)
	}
	close(start)
	for range 2 {
		if e := <-results; e != nil {
			t.Fatal(e)
		}
	}
	a, e := r.LookupAccount(ctx, actor)
	if e != nil || a.Nick != "Concurrent nick" || a.Profile == nil || a.Profile.Bio != "concurrent bio" {
		t.Fatal("concurrent partial update lost data", a, e)
	}
}
func TestPGAccountsAtomicRollbackAndQuotaRace(t *testing.T) {
	db := accountDB(t)
	r := accountpg.New(db)
	ctx := context.Background()
	owner := accountFixture(t, db, 20)
	query(t, db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at) VALUES($1,1,CURRENT_TIMESTAMP)`, owner)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("account-race-%d", i)
			_, e := r.CreatePublisher(ctx, owner, accounts.PublisherPatch{Name: &name, Nick: sp("Race")})
			results <- e
		}(i)
	}
	wg.Wait()
	close(results)
	won := 0
	for e := range results {
		if e == nil {
			won++
		} else if !errors.Is(e, accounts.ErrQuota) {
			t.Fatal(e)
		}
	}
	if won != 1 {
		t.Fatalf("quota winners %d", won)
	}
	query(t, db, `CREATE FUNCTION owned_accounts_test_reject_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.actor_id='ac100000-0000-4000-8000-000000000020' THEN RAISE EXCEPTION 'synthetic audit failure'; END IF; RETURN NEW; END $$`)
	query(t, db, `CREATE TRIGGER owned_accounts_test_reject_audit BEFORE INSERT ON owned_accounts_audit FOR EACH ROW EXECUTE FUNCTION owned_accounts_test_reject_audit()`)
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP TRIGGER IF EXISTS owned_accounts_test_reject_audit ON owned_accounts_audit;DROP FUNCTION IF EXISTS owned_accounts_test_reject_audit()`)
	})
	if _, e := r.UpdateProfile(ctx, owner, accounts.ProfilePatch{Nick: sp("Must rollback"), Bio: sp("Must rollback")}); e == nil {
		t.Fatal("audit failure ignored")
	}
	a, e := r.LookupAccount(ctx, owner)
	if e != nil || a.Nick != "Fixture" || a.Profile != nil {
		t.Fatal("partial profile mutation", a, e)
	}
}
func TestPGAccountsProcessPersistence(t *testing.T) {
	if os.Getenv("SOLSYNTH_ACCOUNT_RESTART_CHILD") == "1" {
		db := accountDB(t)
		a, e := accountpg.New(db).LookupAccount(context.Background(), accountID(30))
		if e != nil || a.Profile == nil || a.Profile.Bio != "process sentinel" {
			t.Fatal("fresh process profile missing", e)
		}
		return
	}
	db := accountDB(t)
	id := accountFixture(t, db, 30)
	if _, e := accountpg.New(db).UpdateProfile(context.Background(), id, accounts.ProfilePatch{Bio: sp("process sentinel")}); e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestPGAccountsProcessPersistence$")
	cmd.Env = append(os.Environ(), "SOLSYNTH_ACCOUNT_RESTART_CHILD=1")
	b, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("fresh process verification failed: %v %s", e, strings.TrimSpace(string(b)))
	}
}
