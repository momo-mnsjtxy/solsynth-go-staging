// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/presencepg"
	"solsynth.local/go-migration/internal/profilemediapg"
	"solsynth.local/go-migration/internal/socialpg"
	fed "solsynth.local/go-migration/ports/federation"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/presence"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/posts"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func expiryString(s string) *string { return &s }
func expiryInt(v int) *int          { return &v }
func expiryChange(t *testing.T, r *socialpg.Repository, a, b string, c social.RelationshipChange) social.Relationship {
	t.Helper()
	v, e := r.ChangeRelationship(context.Background(), a, b, c)
	if e != nil {
		t.Fatal(c, e)
	}
	return v
}
func expiryEvents(t *testing.T, db *sql.DB, id string) int {
	t.Helper()
	var n int
	if e := db.QueryRow(`SELECT count(*) FROM owned_social_expiry_events WHERE source_id=$1`, id).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func TestPGRelationshipExpiryImmediateDegradeAndCleanup(t *testing.T) {
	db, r, ids := socialFixture(t)
	a, b := ids[0], ids[1]
	ctx := context.Background()
	v := expiryChange(t, r, a, b, social.RelationshipChange{Action: social.Block, ExpiresIn: expiryString("1h"), DegradeTo: expiryInt(social.Muted)})
	if v.ExpiredAt == nil {
		t.Fatal("missing authored expiry projection")
	}
	query(t, db, `UPDATE owned_social_relationships SET expired_at=clock_timestamp()-interval '1 second' WHERE id=$1`, v.ID)
	effective, e := r.Relationship(ctx, a, b)
	if e != nil || effective.Status != social.Muted || effective.ExpiredAt != nil {
		t.Fatal("late worker retained stale block", effective, e)
	}
	var raw int
	if e = db.QueryRow(`SELECT status FROM owned_social_relationships WHERE id=$1`, v.ID).Scan(&raw); e != nil || raw != social.Blocked {
		t.Fatal(raw, e)
	}
	if _, e = r.ProcessExpiredRelationships(ctx, 1000); e != nil {
		t.Fatal(e)
	}
	if expiryEvents(t, db, v.ID) != 1 {
		t.Fatal("missing degradation event")
	}
	if _, e = r.ProcessExpiredRelationships(ctx, 1000); e != nil {
		t.Fatal(e)
	}
	if expiryEvents(t, db, v.ID) != 1 {
		t.Fatal("expiry replay duplicated event")
	}
	var next sql.NullInt64
	var owner, target string
	if e = db.QueryRow(`SELECT account_id::text,related_id::text,next_status FROM owned_social_expiry_events WHERE source_id=$1`, v.ID).Scan(&owner, &target, &next); e != nil || owner != a || target != b || !next.Valid || next.Int64 != social.Muted {
		t.Fatal(owner, target, next, e)
	}
	// A permanent request replaces a timer without inheriting its degradation.
	v = expiryChange(t, r, a, b, social.RelationshipChange{Action: social.Block})
	if v.ExpiredAt != nil {
		t.Fatal("omitted expiry did not retain permanent block")
	}
	if _, e = r.ProcessExpiredRelationships(ctx, 0); !errors.Is(e, social.ErrInvalid) {
		t.Fatal(e)
	}
}
func TestPGRelationshipExpiryRequestsAndLegacyNull(t *testing.T) {
	db, r, ids := socialFixture(t)
	ctx := context.Background()
	a, b, c, d := ids[0], ids[1], ids[2], ids[3]
	request := expiryChange(t, r, a, b, social.RelationshipChange{Action: social.RequestFriend})
	if request.ExpiredAt == nil || request.ExpiredAt.Sub(request.CreatedAt) != 7*24*time.Hour {
		t.Fatal("new request lifetime", request)
	}
	query(t, db, `UPDATE owned_social_requests SET expired_at=clock_timestamp()-interval '1 second' WHERE id=$1`, request.ID)
	if _, e := r.ChangeRelationship(ctx, b, a, social.RelationshipChange{Action: social.AcceptFriend}); !errors.Is(e, social.ErrNotFound) {
		t.Fatal("expired request accepted", e)
	}
	replacement := expiryChange(t, r, a, b, social.RelationshipChange{Action: social.RequestFriend})
	if replacement.ID == request.ID || expiryEvents(t, db, request.ID) != 1 {
		t.Fatal("request renewal reused expired intent")
	}
	legacy := authorityUUID(t)
	query(t, db, `INSERT INTO owned_social_requests(id,account_id,related_id,created_at)VALUES($1,$2,$3,clock_timestamp()-interval '30 days')`, legacy, c, d)
	rows, _, e := r.Relationships(ctx, c, true, 0, 100)
	if e != nil {
		t.Fatal(e)
	}
	if len(rows) != 1 || rows[0].ID != legacy || rows[0].ExpiredAt != nil {
		t.Fatal("existing no-expiry request changed", rows)
	}
	expiryChange(t, r, d, c, social.RelationshipChange{Action: social.AcceptFriend})
}
func TestPGRelationshipExpiryActorIsolationAndAtomicEvents(t *testing.T) {
	db, r, ids := socialFixture(t)
	ctx := context.Background()
	a, b := ids[0], ids[1]
	expiryChange(t, r, a, b, social.RelationshipChange{Action: social.RequestFriend})
	expiryChange(t, r, a, b, social.RelationshipChange{Action: social.Mute, ExpiresIn: expiryString("30m")})
	if _, e := r.ChangeRelationship(ctx, b, a, social.RelationshipChange{Action: social.AcceptFriend}); !errors.Is(e, social.ErrNotFound) {
		t.Fatal("stale request overwrote sender mute", e)
	}
	expiryChange(t, r, a, b, social.RelationshipChange{Action: social.Forget})
	expiryChange(t, r, b, a, social.RelationshipChange{Action: social.Block})
	timed := expiryChange(t, r, a, b, social.RelationshipChange{Action: social.Block, ExpiresIn: expiryString("30m")})
	query(t, db, `UPDATE owned_social_relationships SET expired_at=clock_timestamp()-interval '1 second' WHERE id=$1`, timed.ID)
	constraint := "expiry_fail_" + strings.ReplaceAll(a, "-", "")
	query(t, db, `ALTER TABLE owned_social_expiry_events ADD CONSTRAINT `+constraint+` CHECK(account_id<>'`+a+`'::uuid) NOT VALID`)
	if _, e := r.ProcessExpiredRelationships(ctx, 1000); e == nil {
		t.Fatal("event failure accepted")
	}
	query(t, db, `ALTER TABLE owned_social_expiry_events DROP CONSTRAINT `+constraint)
	var exists bool
	if e := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM owned_social_relationships WHERE id=$1)`, timed.ID).Scan(&exists); e != nil || !exists || expiryEvents(t, db, timed.ID) != 0 {
		t.Fatal("cleanup partially committed", exists, e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := r.ProcessExpiredRelationships(ctx, 1000); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if expiryEvents(t, db, timed.ID) != 1 {
		t.Fatal("concurrent cleanup duplicated event")
	}
	reverse, e := r.Relationship(ctx, b, a)
	if e != nil || reverse.Status != social.Blocked || reverse.ExpiredAt != nil {
		t.Fatal("other person's permanent block changed", reverse, e)
	}
}
func expiryLockPair(t *testing.T, db *sql.DB, a, b string) *sql.Tx {
	t.Helper()
	tx, e := db.BeginTx(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	ids := []string{a, b}
	sort.Strings(ids)
	for _, id := range ids {
		var held string
		if e = tx.QueryRow(`SELECT id::text FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, id).Scan(&held); e != nil {
			t.Fatal(e)
		}
	}
	return tx
}
func expiryWaitLock(t *testing.T, db *sql.DB, pid int) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if e := db.QueryRow(`SELECT COALESCE(wait_event_type='Lock',false) FROM pg_stat_activity WHERE pid=$1`, pid).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("relationship operation never reached canonical account lock")
		case <-ticker.C:
		}
	}
}
func TestPGRelationshipExpiryWaitObservesRenewalAndRequestDeadline(t *testing.T) {
	db, r, ids := socialFixture(t)
	ctx := context.Background()
	a, b := ids[0], ids[1]
	if _, e := r.ProcessExpiredRelationships(ctx, 1000); e != nil {
		t.Fatal(e)
	}
	v := expiryChange(t, r, a, b, social.RelationshipChange{Action: social.Block, ExpiresIn: expiryString("1h")})
	query(t, db, `UPDATE owned_social_relationships SET expired_at=clock_timestamp()-interval '1 second' WHERE id=$1`, v.ID)
	other := database(t)
	other.SetMaxOpenConns(1)
	var pid int
	if e := other.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	worker := socialpg.New(other)
	lock := expiryLockPair(t, db, a, b)
	defer lock.Rollback()
	done := make(chan error, 1)
	go func() { _, e := worker.ProcessExpiredRelationships(ctx, 1000); done <- e }()
	expiryWaitLock(t, db, pid)
	if _, e := lock.Exec(`UPDATE owned_social_relationships SET expired_at=NULL,degrade_to=NULL,policy_revision=nextval('owned_social_policy_revision') WHERE id=$1`, v.ID); e != nil {
		t.Fatal(e)
	}
	if e := lock.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	renewed, e := r.Relationship(ctx, a, b)
	if e != nil || renewed.Status != social.Blocked || renewed.ExpiredAt != nil || expiryEvents(t, db, v.ID) != 0 {
		t.Fatal("stale worker removed renewed policy", renewed, e)
	}
	expiryChange(t, r, a, b, social.RelationshipChange{Action: social.Forget})
	request := expiryChange(t, r, a, b, social.RelationshipChange{Action: social.RequestFriend})
	lock = expiryLockPair(t, db, a, b)
	defer lock.Rollback()
	go func() {
		_, e := worker.ChangeRelationship(ctx, b, a, social.RelationshipChange{Action: social.AcceptFriend})
		done <- e
	}()
	expiryWaitLock(t, db, pid)
	if _, e = lock.Exec(`UPDATE owned_social_requests SET expired_at=clock_timestamp()-interval '1 second',policy_revision=nextval('owned_social_policy_revision') WHERE id=$1`, request.ID); e != nil {
		t.Fatal(e)
	}
	if e = lock.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, social.ErrNotFound) {
		t.Fatal("waited accept used old deadline", e)
	}
}
func TestPGRelationshipExpiryCurrentVisibilityAcrossDomains(t *testing.T) {
	f := newChatRelationsFixture(t)
	room := relationDirect(t, f)
	if _, e := f.chats.ChangeDirect(f.contexts[f.other], f.other, room.ID, "confirm"); e != nil {
		t.Fatal(e)
	}
	r := f.social
	v, e := r.ChangeRelationship(f.contexts[f.owner], f.owner, f.other, social.RelationshipChange{Action: social.Block, ExpiresIn: expiryString("1h")})
	if e != nil {
		t.Fatal(e)
	}
	profile := accountpg.NewWithAuthority(f.db, f.guard).WithProfileMedia(profilemediapg.New(f.db, nil))
	status := presencepg.NewWithAuthority(f.db, f.guard)
	if _, e = profile.LookupAccountForViewer(f.contexts[f.other], f.other, f.owner); !errors.Is(e, accounts.ErrNotFound) {
		t.Fatal("blocked profile exposed", e)
	}
	if _, e = status.GetStatus(f.contexts[f.other], f.other, f.owner); !errors.Is(e, presence.ErrNotFound) {
		t.Fatal("blocked presence exposed", e)
	}
	if _, e = f.chats.GetDirect(f.contexts[f.other], f.other, f.owner); e == nil {
		t.Fatal("blocked chat exposed")
	}
	query(t, f.db, `UPDATE owned_social_relationships SET expired_at=clock_timestamp()-interval '1 second' WHERE id=$1`, v.ID)
	// No cleanup invocation: all readers must use the same current-time policy.
	if _, e = profile.LookupAccountForViewer(f.contexts[f.other], f.other, f.owner); e != nil {
		t.Fatal("expired block retained by profile", e)
	}
	if _, e = status.GetStatus(f.contexts[f.other], f.other, f.owner); e != nil {
		t.Fatal("expired block retained by presence", e)
	}
	if _, e = f.chats.GetDirect(f.contexts[f.other], f.other, f.owner); e != nil {
		t.Fatal("expired block retained by chat", e)
	}
	var friendship bool
	if e = f.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM owned_social_effective_relationships WHERE account_id=$1 AND related_id=$2 AND status>=100)`, f.owner, f.other).Scan(&friendship); e != nil || friendship {
		t.Fatal("expiry restored friendship", friendship, e)
	}
	if _, e = r.ProcessExpiredRelationships(context.Background(), 1000); e != nil {
		t.Fatal(e)
	}
}
func TestPGRelationshipExpiryDoesNotExpandFederationAudience(t *testing.T) {
	x := newFederationFixture(t, true)
	peer := authorityUUID(t)
	if e := authoritypg.SeedSynthetic(context.Background(), x.db, peer, "synthetic_expiry_federation_"+strings.ReplaceAll(peer, "-", ""), time.Now()); e != nil {
		t.Fatal(e)
	}
	r := socialpg.New(x.db)
	v := expiryChange(t, r, x.actor, peer, social.RelationshipChange{Action: social.Block, ExpiresIn: expiryString("1h")})
	private := x.makePost(t, "Private expiry fixture", posts.Private, false)
	public := x.makePost(t, "Public expiry fixture", posts.Public, false)
	query(t, x.db, `UPDATE owned_social_relationships SET expired_at=clock_timestamp()-interval '1 second' WHERE id=$1`, v.ID)
	if _, e := x.r.Object(x.ctx, private.ID); !errors.Is(e, fed.ErrNotFound) {
		t.Fatal("expiry expanded federation audience", e)
	}
	if _, e := x.r.Object(x.ctx, public.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := r.ProcessExpiredRelationships(context.Background(), 1000); e != nil {
		t.Fatal(e)
	}
}
func expirySnapshot(t *testing.T, db *sql.DB, ids []string) json.RawMessage {
	t.Helper()
	var raw []byte
	e := db.QueryRow(`SELECT jsonb_build_object('relationships',(SELECT COALESCE(jsonb_agg(to_jsonb(r)ORDER BY id),'[]'::jsonb)FROM owned_social_relationships r WHERE account_id IN($1,$2,$3,$4)),'requests',(SELECT COALESCE(jsonb_agg(to_jsonb(r)ORDER BY id),'[]'::jsonb)FROM owned_social_requests r WHERE account_id IN($1,$2,$3,$4)),'events',(SELECT COALESCE(jsonb_agg(to_jsonb(r)ORDER BY id),'[]'::jsonb)FROM owned_social_expiry_events r WHERE account_id IN($1,$2,$3,$4)))`, ids[0], ids[1], ids[2], ids[3]).Scan(&raw)
	if e != nil {
		t.Fatal(e)
	}
	return raw
}
func TestPGRelationshipExpiryRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	var saved struct {
		Accounts                   []string
		Permanent, Timed, Degraded string
		Snapshot                   json.RawMessage
	}
	if phase == "before" {
		db, r, ids := socialFixture(t)
		saved.Accounts = ids
		saved.Permanent = expiryChange(t, r, ids[0], ids[1], social.RelationshipChange{Action: social.Block}).ID
		saved.Timed = expiryChange(t, r, ids[1], ids[2], social.RelationshipChange{Action: social.Mute, ExpiresIn: expiryString("30d")}).ID
		saved.Degraded = expiryChange(t, r, ids[2], ids[3], social.RelationshipChange{Action: social.Block, ExpiresIn: expiryString("1h"), DegradeTo: expiryInt(social.Muted)}).ID
		query(t, db, `UPDATE owned_social_relationships SET expired_at=clock_timestamp()-interval '1 second' WHERE id=$1`, saved.Degraded)
		if _, e := r.ProcessExpiredRelationships(context.Background(), 1000); e != nil {
			t.Fatal(e)
		}
		saved.Snapshot = expirySnapshot(t, db, ids)
		saveRestartFixture(t, "relationship-expiry-restart.json", saved)
		return
	}
	db := database(t)
	loadRestartFixture(t, "relationship-expiry-restart.json", &saved)
	raw := expirySnapshot(t, db, saved.Accounts)
	var same bool
	if e := db.QueryRow(`SELECT $1::jsonb=$2::jsonb`, string(raw), string(saved.Snapshot)).Scan(&same); e != nil || !same {
		t.Fatal("expiry policy/event prestate changed after restart", same, e)
	}
	r := socialpg.New(db)
	a, e := r.Relationship(context.Background(), saved.Accounts[0], saved.Accounts[1])
	if e != nil || a.ID != saved.Permanent || a.Status != social.Blocked || a.ExpiredAt != nil {
		t.Fatal(a, e)
	}
	a, e = r.Relationship(context.Background(), saved.Accounts[1], saved.Accounts[2])
	if e != nil || a.ID != saved.Timed || a.Status != social.Muted || a.ExpiredAt == nil {
		t.Fatal(a, e)
	}
	a, e = r.Relationship(context.Background(), saved.Accounts[2], saved.Accounts[3])
	if e != nil || a.ID != saved.Degraded || a.Status != social.Muted || a.ExpiredAt != nil || expiryEvents(t, db, a.ID) != 1 {
		t.Fatal(a, e)
	}
}
