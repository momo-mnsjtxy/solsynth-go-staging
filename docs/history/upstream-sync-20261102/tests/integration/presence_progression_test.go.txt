// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/presencepg"
	"solsynth.local/go-migration/internal/progressionpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/presence"
	"solsynth.local/go-migration/ports/passport/progression"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type presenceProgressionFixture struct {
	db           *sql.DB
	ctx          context.Context
	actor, token string
	guard        *authoritypg.TransactionGuard
	issuer       *authority.Service
	verifier     *authority.Verifier
	principal    authority.Principal
	presence     *presencepg.Repository
	progression  *progressionpg.Repository
}

func newPresenceProgressionFixture(t *testing.T) presenceProgressionFixture {
	t.Helper()
	db := database(t)
	query(t, db, presencepg.Schema)
	query(t, db, progressionpg.Schema)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	f := presenceProgressionFixture{db: db, ctx: ctx, actor: authorityUUID(t)}
	if e := authoritypg.SeedSynthetic(ctx, db, f.actor, "synthetic_presence_"+strings.ReplaceAll(f.actor, "-", ""), time.Now().UTC()); e != nil {
		t.Fatal(e)
	}
	query(t, db, `INSERT INTO owned_accounts_profiles(account_id,id,time_zone,updated_at)VALUES($1,$2,'UTC',now())`, f.actor, authorityUUID(t))
	store := authoritypg.New(db)
	if e := store.ReplacePolicy(ctx, f.actor, []authority.Rule{{Pattern: "accounts.statuses.*"}, {Pattern: "progression.badges.manage"}}, nil); e != nil {
		t.Fatal(e)
	}
	now := &atomic.Int64{}
	now.Store(time.Now().UTC().Unix())
	cfg := authorityConfig(t, authorityKeys(t), now)
	var e error
	f.issuer, e = authority.New(store, cfg)
	if e != nil {
		t.Fatal(e)
	}
	f.token = pgIssue(t, f.issuer, f.actor).Token
	f.verifier, e = authority.NewVerifier(store, cfg)
	if e != nil {
		t.Fatal(e)
	}
	f.principal, e = f.verifier.Authenticate(ctx, f.token)
	if e != nil {
		t.Fatal(e)
	}
	f.guard = authoritypg.NewTransactionGuard(store, f.verifier)
	f.ctx = f.guard.Bind(ctx, f.principal)
	f.presence = presencepg.NewWithAuthority(db, f.guard)
	f.progression = progressionpg.NewWithAuthority(db, f.guard)
	return f
}
func ppScalar(t *testing.T, f presenceProgressionFixture, q string, args ...any) int64 {
	t.Helper()
	var v int64
	if e := f.db.QueryRowContext(f.ctx, q, args...).Scan(&v); e != nil {
		t.Fatal(e)
	}
	return v
}
func ppOther(t *testing.T, f presenceProgressionFixture) string {
	t.Helper()
	id := authorityUUID(t)
	if e := authoritypg.SeedSynthetic(f.ctx, f.db, id, "synthetic_presence_other_"+strings.ReplaceAll(id, "-", ""), time.Now().UTC()); e != nil {
		t.Fatal(e)
	}
	return id
}
func ppHTTP(t *testing.T, h http.Handler, method, path, token, body string, status int) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != status {
		t.Fatalf("%s %s: %d want %d %s", method, path, w.Code, status, w.Body.String())
	}
}
func TestPGPresencePrivacyLeasesAndAuthority(t *testing.T) {
	f := newPresenceProgressionFixture(t)
	other := ppOther(t, f)
	lease, e := f.presence.Heartbeat(f.ctx, f.actor, false)
	if e != nil {
		t.Fatal(e)
	}
	if d := lease.ExpiresAt.Sub(lease.LastSeenAt); d <= 0 || d > 120*time.Second {
		t.Fatal("lease bound", d)
	}
	if _, e = f.presence.Heartbeat(f.ctx, other, false); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("actor substitution", e)
	}
	if _, e = f.presence.Heartbeat(context.Background(), f.actor, false); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("unbound proof", e)
	}
	if _, e = presencepg.New(f.db).Heartbeat(f.ctx, f.actor, false); !errors.Is(e, authority.ErrUnavailable) {
		t.Fatal("unguarded constructor", e)
	}
	if _, e = presencepg.NewWithAuthority(database(t), f.guard).Heartbeat(f.ctx, f.actor, false); !errors.Is(e, authority.ErrUnavailable) {
		t.Fatal("wrong pool", e)
	}
	v, e := f.presence.GetStatus(context.Background(), "", f.actor)
	if e != nil || !v.IsOnline || v.LastSeenAt != nil {
		t.Fatal("public defaults", v, e)
	}
	pref := presence.Preferences{OnlineVisibility: "private", LastSeenVisibility: "private", StatusVisibility: "private"}
	if _, e = f.presence.SetPreferences(f.ctx, f.actor, pref); e != nil {
		t.Fatal(e)
	}
	label := "Synthetic private status"
	if _, e = f.presence.SetStatus(f.ctx, f.actor, presence.StatusRequest{Label: &label}, false); e != nil {
		t.Fatal(e)
	}
	v, e = f.presence.GetStatus(context.Background(), "", f.actor)
	if e != nil || v.IsOnline || v.IsCustomized || v.LastSeenAt != nil {
		t.Fatal("privacy leak", v, e)
	}
	v, e = f.presence.GetStatus(f.ctx, f.actor, f.actor)
	if e != nil || !v.IsOnline || !v.IsCustomized || v.LastSeenAt == nil {
		t.Fatal("owner projection", v, e)
	}
	second := pgIssue(t, f.issuer, f.actor)
	p, e := f.verifier.Authenticate(f.ctx, second.Token)
	if e != nil {
		t.Fatal(e)
	}
	secondCtx := f.guard.Bind(f.ctx, p)
	if _, e = f.presence.Heartbeat(secondCtx, f.actor, true); e != nil {
		t.Fatal(e)
	}
	if e = f.presence.Disconnect(f.ctx, f.actor); e != nil {
		t.Fatal(e)
	}
	v, e = f.presence.GetStatus(secondCtx, f.actor, f.actor)
	if e != nil || !v.IsOnline || !v.IsIdle || v.IdleSince == nil {
		t.Fatal("remaining session", v, e)
	}
	query(t, f.db, `UPDATE owned_presence_sessions SET expires_at=last_seen_at WHERE account_id=$1`, f.actor)
	v, e = f.presence.GetStatus(secondCtx, f.actor, f.actor)
	if e != nil || v.IsOnline || v.IsIdle {
		t.Fatal("strict lease expiry", v, e)
	}
	if _, e = f.presence.Heartbeat(secondCtx, f.actor, false); e != nil {
		t.Fatal(e)
	}
	if e = f.issuer.Revoke(f.ctx, second.Token, "current"); e != nil {
		t.Fatal(e)
	}
	v, e = f.presence.GetStatus(f.ctx, f.actor, f.actor)
	if e != nil || v.IsOnline {
		t.Fatal("revoked lease kept account online", v, e)
	}
	if _, e = f.presence.Heartbeat(secondCtx, f.actor, false); !errors.Is(e, authority.ErrRevoked) {
		t.Fatal("revoked heartbeat", e)
	}
	h := (&presence.Service{Repository: f.presence, Authority: f.verifier, Bind: f.guard.Bind}).Handler()
	ppHTTP(t, h, "POST", "/api/accounts/me/presence/heartbeat", f.token, `{"is_idle":false,"account_id":"forged"}`, 400)
	ppHTTP(t, h, "GET", "/api/accounts/me/statuses", "", "", 401)
}
func TestPGPresenceFriendsBlocksInvisibleAndExpiry(t *testing.T) {
	f := newPresenceProgressionFixture(t)
	friend := ppOther(t, f)
	pair := pgIssue(t, f.issuer, friend)
	p, e := f.verifier.Authenticate(f.ctx, pair.Token)
	if e != nil {
		t.Fatal(e)
	}
	viewer := f.guard.Bind(f.ctx, p)
	query(t, f.db, `INSERT INTO owned_social_relationships(id,account_id,related_id,status,created_at,updated_at)VALUES($1,$2,$3,100,now(),now()),($4,$3,$2,100,now(),now())`, authorityUUID(t), f.actor, friend, authorityUUID(t))
	pref := presence.Preferences{OnlineVisibility: "friends", LastSeenVisibility: "friends", StatusVisibility: "friends"}
	if _, e = f.presence.SetPreferences(f.ctx, f.actor, pref); e != nil {
		t.Fatal(e)
	}
	if _, e = f.presence.Heartbeat(f.ctx, f.actor, false); e != nil {
		t.Fatal(e)
	}
	v, e := f.presence.GetStatus(viewer, friend, f.actor)
	if e != nil || !v.IsOnline || v.LastSeenAt == nil {
		t.Fatal("friend denied", v, e)
	}
	label := "Synthetic invisible"
	st, e := f.presence.SetStatus(f.ctx, f.actor, presence.StatusRequest{Type: presence.Invisible, Label: &label}, false)
	if e != nil {
		t.Fatal(e)
	}
	v, e = f.presence.GetStatus(viewer, friend, f.actor)
	if e != nil || v.IsOnline || v.LastSeenAt != nil || v.Type != presence.Default {
		t.Fatal("invisible leaked", v, e)
	}
	query(t, f.db, `UPDATE owned_presence_statuses SET cleared_at=created_at WHERE id=$1`, st.ID)
	v, e = f.presence.GetStatus(viewer, friend, f.actor)
	if e != nil || !v.IsOnline || v.IsCustomized {
		t.Fatal("expired status retained", v, e)
	}
	query(t, f.db, `UPDATE owned_social_relationships SET status=-100 WHERE account_id=$1 AND related_id=$2`, f.actor, friend)
	if _, e = f.presence.GetStatus(viewer, friend, f.actor); !errors.Is(e, presence.ErrNotFound) {
		t.Fatal("blocked status readable", e)
	}
}
func TestPGProgressionConcurrentCheckInAndIsolation(t *testing.T) {
	f := newPresenceProgressionFixture(t)
	other := ppOther(t, f)
	var wg sync.WaitGroup
	results := make(chan progression.CheckIn, 12)
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := f.progression.CheckIn(f.ctx, f.actor)
			if e != nil {
				errs <- e
				return
			}
			results <- v
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	id := ""
	for v := range results {
		if id == "" {
			id = v.ID
		}
		if v.ID != id || v.RewardExperience != 100 || v.RewardPoints != nil || len(v.Tips) != 0 || v.FortuneReport != nil {
			t.Fatal("non-idempotent/unsupported reward", v)
		}
	}
	if n := ppScalar(t, f, `SELECT count(*) FROM owned_progression_checkins WHERE account_id=$1`, f.actor); n != 1 {
		t.Fatal(n)
	}
	if n := ppScalar(t, f, `SELECT COALESCE(sum(delta),0) FROM owned_progression_experience WHERE account_id=$1`, f.actor); n != 100 {
		t.Fatal("duplicate reward", n)
	}
	if _, e := f.progression.CheckIn(f.ctx, other); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("cross-account check-in", e)
	}
	if n := ppScalar(t, f, `SELECT count(*) FROM owned_progression_experience WHERE account_id=$1`, other); n != 0 {
		t.Fatal("cross-account effect", n)
	}
	if _, e := progressionpg.New(f.db).CheckIn(f.ctx, f.actor); !errors.Is(e, authority.ErrUnavailable) {
		t.Fatal("unguarded", e)
	}
	if _, e := f.progression.CheckIn(context.Background(), f.actor); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("proof missing", e)
	}
	s, e := f.progression.Summary(f.ctx, f.actor)
	if e != nil || s.Experience != 100 {
		t.Fatal(s, e)
	}
	h := (&progression.Service{Repository: f.progression, Authority: f.verifier, Bind: f.guard.Bind}).Handler()
	ppHTTP(t, h, "POST", "/api/accounts/me/check-in", f.token, `{"experience":999}`, 400)
	ppHTTP(t, h, "POST", "/api/accounts/me/check-in?backdated=2020-01-01", f.token, `null`, 400)
	ppHTTP(t, h, "POST", "/api/accounts/me/check-in", f.token, `null`, 200)
	ppHTTP(t, h, "POST", "/api/admin/progression/achievements", f.token, `{"identifier":"forged","title":"forged","summary":"forged","trigger":{"actions":["post.create"],"mode":"count"}}`, 403)
}
func ppPost(t *testing.T, f presenceProgressionFixture) (string, string) {
	t.Helper()
	pub, post := authorityUUID(t), authorityUUID(t)
	name := "synthetic_presence_pub_" + strings.ReplaceAll(pub, "-", "")
	query(t, f.db, `INSERT INTO owned_publishers(id,name,nick,type,account_id,created_at,updated_at)VALUES($1,$2,$2,0,$3,now(),now())`, pub, name, f.actor)
	query(t, f.db, `INSERT INTO owned_text_posts(id,publisher_id,content,visibility,type,published_at,created_at,updated_at)VALUES($1,$2,'Synthetic canonical progression post',0,0,now(),now(),now())`, post, pub)
	query(t, f.db, `INSERT INTO owned_text_post_audit(actor_id,post_id,action,occurred_at)VALUES($1,$2,'create',now())`, f.actor, post)
	return pub, post
}
func TestPGProgressionCanonicalProjectorRollbackAndEarnedBadges(t *testing.T) {
	f := newPresenceProgressionFixture(t)
	_, post := ppPost(t, f)
	if e := f.progression.RecordCanonicalEvent(f.ctx, f.actor, "post.create", authorityUUID(t)); !errors.Is(e, progression.ErrNotFound) {
		t.Fatal("forged source", e)
	}
	tx, e := f.db.BeginTx(f.ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.progression.ApplyCanonicalEvent(f.ctx, tx, f.actor, "post.create", post); e != nil {
		tx.Rollback()
		t.Fatal(e)
	}
	if e = tx.Rollback(); e != nil {
		t.Fatal(e)
	}
	if n := ppScalar(t, f, `SELECT count(*) FROM owned_progression_experience WHERE account_id=$1`, f.actor); n != 0 {
		t.Fatal("rollback leaked xp", n)
	}
	// Use an explicitly provisioned synthetic administrator, never a client XP endpoint.
	admin := ppOther(t, f)
	if e = authoritypg.New(f.db).ReplacePolicy(f.ctx, admin, []authority.Rule{{Pattern: "progression.*"}}, nil); e != nil {
		t.Fatal(e)
	}
	pair := pgIssue(t, f.issuer, admin)
	p, e := f.verifier.Authenticate(f.ctx, pair.Token)
	if e != nil {
		t.Fatal(e)
	}
	adminCtx := f.guard.Bind(f.ctx, p)
	identifier := "synthetic-earned-" + strings.ReplaceAll(f.actor, "-", "")
	label := "Synthetic catalog badge"
	d := progression.Definition{Identifier: identifier, Title: "Synthetic earned badge", Summary: "Synthetic canonical event fixture", IsEnabled: true, IsProgressEnabled: true, TargetCount: 1, Trigger: progression.Trigger{Actions: []string{"post.create"}, Mode: "count"}, Reward: progression.Reward{Experience: 7, Badge: &progression.BadgeReward{Type: "progression.post.featured", Label: &label}}}
	if _, e = f.progression.SaveDefinition(adminCtx, admin, "achievement", "", d, true); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_, _ = f.db.Exec(`UPDATE owned_progression_definitions SET definition=jsonb_set(definition,'{is_enabled}','false')WHERE identifier=$1`, identifier)
	})
	// Two workers may discover the same committed record; durable receipts and the
	// canonical actor lock prevent duplicate grants and let unrelated actors run.
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := f.progression.ProcessDue(f.ctx, 1000); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	for ppScalar(t, f, `SELECT count(*) FROM owned_progression_events WHERE account_id=$1 AND event_key=$2`, f.actor, "post.create:"+post) == 0 {
		n, e := f.progression.ProcessDue(f.ctx, 1000)
		if e != nil || n == 0 {
			t.Fatal("canonical post never projected", n, e)
		}
	}
	if e = f.progression.RecordCanonicalEvent(f.ctx, f.actor, "post.create", post); e != nil {
		t.Fatal(e)
	}
	if n := ppScalar(t, f, `SELECT count(*) FROM owned_progression_grants WHERE account_id=$1 AND identifier=$2`, f.actor, identifier); n != 1 {
		t.Fatal("duplicate/missing grant", n)
	}
	if n := ppScalar(t, f, `SELECT count(*) FROM owned_progression_experience WHERE account_id=$1 AND reason LIKE $2`, f.actor, "achievement:"+identifier+":%"); n != 1 {
		t.Fatal("duplicate XP", n)
	}
	badges, e := f.progression.Badges(f.ctx, f.actor, f.actor)
	if e != nil {
		t.Fatal(e)
	}
	var badgeID string
	for _, b := range badges {
		if b.Type == "progression.post.featured" {
			badgeID = b.ID
		}
	}
	if badgeID == "" {
		t.Fatal("badge not earned")
	}
	if e = f.progression.ActivateBadge(f.ctx, f.actor, badgeID); e != nil {
		t.Fatal(e)
	}
	summary, e := f.progression.Summary(f.ctx, f.actor)
	if e != nil || summary.ActiveBadge == nil || summary.ActiveBadge.ID != badgeID {
		t.Fatal(summary, e)
	}
	if e = f.progression.ActivateBadge(f.ctx, f.actor, authorityUUID(t)); !errors.Is(e, progression.ErrNotFound) {
		t.Fatal("unearned badge activated", e)
	}
	query(t, f.db, `UPDATE owned_progression_badges SET expired_at=created_at WHERE id=$1`, badgeID)
	if e = f.progression.ActivateBadge(f.ctx, f.actor, badgeID); !errors.Is(e, progression.ErrNotFound) {
		t.Fatal("expired badge activated", e)
	}
	if e = f.progression.SetLevelPolicy(adminCtx, admin, []int64{0, 100, 300}); e != nil {
		t.Fatal(e)
	}
	summary, e = f.progression.Summary(f.ctx, f.actor)
	if e != nil || summary.Level == nil || !summary.LevelPolicyConfigured {
		t.Fatal("configured level", summary, e)
	}
	if n := ppScalar(t, f, `SELECT count(*) FROM owned_progression_outbox WHERE account_id=$1`, f.actor); n == 0 {
		t.Fatal("no durable completion")
	}
	if _, e = f.progression.ProcessDue(f.ctx, 1001); !errors.Is(e, progression.ErrInvalid) {
		t.Fatal("unbounded worker", e)
	}
}
func TestPGPresenceProgressionRevocationWhileBlocked(t *testing.T) {
	for _, domain := range []string{"presence", "checkin"} {
		t.Run(domain, func(t *testing.T) {
			f := newPresenceProgressionFixture(t)
			base := atomicAuthorityFixture{db: f.db, ctx: f.ctx, actor: f.actor}
			blocker, pid := atomicBlocker(t, base, "owned_authority_accounts", f.actor)
			if _, e := blocker.ExecContext(f.ctx, `UPDATE owned_authority_sessions SET revoked_at=now(),epoch=epoch+1 WHERE id=$1`, f.principal.Session.ID); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() {
				if domain == "presence" {
					_, e := f.presence.Heartbeat(f.ctx, f.actor, false)
					done <- e
				} else {
					_, e := f.progression.CheckIn(f.ctx, f.actor)
					done <- e
				}
			}()
			waitAtomicBlocked(t, base, pid, "owned_authority_accounts")
			if e := blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e := <-done; !errors.Is(e, authority.ErrRevoked) {
				t.Fatal("stale proof wrote", e)
			}
			for _, table := range []string{"owned_presence_sessions", "owned_progression_checkins", "owned_progression_experience"} {
				if n := ppScalar(t, f, `SELECT count(*) FROM `+table+` WHERE account_id=$1`, f.actor); n != 0 {
					t.Fatal("rollback side effect", table, n)
				}
			}
		})
	}
}
func TestPGPresenceProgressionRestartPersistence(t *testing.T) {
	db := database(t)
	query(t, db, presencepg.Schema)
	query(t, db, progressionpg.Schema)
	const actor = "c8910000-0000-4000-8000-000000000001"
	switch os.Getenv("PG_TEST_PHASE") {
	case "before":
		f := newPresenceProgressionFixture(t)
		if e := authoritypg.SeedSynthetic(f.ctx, db, actor, "synthetic_presence_restart", time.Now().UTC()); e != nil {
			t.Fatal(e)
		}
		pair := pgIssue(t, f.issuer, actor)
		p, e := f.verifier.Authenticate(f.ctx, pair.Token)
		if e != nil {
			t.Fatal(e)
		}
		ctx := f.guard.Bind(f.ctx, p)
		if _, e = f.presence.SetPreferences(ctx, actor, presence.Preferences{OnlineVisibility: "private", LastSeenVisibility: "private", StatusVisibility: "private"}); e != nil {
			t.Fatal(e)
		}
		if _, e = f.presence.Heartbeat(ctx, actor, false); e != nil {
			t.Fatal(e)
		}
		if _, e = f.progression.CheckIn(ctx, actor); e != nil {
			t.Fatal(e)
		}
	case "after":
	default:
		t.Fatal("PG_TEST_PHASE must be before or after")
	}
	var count, xp int
	var privacy string
	if e := db.QueryRow(`SELECT (SELECT count(*) FROM owned_progression_checkins WHERE account_id=$1),(SELECT COALESCE(sum(delta),0)FROM owned_progression_experience WHERE account_id=$1),(SELECT online_visibility FROM owned_presence_preferences WHERE account_id=$1)`, actor).Scan(&count, &xp, &privacy); e != nil {
		t.Fatal(e)
	}
	if count != 1 || xp != 100 || privacy != "private" {
		t.Fatal("restart state", count, xp, privacy)
	}
	var valid bool
	if e := db.QueryRow(`SELECT bool_and(expires_at<=last_seen_at+interval '120 seconds')FROM owned_presence_sessions WHERE account_id=$1`, actor).Scan(&valid); e != nil || !valid {
		t.Fatal("lease bound lost", e)
	}
}

func TestPGProgressionChatSocialProjection(t *testing.T) {
	f := newPresenceProgressionFixture(t)
	other := ppOther(t, f)
	room, member, message, relation := authorityUUID(t), authorityUUID(t), authorityUUID(t), authorityUUID(t)
	query(t, f.db, `INSERT INTO chat_rooms(id,is_public,encryption_mode,owned_owner_id,owned_name)VALUES($1,false,0,$2,'Synthetic progression room')`, room, f.actor)
	query(t, f.db, `INSERT INTO chat_members(id,chat_room_id,account_id,joined_at)VALUES($1,$2,$3,now())`, member, room, f.actor)
	query(t, f.db, `INSERT INTO chat_messages(id,chat_room_id,sender_id,type,room_sequence,content,created_at,updated_at,owned_request_hash)VALUES($1,$2,$3,'text',1,'Synthetic authored message',now(),now(),decode(repeat('ab',32),'hex'))`, message, room, member)
	query(t, f.db, `INSERT INTO owned_social_relationships(id,account_id,related_id,status,created_at,updated_at)VALUES($1,$2,$3,100,now(),now()),($4,$3,$2,100,now(),now())`, relation, f.actor, other, authorityUUID(t))
	for ppScalar(t, f, `SELECT count(*) FROM owned_progression_events WHERE account_id=$1 AND kind IN('chat.use','chatroom.join','relationship.friend_established')`, f.actor) < 3 {
		n, e := f.progression.ProcessDue(f.ctx, 1000)
		if e != nil || n == 0 {
			t.Fatal("chat/social not projected", n, e)
		}
	}
	for _, id := range []string{"initial-chat", "first-chatroom", "friends-1"} {
		if n := ppScalar(t, f, `SELECT count(*) FROM owned_progression_grants WHERE account_id=$1 AND identifier=$2`, f.actor, id); n != 1 {
			t.Fatal("missing earned grant", id, n)
		}
	}
	if e := f.progression.RecordCanonicalEvent(f.ctx, f.actor, "relationship.friend_established", relation); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `DELETE FROM owned_social_relationships WHERE account_id=$1 AND related_id=$2`, f.actor, other)
	replacement := authorityUUID(t)
	query(t, f.db, `INSERT INTO owned_social_relationships(id,account_id,related_id,status,created_at,updated_at)VALUES($1,$2,$3,100,now(),now())`, replacement, f.actor, other)
	if e := f.progression.RecordCanonicalEvent(f.ctx, f.actor, "relationship.friend_established", replacement); e != nil {
		t.Fatal(e)
	}
	if n := ppScalar(t, f, `SELECT progress_count FROM owned_progression_progress WHERE account_id=$1 AND kind='achievement' AND identifier='friends-5' AND period_key=''`, f.actor); n != 1 {
		t.Fatal("re-friending farmed XP", n)
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, other)
	if _, e := f.progression.ProcessDue(f.ctx, 1000); e != nil {
		t.Fatal("disabled account must be receipted safely", e)
	}
}
