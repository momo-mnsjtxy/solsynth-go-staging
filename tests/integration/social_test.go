// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/social"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func socialFixture(t *testing.T) (*sql.DB, *socialpg.Repository, []string) {
	t.Helper()
	db := database(t)
	for _, schema := range []string{authoritypg.Schema, accountpg.Schema, socialpg.Schema} {
		query(t, db, schema)
	}
	ids := []string{authorityUUID(t), authorityUUID(t), authorityUUID(t), authorityUUID(t)}
	for _, id := range ids {
		if e := authoritypg.SeedSynthetic(context.Background(), db, id, "social_"+strings.ReplaceAll(id, "-", ""), time.Now()); e != nil {
			t.Fatal(e)
		}
		query(t, db, `INSERT INTO owned_realm_quota(account_id,total,starts_at)VALUES($1,8,'2020-01-01')`, id)
	}
	return db, socialpg.New(db), ids
}
func socialRealm(t *testing.T, r *socialpg.Repository, actor string, public, community bool) social.Realm {
	t.Helper()
	slug := "r-" + authorityUUID(t)
	name := "Synthetic realm"
	v, e := r.CreateRealm(context.Background(), actor, social.RealmPatch{Slug: &slug, Name: &name, IsPublic: &public, IsCommunity: &community})
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestPGSocialRelationships(t *testing.T) {
	db, r, ids := socialFixture(t)
	ctx := context.Background()
	a, b, c := ids[0], ids[1], ids[2]
	if _, e := r.ChangeRelationship(ctx, a, a, social.RelationshipChange{Action: social.RequestFriend}); !errors.Is(e, social.ErrInvalid) {
		t.Fatal("self request", e)
	}
	var winners atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := r.ChangeRelationship(ctx, a, b, social.RelationshipChange{Action: social.RequestFriend})
			if e == nil {
				winners.Add(1)
			} else if !errors.Is(e, social.ErrConflict) {
				errs <- e
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	if winners.Load() != 1 {
		t.Fatal("duplicate request race", winners.Load())
	}
	requests, total, e := r.Relationships(ctx, b, true, 0, 20)
	if e != nil || total != 1 || len(requests) != 1 || requests[0].AccountID != a || requests[0].RelatedID != b {
		t.Fatal(requests, total, e)
	}
	if _, e = r.ChangeRelationship(ctx, c, a, social.RelationshipChange{Action: social.AcceptFriend}); !errors.Is(e, social.ErrNotFound) {
		t.Fatal("cross account acceptance", e)
	}
	if _, e = r.ChangeRelationship(ctx, a, b, social.RelationshipChange{Action: social.AcceptFriend}); !errors.Is(e, social.ErrNotFound) {
		t.Fatal("sender accepted own request", e)
	}
	if _, e = r.ChangeRelationship(ctx, b, a, social.RelationshipChange{Action: social.AcceptFriend}); e != nil {
		t.Fatal(e)
	}
	if _, e = r.ChangeRelationship(ctx, a, b, social.RelationshipChange{Action: social.SetClose}); e != nil {
		t.Fatal(e)
	}
	publisher := authorityUUID(t)
	query(t, db, `INSERT INTO owned_publishers(id,name,nick,type,account_id,created_at,updated_at)VALUES($1::uuid,$1::text,'Synthetic',0,$2,now(),now())`, publisher, a)
	visibility := func(viewer string, close bool, want bool) {
		t.Helper()
		tx, e := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback()
		got, e := r.CanReadFriendsInTransaction(ctx, tx, viewer, publisher, close)
		if e != nil || got != want {
			t.Fatalf("visibility actor %s close %v: %v %v", viewer, close, got, e)
		}
	}
	visibility(b, false, true)
	visibility(b, true, true)
	visibility(c, false, false)
	if _, e = r.ChangeRelationship(ctx, b, a, social.RelationshipChange{Action: social.UnsetClose}); e != nil {
		t.Fatal(e)
	}
	visibility(b, true, true)
	if _, e = r.ChangeRelationship(ctx, a, b, social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	visibility(b, false, false)
	visibility(b, true, false)
	if _, e = r.ChangeRelationship(ctx, b, a, social.RelationshipChange{Action: social.RequestFriend}); !errors.Is(e, social.ErrForbidden) {
		t.Fatal("blocked request", e)
	}
	if _, e = r.ChangeRelationship(ctx, b, a, social.RelationshipChange{Action: social.Forget}); e != nil {
		t.Fatal(e)
	}
	v, e := r.Relationship(ctx, a, b)
	if e != nil || v.Status != social.Blocked {
		t.Fatal("other account removed block", e)
	}
	if _, e = r.ChangeRelationship(ctx, a, b, social.RelationshipChange{Action: social.SetStatus, Status: social.Friend}); e != nil {
		t.Fatal(e)
	}
	visibility(b, false, false)
	if _, e = r.ChangeRelationship(ctx, b, a, social.RelationshipChange{Action: social.DeclineFriend}); e != nil {
		t.Fatal(e)
	}
	if _, e = r.ChangeRelationship(ctx, b, a, social.RelationshipChange{Action: social.AcceptFriend}); !errors.Is(e, social.ErrNotFound) {
		t.Fatal("declined request revived", e)
	}
	if _, e = r.ChangeRelationship(ctx, a, b, social.RelationshipChange{Action: social.RequestFriend}); e != nil {
		t.Fatal(e)
	}
	// Accept and block serialize on both account rows. No outcome may resurrect access.
	start := make(chan struct{})
	wg.Add(2)
	var acceptErr, blockErr error
	go func() {
		defer wg.Done()
		<-start
		_, acceptErr = r.ChangeRelationship(ctx, b, a, social.RelationshipChange{Action: social.AcceptFriend})
	}()
	go func() {
		defer wg.Done()
		<-start
		_, blockErr = r.ChangeRelationship(ctx, a, b, social.RelationshipChange{Action: social.Block})
	}()
	close(start)
	wg.Wait()
	if blockErr != nil {
		t.Fatal(blockErr)
	}
	if acceptErr != nil && !errors.Is(acceptErr, social.ErrNotFound) && !errors.Is(acceptErr, social.ErrForbidden) {
		t.Fatal(acceptErr)
	}
	visibility(b, false, false)
	if rows, total, e := r.Relationships(ctx, c, false, 0, 20); e != nil || len(rows) != 0 || total != 0 {
		t.Fatal("relationship isolation", rows, total, e)
	}
}
func TestPGSocialRealms(t *testing.T) {
	db, r, ids := socialFixture(t)
	ctx := context.Background()
	a, b, c, d := ids[0], ids[1], ids[2], ids[3]
	v := socialRealm(t, r, a, false, true)
	if _, e := r.GetRealm(ctx, "", v.Slug); !errors.Is(e, social.ErrNotFound) {
		t.Fatal("private anonymous detail", e)
	}
	if _, e := r.GetRealm(ctx, b, v.Slug); !errors.Is(e, social.ErrNotFound) {
		t.Fatal("private outsider detail", e)
	}
	if _, e := r.ChangeMember(ctx, b, v.Slug, social.MemberChange{Action: social.Invite, AccountID: c}); !errors.Is(e, social.ErrForbidden) {
		t.Fatal("outsider invite", e)
	}
	if _, e := r.ChangeMember(ctx, a, v.Slug, social.MemberChange{Action: social.Invite, AccountID: b, Role: social.Moderator}); !errors.Is(e, social.ErrForbidden) {
		t.Fatal("unboosted promotion", e)
	}
	invite, e := r.ChangeMember(ctx, a, v.Slug, social.MemberChange{Action: social.Invite, AccountID: b})
	if e != nil || invite.JoinedAt != nil {
		t.Fatal(invite, e)
	}
	if _, e := r.Me(ctx, b, v.Slug); !errors.Is(e, social.ErrForbidden) {
		t.Fatal("pending member authority", e)
	}
	if invites, e := r.Invites(ctx, b, 0, 20); e != nil || len(invites) != 1 || invites[0].Realm.ID != v.ID {
		t.Fatal("invite detail", invites, e)
	}
	if _, e = r.ChangeMember(ctx, b, v.Slug, social.MemberChange{Action: social.Decline}); e != nil {
		t.Fatal(e)
	}
	if _, e = r.ChangeMember(ctx, b, v.Slug, social.MemberChange{Action: social.Accept}); !errors.Is(e, social.ErrNotFound) {
		t.Fatal("declined invite revived", e)
	}
	if _, e = r.ChangeMember(ctx, a, v.Slug, social.MemberChange{Action: social.Invite, AccountID: b}); e != nil {
		t.Fatal(e)
	}
	if _, e = r.ChangeMember(ctx, b, v.Slug, social.MemberChange{Action: social.Accept}); e != nil {
		t.Fatal(e)
	}
	if e = r.Realm(ctx, wallet.Principal{AccountID: b, SessionID: "verified-by-bridge"}, v.ID, wallet.Viewer); e != nil {
		t.Fatal(e)
	}
	if e = r.Realm(ctx, wallet.Principal{AccountID: b, SessionID: "verified-by-bridge"}, v.ID, wallet.Editor); !errors.Is(e, wallet.ErrForbidden) {
		t.Fatal("member wallet editor", e)
	}
	query(t, db, `UPDATE owned_realms SET boost_level=2 WHERE id=$1`, v.ID)
	if _, e = r.ChangeMember(ctx, a, v.Slug, social.MemberChange{Action: social.ChangeRole, AccountID: b, Role: social.Moderator}); e != nil {
		t.Fatal(e)
	}
	for _, change := range []social.MemberChange{{Action: social.ChangeRole, AccountID: a, Role: social.Normal}, {Action: social.Remove, AccountID: a}, {Action: social.Invite, AccountID: c, Role: social.Moderator}, {Action: social.ChangeRole, AccountID: b, Role: social.Owner}} {
		if _, e = r.ChangeMember(ctx, b, v.Slug, change); !errors.Is(e, social.ErrForbidden) && !errors.Is(e, social.ErrInvalid) {
			t.Fatal("role escalation", change, e)
		}
	}
	if _, e = r.ChangeMember(ctx, b, v.Slug, social.MemberChange{Action: social.Invite, AccountID: c}); e != nil {
		t.Fatal(e)
	}
	if _, e = r.ChangeMember(ctx, b, v.Slug, social.MemberChange{Action: social.Leave}); e != nil {
		t.Fatal(e)
	}
	if _, e = r.ChangeMember(ctx, c, v.Slug, social.MemberChange{Action: social.Accept}); !errors.Is(e, social.ErrForbidden) {
		t.Fatal("departed inviter accepted", e)
	}
	rejoined, e := r.ChangeMember(ctx, b, v.Slug, social.MemberChange{Action: social.Join})
	if e != nil || rejoined.Role != social.Normal {
		t.Fatal("rejoin revived moderator", rejoined, e)
	}
	if _, e = r.ChangeMember(ctx, a, v.Slug, social.MemberChange{Action: social.Leave}); !errors.Is(e, social.ErrForbidden) {
		t.Fatal("owner left", e)
	}
	if _, e = r.ChangeRelationship(ctx, a, d, social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	if _, e = r.ChangeMember(ctx, d, v.Slug, social.MemberChange{Action: social.Join}); !errors.Is(e, social.ErrForbidden) {
		t.Fatal("blocked join", e)
	}
	publisher := authorityUUID(t)
	query(t, db, `INSERT INTO owned_publishers(id,name,nick,type,realm_id,created_at,updated_at)VALUES($1::uuid,$1::text,'Realm publisher',1,$2,now(),now())`, publisher, v.ID)
	if e = r.DeleteRealm(ctx, b, v.Slug); !errors.Is(e, social.ErrForbidden) {
		t.Fatal("nonowner delete", e)
	}
	if e = r.DeleteRealm(ctx, a, v.Slug); e != nil {
		t.Fatal(e)
	}
	var deleted bool
	if e = db.QueryRow(`SELECT deleted_at IS NOT NULL FROM owned_publishers WHERE id=$1`, publisher).Scan(&deleted); e != nil || !deleted {
		t.Fatal("realm publisher survived", e)
	}
	if _, e = r.ChangeMember(ctx, c, v.Slug, social.MemberChange{Action: social.Accept}); !errors.Is(e, social.ErrNotFound) {
		t.Fatal("deleted realm acceptance", e)
	}
	if e = r.Realm(ctx, wallet.Principal{AccountID: a, SessionID: "verified-by-bridge"}, v.ID, wallet.Viewer); !errors.Is(e, wallet.ErrForbidden) {
		t.Fatal("deleted realm wallet authority", e)
	}
	if _, e = r.CreateRealm(ctx, a, social.RealmPatch{Slug: &v.Slug, Name: &v.Name}); !errors.Is(e, social.ErrConflict) {
		t.Fatal("deleted realm name takeover", e)
	}
}
func TestPGSocialTransactionsAndHTTP(t *testing.T) {
	db, plain, ids := socialFixture(t)
	ctx := context.Background()
	a, b := ids[0], ids[1]
	now := &atomic.Int64{}
	now.Store(time.Now().Unix())
	dir := authorityKeys(t)
	issuer := newPGAuthority(t, db, dir, now)
	conf := authorityConfig(t, dir, now)
	verifier, e := authority.NewVerifier(authoritypg.New(db), authority.Config{Issuer: conf.Issuer, Audience: conf.Audience, TrustedKeys: conf.TrustedKeys, Now: conf.Now})
	if e != nil {
		t.Fatal(e)
	}
	if e = authoritypg.New(db).ReplacePolicy(ctx, a, []authority.Rule{{Pattern: "relationships.*"}, {Pattern: "realms.*"}}, nil); e != nil {
		t.Fatal(e)
	}
	pair := pgIssue(t, issuer, a)
	proof, e := verifier.Authenticate(ctx, pair.Token)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(db), verifier)
	repo := socialpg.NewWithAuthority(db, guard)
	bound := guard.Bind(ctx, proof)
	if _, e = repo.ChangeRelationship(ctx, a, b, social.RelationshipChange{Action: social.RequestFriend}); e == nil {
		t.Fatal("unbound context mutated")
	}
	handler := (&social.Service{Repository: repo, Authority: verifier}).Handler()
	call := func(method, route, body string) int {
		t.Helper()
		req := httptest.NewRequest(method, route, strings.NewReader(body)).WithContext(bound)
		req.Header.Set("Authorization", "Bearer "+pair.Token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := call("POST", "/stargate/relationships/"+b+"/friends", ""); got != 200 {
		t.Fatal("client request route", got)
	}
	if got := call("POST", "/stargate/relationships/"+b+"/block", `{"expires_in":"30m"}`); got != 501 {
		t.Fatal("finite block silently permanent", got)
	}
	if got := call("POST", "/stargate/relationships/"+b+"/block", `{"role":100}`); got != 400 {
		t.Fatal("unknown request property", got)
	}
	req := httptest.NewRequest("GET", "/api/realms", nil)
	req.Header.Set("X-Account-ID", a)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatal("identity header trusted", rec.Code)
	}
	req = httptest.NewRequest("GET", "/api/realms", nil)
	req.Header.Set("Authorization", "Bearer invalid")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatal("invalid auth downgrade", rec.Code)
	}
	// Deliberate audit failure must roll back both friendship directions and request removal.
	query(t, db, `ALTER TABLE owned_social_audit ADD CONSTRAINT social_test_rollback CHECK(action<>'relationships.accept') NOT VALID`)
	t.Cleanup(func() {
		_, _ = db.Exec(`ALTER TABLE owned_social_audit DROP CONSTRAINT IF EXISTS social_test_rollback`)
	})
	if _, e = plain.ChangeRelationship(ctx, b, a, social.RelationshipChange{Action: social.AcceptFriend}); e == nil {
		t.Fatal("expected audit rollback")
	}
	query(t, db, `ALTER TABLE owned_social_audit DROP CONSTRAINT social_test_rollback`)
	if _, e = plain.Relationship(ctx, a, b); !errors.Is(e, social.ErrNotFound) {
		t.Fatal("failed transaction left accepted edge", e)
	}
	if _, total, e := plain.Relationships(ctx, b, true, 0, 20); e != nil || total != 1 {
		t.Fatal("failed accept lost request", e)
	}
	// A policy change that commits while the writer waits must be observed by its
	// transaction guard; the HTTP check alone is deliberately insufficient.
	locked, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = locked.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, a); e != nil {
		t.Fatal(e)
	}
	result := make(chan error, 1)
	go func() {
		_, e := repo.ChangeRelationship(bound, a, b, social.RelationshipChange{Action: social.Block})
		result <- e
	}()
	if _, e = locked.ExecContext(ctx, `DELETE FROM owned_authority_grants WHERE account_id=$1`, a); e != nil {
		t.Fatal(e)
	}
	if e = locked.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-result; !errors.Is(e, authority.ErrForbidden) {
		t.Fatal("stale permission authorized", e)
	}
	if _, e = plain.Relationship(ctx, a, b); !errors.Is(e, social.ErrNotFound) {
		t.Fatal("denied mutation committed", e)
	}
}

// After is read-only and never installs schema or repairs rows. The CI harness
// must stop and restart PostgreSQL between these two invocations.
func TestPGSocialRestartPersistence(t *testing.T) {
	db := database(t)
	ctx := context.Background()
	r := socialpg.New(db)
	const a = "c0500000-0000-4000-8000-000000000001"
	const b = "c0500000-0000-4000-8000-000000000002"
	const slug = "social-restart-realm"
	switch os.Getenv("PG_TEST_PHASE") {
	case "before":
		query(t, db, socialpg.Schema)
		for _, id := range []string{a, b} {
			if e := authoritypg.SeedSynthetic(ctx, db, id, "restart_"+strings.ReplaceAll(id, "-", ""), time.Now()); e != nil {
				t.Fatal(e)
			}
		}
		query(t, db, `INSERT INTO owned_realm_quota(account_id,total,starts_at)VALUES($1,1,'2020-01-01')`, a)
		name := "Restart realm"
		s := slug
		v, e := r.CreateRealm(ctx, a, social.RealmPatch{Slug: &s, Name: &name})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = r.ChangeMember(ctx, a, v.Slug, social.MemberChange{Action: social.Invite, AccountID: b}); e != nil {
			t.Fatal(e)
		}
		if _, e = r.ChangeMember(ctx, b, v.Slug, social.MemberChange{Action: social.Accept}); e != nil {
			t.Fatal(e)
		}
		if _, e = r.ChangeRelationship(ctx, a, b, social.RelationshipChange{Action: social.RequestFriend}); e != nil {
			t.Fatal(e)
		}
		if _, e = r.ChangeRelationship(ctx, b, a, social.RelationshipChange{Action: social.AcceptFriend}); e != nil {
			t.Fatal(e)
		}
		if _, e = r.ChangeRelationship(ctx, a, b, social.RelationshipChange{Action: social.SetClose}); e != nil {
			t.Fatal(e)
		}
	case "after":
	default:
		t.Fatal("PG_TEST_PHASE must be before or after")
	}
	rel, e := r.Relationship(ctx, a, b)
	if e != nil || rel.Status != social.CloseFriend {
		t.Fatal("durable close friendship missing", rel, e)
	}
	reverse, e := r.Relationship(ctx, b, a)
	if e != nil || reverse.Status != social.Friend {
		t.Fatal("directionality changed", reverse, e)
	}
	member, e := r.Me(ctx, b, slug)
	if e != nil || member.JoinedAt == nil || member.Role != social.Normal {
		t.Fatal("durable membership missing", member, e)
	}
	owner, e := r.Me(ctx, a, slug)
	if e != nil || owner.Role != social.Owner {
		t.Fatal("durable owner missing", owner, e)
	}
}

func TestPGSocialRealmWriteRevocationOrdering(t *testing.T) {
	db, r, ids := socialFixture(t)
	ctx := context.Background()
	a, b := ids[0], ids[1]
	v := socialRealm(t, r, a, true, true)
	if _, e := r.ChangeMember(ctx, b, v.ID, social.MemberChange{Action: social.Join}); e != nil {
		t.Fatal(e)
	}
	revoker, e := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		t.Fatal(e)
	}
	defer revoker.Rollback()
	if _, e = revoker.ExecContext(ctx, `SELECT id FROM owned_realms WHERE id=$1 FOR UPDATE`, v.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = revoker.ExecContext(ctx, `UPDATE owned_realm_members SET leave_at=statement_timestamp() WHERE realm_id=$1 AND account_id=$2`, v.ID, b); e != nil {
		t.Fatal(e)
	}
	writer, e := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		t.Fatal(e)
	}
	defer writer.Rollback()
	var pid int
	if e = writer.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	result := make(chan error, 1)
	go func() { _, e := r.RealmRoleForWriteInTransaction(ctx, writer, b, v.ID); result <- e }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if e = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock')`, pid).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("realm writer did not wait on membership lifecycle lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if e = revoker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-result; !errors.Is(e, social.ErrForbidden) {
		t.Fatal("realm write retained revoked membership", e)
	}
}
