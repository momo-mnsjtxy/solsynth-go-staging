// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/internal/presencepg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/passport/presence"
	"solsynth.local/go-migration/ports/passport/social"
	c "solsynth.local/go-migration/ports/wallet/commerce"
)

type syntheticChatProfilePerk struct{}

func (syntheticChatProfilePerk) RequireChatProfile(context.Context, *sql.DB, *sql.Tx, string, time.Time) error {
	return nil
}
func detailFixture(t *testing.T) chatRelationsFixture {
	t.Helper()
	f := newChatRelationsFixture(t)
	query(t, f.db, presencepg.Schema)
	f.chats.WithProfilePerks(syntheticChatProfilePerk{})
	return f
}
func detailScope(t *testing.T, db *sql.DB, actor string) string {
	t.Helper()
	var s string
	if e := db.QueryRow(`SELECT name FROM owned_authority_accounts WHERE id=$1`, actor).Scan(&s); e != nil {
		t.Fatal(e)
	}
	return s
}
func detailRoom(t *testing.T, f chatRelationsFixture, slug string, public bool) rooms.Room {
	t.Helper()
	v, e := f.chats.Create(f.contexts[f.owner], f.owner, rooms.RoomPatch{Name: relationText("Detail room"), Slug: &slug, IsPublic: &public, IsCommunity: &public})
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func detailNoSlug(t *testing.T, f chatRelationsFixture, scope, slug string) {
	t.Helper()
	if _, e := f.chats.PublicBySlug(f.base, "", scope, slug); !errors.Is(e, rooms.ErrNotFound) {
		t.Fatal("slug disclosed", scope, slug, e)
	}
}

// An online projection has no room-media response. Even a failing decorator
// must not be consulted, and inaccessible slug lookups must deny first.
type detailRejectMedia struct{ calls int }

func (m *detailRejectMedia) Ready(context.Context) error { return nil }
func (m *detailRejectMedia) Apply(context.Context, *sql.Tx, string, *rooms.Room, rooms.RoomPatch) (map[string]any, error) {
	return nil, rooms.ErrUnavailable
}
func (m *detailRejectMedia) Load(context.Context, *sql.Tx, string, *rooms.Room) error {
	m.calls++
	return rooms.ErrUnavailable
}

func TestPGChatDetailsSlugScopeRenameAndNondisclosure(t *testing.T) {
	f := detailFixture(t)
	scope := detailScope(t, f.db, f.owner)
	room := detailRoom(t, f, "  Shared.Room  ", true)
	got, e := f.chats.PublicBySlug(f.base, "", scope, "SHARED.ROOM")
	if e != nil || got.ID != room.ID || got.Slug == nil || *got.Slug != "shared.room" {
		t.Fatal(got, e)
	}
	if _, e = f.social.ChangeRelationship(f.contexts[f.other], f.other, f.owner, social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.chats.PublicBySlug(f.contexts[f.other], f.other, scope, "shared.room"); !errors.Is(e, rooms.ErrNotFound) {
		t.Fatal("blocked owner alias exposed", e)
	}
	if _, e = f.chats.Create(f.contexts[f.owner], f.owner, rooms.RoomPatch{Name: relationText("Conflict"), Slug: relationText("shared.room")}); !errors.Is(e, rooms.ErrConflict) {
		t.Fatal("duplicate scope", e)
	}
	yes := true
	other, e := f.chats.Create(f.contexts[f.other], f.other, rooms.RoomPatch{Name: relationText("Other owner"), Slug: relationText("shared.room"), IsPublic: &yes})
	if e != nil {
		t.Fatal(e)
	}
	got, e = f.chats.PublicBySlug(f.base, "", detailScope(t, f.db, f.other), "shared.room")
	if e != nil || got.ID != other.ID {
		t.Fatal(got, e)
	}
	secret := detailRoom(t, f, "secret", false)
	query(t, f.db, `UPDATE chat_rooms SET encryption_mode=88 WHERE id=$1`, secret.ID)
	if _, e = f.chats.OnlineMembers(f.base, "", secret.ID); !errors.Is(e, rooms.ErrNotFound) {
		t.Fatal("private unsupported-mode existence disclosed", e)
	}
	media := &detailRejectMedia{}
	f.chats.WithRoomMedia(media)
	detailNoSlug(t, f, scope, "secret")
	detailNoSlug(t, f, scope, "missing")
	if media.calls != 0 {
		t.Fatal("inaccessible room invoked media hydration")
	}
	f.chats.WithRoomMedia(nil)
	renamed := "detail_" + strings.ReplaceAll(f.owner, "-", "")
	query(t, f.db, `UPDATE owned_authority_accounts SET name=$2 WHERE id=$1`, f.owner, renamed)
	detailNoSlug(t, f, scope, "shared.room")
	// A recycled name resolves only rooms belonging to its current owner.
	query(t, f.db, `UPDATE owned_authority_accounts SET name=$2 WHERE id=$1`, f.outsider, scope)
	reused, e := f.chats.Create(f.contexts[f.outsider], f.outsider, rooms.RoomPatch{Name: relationText("Reused name"), Slug: relationText("shared.room"), IsPublic: &yes})
	if e != nil {
		t.Fatal(e)
	}
	got, e = f.chats.PublicBySlug(f.base, "", scope, "shared.room")
	if e != nil || got.ID != reused.ID || got.ID == room.ID {
		t.Fatal("name reuse inherited old identity", got, e)
	}
	got, e = f.chats.PublicBySlug(f.base, "", renamed, "shared.room")
	if e != nil || got.ID != room.ID {
		t.Fatal(got, e)
	}
	if _, e = f.chats.Update(f.contexts[f.owner], f.owner, room.ID, rooms.RoomPatch{Slug: relationText("new-slug")}); e != nil {
		t.Fatal(e)
	}
	detailNoSlug(t, f, renamed, "shared.room")
	if _, e = f.chats.Update(f.contexts[f.owner], f.owner, room.ID, rooms.RoomPatch{Slug: relationText("")}); e != nil {
		t.Fatal(e)
	}
	detailNoSlug(t, f, renamed, "new-slug")
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.other)
	detailNoSlug(t, f, detailScope(t, f.db, f.other), "shared.room")
}
func TestPGChatDetailsRealmScopeCollisionTransferAndRollback(t *testing.T) {
	f := detailFixture(t)
	a := relationRealm(t, f, true)
	b := relationRealm(t, f, true)
	one := relationRoom(t, f, a)
	two := relationRoom(t, f, b)
	for _, r := range []rooms.Room{one, two} {
		if _, e := f.chats.Update(f.contexts[f.owner], f.owner, r.ID, rooms.RoomPatch{Slug: relationText("same")}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := f.chats.Update(f.contexts[f.owner], f.owner, one.ID, rooms.RoomPatch{RealmID: &b.ID}); !errors.Is(e, rooms.ErrConflict) {
		t.Fatal("realm slug collision", e)
	}
	got, e := f.chats.PublicBySlug(f.base, "", a.Slug, "same")
	if e != nil || got.ID != one.ID || got.RealmID == nil || *got.RealmID != a.ID {
		t.Fatal("failed move was not atomic", got, e)
	}
	if _, e = f.chats.Update(f.contexts[f.owner], f.owner, two.ID, rooms.RoomPatch{Slug: relationText("different")}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.chats.Update(f.contexts[f.owner], f.owner, one.ID, rooms.RoomPatch{RealmID: &b.ID}); e != nil {
		t.Fatal(e)
	}
	detailNoSlug(t, f, a.Slug, "same")
	fresh := "renamed_" + strings.ReplaceAll(b.ID, "-", "")
	query(t, f.db, `UPDATE owned_realms SET slug=$2 WHERE id=$1`, b.ID, fresh)
	detailNoSlug(t, f, b.Slug, "same")
	got, e = f.chats.PublicBySlug(f.base, "", fresh, "same")
	if e != nil || got.ID != one.ID {
		t.Fatal(got, e)
	}
	query(t, f.db, `UPDATE owned_realms SET is_public=false WHERE id=$1`, b.ID)
	detailNoSlug(t, f, fresh, "same")
	query(t, f.db, `UPDATE owned_realms SET account_id=$2 WHERE id=$1`, b.ID, f.outsider)
	if _, e = f.chats.PublicBySlug(f.contexts[f.outsider], f.outsider, fresh, "same"); !errors.Is(e, rooms.ErrNotFound) {
		t.Fatal("new realm ownership invented private membership", e)
	}
	query(t, f.db, `UPDATE owned_realms SET is_public=true WHERE id=$1`, b.ID)
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.outsider)
	detailNoSlug(t, f, fresh, "same")
}
func TestPGChatDetailsOnlineCanonicalPrivacyAndSessionExpiry(t *testing.T) {
	f := detailFixture(t)
	room := detailRoom(t, f, "online", true)
	for _, actor := range []string{f.other, f.third, f.outsider} {
		if _, e := f.chats.ChangeMember(f.contexts[actor], actor, room.ID, rooms.Join, ""); e != nil {
			t.Fatal(e)
		}
	}
	p := presencepg.NewWithAuthority(f.db, f.guard)
	for _, actor := range []string{f.owner, f.other, f.third, f.outsider} {
		if _, e := p.Heartbeat(f.contexts[actor], actor, false); e != nil {
			t.Fatal(e)
		}
	}
	count := func(actor string, want int) {
		t.Helper()
		ctx := f.base
		if actor != "" {
			ctx = f.contexts[actor]
		}
		v, e := f.chats.OnlineMembers(ctx, actor, room.ID)
		if e != nil || v.OnlineCount != want || len(v.OnlineAccounts) != want {
			t.Fatal(v, e, want)
		}
	}
	count("", 4)
	// No room media, realm detail, activity or private account payload is needed
	// to count public canonical presence. A one-connection pool must suffice.
	media := &detailRejectMedia{}
	f.chats.WithRoomMedia(media)
	f.db.SetMaxOpenConns(1)
	count("", 4)
	f.db.SetMaxOpenConns(0)
	if media.calls != 0 {
		t.Fatal("online count hydrated unused room metadata")
	}
	f.chats.WithRoomMedia(nil)
	query(t, f.db, `INSERT INTO owned_accounts_profiles(id,account_id,birthday,updated_at) VALUES($1,$2,'2000-01-01',clock_timestamp()) ON CONFLICT(account_id)DO UPDATE SET birthday='2000-01-01'`, authorityUUID(t), f.other)
	projection, e := f.chats.OnlineMembers(f.base, "", room.ID)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(projection)
	if e != nil || strings.Contains(string(raw), "birthday") || strings.Contains(string(raw), "session_id") || strings.Contains(string(raw), "activities") {
		t.Fatal("private online projection", string(raw), e)
	}
	pref := presence.DefaultPreferences()
	pref.OnlineVisibility = "private"
	if _, e := p.SetPreferences(f.contexts[f.other], f.other, pref); e != nil {
		t.Fatal(e)
	}
	count("", 3)
	if _, e := p.SetStatus(f.contexts[f.third], f.third, presence.StatusRequest{Type: presence.Invisible}, false); e != nil {
		t.Fatal(e)
	}
	count("", 2)
	if _, e := f.social.ChangeRelationship(f.contexts[f.owner], f.owner, f.outsider, social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	count(f.owner, 1)
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.outsider)
	count("", 1)
	query(t, f.db, `UPDATE owned_presence_sessions SET last_seen_at=clock_timestamp()-interval '2 seconds',expires_at=clock_timestamp()-interval '1 second',idle_since=NULL WHERE account_id=$1`, f.owner)
	count("", 0)
	query(t, f.db, `UPDATE chat_rooms SET is_public=false WHERE id=$1`, room.ID)
	if _, e := f.chats.OnlineMembers(f.base, "", room.ID); !errors.Is(e, rooms.ErrNotFound) {
		t.Fatal("private existence disclosed", e)
	}
	// The direct-room response preserves the canonical status shape exactly,
	// without reintroducing a member/account collection or private profile data.
	if _, e = p.SetPreferences(f.contexts[f.other], f.other, presence.DefaultPreferences()); e != nil {
		t.Fatal(e)
	}
	if _, e = p.SetStatus(f.contexts[f.other], f.other, presence.StatusRequest{Type: presence.Busy, Label: relationText("Canonical busy"), Meta: json.RawMessage(`{"purpose":"synthetic"}`)}, false); e != nil {
		t.Fatal(e)
	}
	direct := relationDirect(t, f)
	got, e := f.chats.OnlineMembers(f.contexts[f.owner], f.owner, direct.ID)
	if e != nil || got.DirectMessageStatus == nil || len(got.OnlineAccounts) != 0 || len(got.OnlineUserNames) != 0 {
		t.Fatal(got, e)
	}
	canonical, e := p.GetStatus(f.contexts[f.owner], f.owner, f.other)
	if e != nil {
		t.Fatal(e)
	}
	actualJSON, _ := json.Marshal(got.DirectMessageStatus)
	canonicalJSON, _ := json.Marshal(canonical)
	if string(actualJSON) != string(canonicalJSON) {
		t.Fatal("chat/canonical status drift", string(actualJSON), string(canonicalJSON))
	}
}
func TestPGChatDetailsProfileClearAuditRollbackAndOriginalProof(t *testing.T) {
	f := detailFixture(t)
	room := detailRoom(t, f, "profile", false)
	v, e := f.chats.UpdateProfile(f.contexts[f.owner], f.owner, room.ID, rooms.ProfileRequest{Nick: relationText("Room-specific")})
	if e != nil || v.Nick == nil || *v.Nick != "Room-specific" || v.Account == nil {
		t.Fatal(v, e)
	}
	if _, e = f.chats.UpdateProfile(f.base, f.owner, room.ID, rooms.ProfileRequest{Nick: relationText("forged")}); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("missing original proof", e)
	}
	before := detailSnapshot(t, f.db, room.ID)
	query(t, f.db, `CREATE FUNCTION synthetic_chat_detail_fail() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.room_id::text=TG_ARGV[0] THEN RAISE EXCEPTION 'synthetic audit failure';END IF;RETURN NEW;END$$`)
	query(t, f.db, fmt.Sprintf(`CREATE TRIGGER synthetic_chat_detail_fail BEFORE INSERT ON owned_chat_detail_audit FOR EACH ROW EXECUTE FUNCTION synthetic_chat_detail_fail('%s')`, room.ID))
	if _, e = f.chats.UpdateProfile(f.contexts[f.owner], f.owner, room.ID, rooms.ProfileRequest{Nick: relationText("must roll back")}); e == nil {
		t.Fatal("audit failure accepted")
	}
	query(t, f.db, `DROP TRIGGER synthetic_chat_detail_fail ON owned_chat_detail_audit`)
	query(t, f.db, `DROP FUNCTION synthetic_chat_detail_fail()`)
	if got := detailSnapshot(t, f.db, room.ID); got != before {
		t.Fatal("profile audit failure did not roll back exact state")
	}
	v, e = f.chats.Me(f.contexts[f.owner], f.owner, room.ID)
	if e != nil || v.Nick == nil || *v.Nick != "Room-specific" {
		t.Fatal(v, e)
	}
	f.chats.WithProfilePerks(nil)
	if _, e = f.chats.UpdateProfile(f.contexts[f.owner], f.owner, room.ID, rooms.ProfileRequest{}); !errors.Is(e, rooms.ErrUnavailable) {
		t.Fatal("missing perk configuration admitted", e)
	}
	if e = authoritypg.New(f.db).ReplacePolicy(f.base, f.owner, []authority.Rule{{Pattern: "chat.members.manage", Deny: true}}, nil); e != nil {
		t.Fatal(e)
	}
	v, e = f.chats.ClearProfile(f.contexts[f.owner], f.owner, room.ID)
	if e != nil || v.Nick != nil {
		t.Fatal("clear incorrectly requires paid scope", v, e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_chat_detail_audit WHERE room_id=$1`, room.ID); n != 2 {
		t.Fatal(n)
	}
}
func TestPGChatDetailsCanonicalProfilePerkLevelAndExpiry(t *testing.T) {
	f := newCommerceFixture(t)
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	chat, e := chatpg.NewWithAuthority(f.db, chatPolicy, f.guard)
	if e != nil {
		t.Fatal(e)
	}
	chat.WithSocial(socialpg.NewWithAuthority(f.db, f.guard))
	perk, e := f.r.ChatProfilePerks(map[string]int{f.plan: 2})
	if e != nil {
		t.Fatal(e)
	}
	chat.WithProfilePerks(perk)
	room, e := chat.Create(f.ctx, f.actor, rooms.RoomPatch{Name: relationText("Paid profile")})
	if e != nil {
		t.Fatal(e)
	}
	req := rooms.ProfileRequest{Nick: relationText("Paid name")}
	if _, e = chat.UpdateProfile(f.ctx, f.actor, room.ID, req); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("unpaid admitted", e)
	}
	sub, e := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: f.plan}, "chat-profile", false)
	if e != nil {
		t.Fatal(e)
	}
	order, e := f.r.SubscriptionOrder(f.ctx, f.actor, sub.Identifier)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = chat.UpdateProfile(f.ctx, f.actor, room.ID, req); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("unpaid order admitted", e)
	}
	if _, e = f.r.PayOrder(f.ctx, f.actor, order.ID, &f.wallet.ID); e != nil {
		t.Fatal(e)
	}
	lower, e := f.r.ChatProfilePerks(map[string]int{f.plan: 1})
	if e != nil {
		t.Fatal(e)
	}
	chat.WithProfilePerks(lower)
	if _, e = chat.UpdateProfile(f.ctx, f.actor, room.ID, req); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("level one admitted", e)
	}
	chat.WithProfilePerks(perk)
	if _, e = chat.UpdateProfile(f.ctx, f.actor, room.ID, req); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE commerce_v1.definitions SET enabled=false WHERE identifier=$1`, f.plan)
	if _, e = chat.UpdateProfile(f.ctx, f.actor, room.ID, req); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("disabled definition admitted", e)
	}
	query(t, f.db, `UPDATE commerce_v1.definitions SET enabled=true WHERE identifier=$1`, f.plan)
	query(t, f.db, `UPDATE commerce_v1.subscriptions SET ended_at=clock_timestamp()-interval '1 second',document=jsonb_set(document,'{ended_at}',to_jsonb(clock_timestamp()-interval '1 second')) WHERE id=$1`, sub.ID)
	if _, e = chat.UpdateProfile(f.ctx, f.actor, room.ID, req); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("expired cycle admitted", e)
	}
	if _, e = chat.ClearProfile(f.ctx, f.actor, room.ID); e != nil {
		t.Fatal(e)
	}
}
func TestPGChatDetailsModerationAliasesPermissionsTimeoutAndAudit(t *testing.T) {
	f := detailFixture(t)
	realm := relationRealm(t, f, true)
	room := relationRoom(t, f, realm)
	m := relationSend(t, f, f.third, room.ID, "Moderated content")
	deletion := "/api/chat/rooms/" + room.ID + "/messages/" + m.ID
	// Realm moderation never grants the ordinary self-delete route ownership.
	backendCall(t, f.roomHTTP, "DELETE", "/api/chat/"+room.ID+"/messages/"+m.ID, f.otherToken, "", 403)
	backendCall(t, f.roomHTTP, "DELETE", deletion, f.thirdToken, `{"reason":"not a moderator"}`, 403)
	snapshot := func() string {
		var value string
		if e := f.db.QueryRow(`SELECT jsonb_build_object('messages',(SELECT jsonb_agg(to_jsonb(m)ORDER BY id)FROM chat_messages m WHERE chat_room_id=$1),'reactions',(SELECT jsonb_agg(to_jsonb(r)ORDER BY id)FROM chat_reactions r WHERE message_id=$2))::text`, room.ID, m.ID).Scan(&value); e != nil {
			t.Fatal(e)
		}
		return detailSnapshot(t, f.db, room.ID) + value
	}
	before := snapshot()
	query(t, f.db, `CREATE FUNCTION synthetic_chat_moderation_fail() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.room_id::text=TG_ARGV[0] THEN RAISE EXCEPTION 'synthetic audit failure'; END IF; RETURN NEW; END$$`)
	query(t, f.db, fmt.Sprintf(`CREATE TRIGGER synthetic_chat_moderation_fail BEFORE INSERT ON owned_chat_detail_audit FOR EACH ROW EXECUTE FUNCTION synthetic_chat_moderation_fail('%s')`, room.ID))
	backendCall(t, f.roomHTTP, "DELETE", deletion, f.otherToken, `{"reason":"must roll back"}`, 503)
	query(t, f.db, `DROP TRIGGER synthetic_chat_moderation_fail ON owned_chat_detail_audit`)
	query(t, f.db, `DROP FUNCTION synthetic_chat_moderation_fail()`)
	if snapshot() != before {
		t.Fatal("moderation audit failure changed messages, room, member, reaction or audit")
	}
	backendCall(t, f.roomHTTP, "DELETE", deletion, f.otherToken, `{"reason":"moderator reason"}`, 204)
	if n := commerceCount(t, f.db, `SELECT count(*) FROM chat_messages WHERE id=$1 AND deleted_at IS NOT NULL AND content='[Message deleted by moderator]'`, m.ID); n != 1 {
		t.Fatal(n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_chat_detail_audit WHERE message_id=$1 AND action='message.moderated' AND reason='moderator reason'`, m.ID); n != 1 {
		t.Fatal(n)
	}
	timeout := "/api/chat/rooms/" + room.ID + "/members/" + f.third + "/timeout"
	v := backendDecode[rooms.ModerationTimeoutResult](t, backendCall(t, f.roomHTTP, "POST", timeout, f.otherToken, `{"duration_minutes":2,"reason":"timeout reason"}`, 200))
	if !v.Success || v.TimeoutUntil.IsZero() {
		t.Fatal(v)
	}
	backendCall(t, f.roomHTTP, "POST", "/api/chat/"+room.ID+"/messages", f.thirdToken, `{"content":"blocked by timeout"}`, 403)
	backendCall(t, f.roomHTTP, "POST", "/api/chat/rooms/"+room.ID+"/members/"+f.owner+"/timeout", f.otherToken, `{"duration_minutes":2}`, 403)
	query(t, f.db, `UPDATE chat_members SET owned_timeout_until=clock_timestamp()+interval '1 minute' WHERE chat_room_id=$1 AND account_id=$2`, room.ID, f.other)
	backendCall(t, f.roomHTTP, "POST", timeout, f.otherToken, `{"duration_minutes":2}`, 403)
	nonrealm := detailRoom(t, f, "no-realm", false)
	own := relationSend(t, f, f.owner, nonrealm.ID, "Owner content")
	backendCall(t, f.roomHTTP, "DELETE", "/api/chat/rooms/"+nonrealm.ID+"/messages/"+own.ID, f.token, `{}`, 403)
}
func TestPGChatDetailsConcurrentSlugAndWaitRevocation(t *testing.T) {
	f := detailFixture(t)
	var wg sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := f.chats.Create(f.contexts[f.owner], f.owner, rooms.RoomPatch{Name: relationText("Concurrent"), Slug: relationText("race")})
			errorsSeen <- e
		}()
	}
	wg.Wait()
	close(errorsSeen)
	success, conflict := 0, 0
	for e := range errorsSeen {
		if e == nil {
			success++
		} else if errors.Is(e, rooms.ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(success, conflict)
	}
	room := detailRoom(t, f, "wait", false)
	ctx, cancel := context.WithTimeout(f.contexts[f.owner], 12*time.Second)
	defer cancel()
	locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
	blocker, pid := atomicBlocker(t, locks, "chat_rooms", room.ID)
	if _, e := blocker.ExecContext(ctx, `UPDATE chat_members SET leave_at=clock_timestamp() WHERE chat_room_id=$1 AND account_id=$2`, room.ID, f.owner); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, e := f.chats.UpdateProfile(ctx, f.owner, room.ID, rooms.ProfileRequest{Nick: relationText("stale")})
		done <- e
	}()
	waitAtomicBlocked(t, locks, pid, "chat_rooms")
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; e == nil {
		t.Fatal("left member mutated profile")
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM chat_members WHERE chat_room_id=$1 AND owned_nick IS NOT NULL`, room.ID); n != 0 {
		t.Fatal(n)
	}
}
func detailSnapshot(t *testing.T, db *sql.DB, rid string) string {
	t.Helper()
	var out string
	if e := db.QueryRow(`SELECT jsonb_build_object('room',(SELECT to_jsonb(r) FROM chat_rooms r WHERE id=$1),'members',(SELECT jsonb_agg(to_jsonb(m) ORDER BY id) FROM chat_members m WHERE chat_room_id=$1),'audit',(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM owned_chat_detail_audit a WHERE room_id=$1))::text`, rid).Scan(&out); e != nil {
		t.Fatal(e)
	}
	return out
}
func TestPGChatDetailsSavedRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	type record struct {
		Actor, Room, Scope, Slug, Token, Snapshot string
		Authority                                 restartAuthoritySnapshot
	}
	var saved record
	if phase == "before" {
		f, config, token := organizationRestartAuthority(t, newOrganizationFixture(t))
		f.chats.WithProfilePerks(syntheticChatProfilePerk{})
		room := detailRoom(t, f.chatRelationsFixture, "saved-details", true)
		if _, e := f.chats.UpdateProfile(f.contexts[f.owner], f.owner, room.ID, rooms.ProfileRequest{Nick: relationText("Saved nickname")}); e != nil {
			t.Fatal(e)
		}
		saved = record{f.owner, room.ID, detailScope(t, db, f.owner), "saved-details", token, detailSnapshot(t, db, room.ID), saveAuthoritySnapshot(t, config)}
		saveRestartFixture(t, "chat-details.json", saved)
		return
	}
	loadRestartFixture(t, "chat-details.json", &saved)
	if got := detailSnapshot(t, db, saved.Room); got != saved.Snapshot {
		t.Fatal("original room, nickname or audit changed across restart")
	}
	config := restoreAuthoritySnapshot(t, saved.Authority)
	store := authoritypg.New(db)
	verifier, e := authority.NewVerifier(store, config)
	if e != nil {
		t.Fatal(e)
	}
	proof, e := verifier.Authenticate(context.Background(), saved.Token)
	if e != nil {
		t.Fatal("saved proof rejected", e)
	}
	guard := authoritypg.NewTransactionGuard(store, verifier)
	ctx := guard.Bind(context.Background(), proof)
	chat, e := chatpg.NewWithAuthority(db, chatPolicy, guard)
	if e != nil {
		t.Fatal(e)
	}
	chat.WithSocial(socialpg.NewWithAuthority(db, guard))
	room, e := chat.PublicBySlug(ctx, saved.Actor, saved.Scope, saved.Slug)
	if e != nil || room.ID != saved.Room {
		t.Fatal(room, e)
	}
	m, e := chat.Me(ctx, saved.Actor, saved.Room)
	if e != nil || m.Nick == nil || *m.Nick != "Saved nickname" {
		t.Fatal(m, e)
	}
	if _, e = chat.UpdateProfile(ctx, saved.Actor, saved.Room, rooms.ProfileRequest{}); !errors.Is(e, rooms.ErrUnavailable) {
		t.Fatal("configuration silently restored", e)
	}
	if _, e = chat.ClearProfile(ctx, saved.Actor, saved.Room); e != nil {
		t.Fatal("saved proof clear", e)
	}
	config.Now = func() time.Time { return saved.Authority.Now.Add(2 * time.Hour) }
	expired, e := authority.NewVerifier(store, config)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = expired.Authenticate(context.Background(), saved.Token); e == nil {
		t.Fatal("original expiry lost")
	}
}

type chatProfilePerkGate struct {
	provider rooms.ProfileEntitlement
	entered  chan struct{}
	release  <-chan struct{}
	once     sync.Once
}

func (g *chatProfilePerkGate) RequireChatProfile(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, at time.Time) error {
	if e := g.provider.RequireChatProfile(ctx, db, tx, actor, at); e != nil {
		return e
	}
	var wait bool
	g.once.Do(func() { wait = true; close(g.entered) })
	if wait {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-g.release:
		}
	}
	return nil
}
func TestPGChatDetailsPerkExpiresAfterInitialCheckRollsBack(t *testing.T) {
	f := newCommerceFixture(t)
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	sub, e := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: f.plan}, "chat-profile-expiry", false)
	if e != nil {
		t.Fatal(e)
	}
	order, e := f.r.SubscriptionOrder(f.ctx, f.actor, sub.Identifier)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.PayOrder(f.ctx, f.actor, order.ID, &f.wallet.ID); e != nil {
		t.Fatal(e)
	}
	chat, e := chatpg.NewWithAuthority(f.db, chatPolicy, f.guard)
	if e != nil {
		t.Fatal(e)
	}
	chat.WithSocial(socialpg.NewWithAuthority(f.db, f.guard))
	room, e := chat.Create(f.ctx, f.actor, rooms.RoomPatch{Name: relationText("Expiry rollback")})
	if e != nil {
		t.Fatal(e)
	}
	perk, e := f.r.ChatProfilePerks(map[string]int{f.plan: 2})
	if e != nil {
		t.Fatal(e)
	}
	var expires time.Time
	if e = f.db.QueryRow(`SELECT clock_timestamp()+interval '2 seconds'`).Scan(&expires); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE commerce_v1.subscriptions SET ended_at=$2::timestamptz,document=jsonb_set(document,'{ended_at}',to_jsonb($2::timestamptz)) WHERE id=$1::uuid`, sub.ID, expires)
	entered, release := make(chan struct{}), make(chan struct{})
	chat.WithProfilePerks(&chatProfilePerkGate{provider: perk, entered: entered, release: release})
	ctx, cancel := context.WithTimeout(f.ctx, 12*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, e := chat.UpdateProfile(ctx, f.actor, room.ID, rooms.ProfileRequest{Nick: relationText("must roll back after expiry")})
		done <- e
	}()
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-entered:
	}
	organizationWaitExpired(t, ctx, f.db, expires)
	close(release)
	if e = <-done; !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("expired canonical perk committed", e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM chat_members WHERE chat_room_id=$1 AND owned_nick IS NOT NULL`, room.ID); n != 0 {
		t.Fatal(n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_chat_detail_audit WHERE room_id=$1`, room.ID); n != 0 {
		t.Fatal("expired change audit committed", n)
	}
}
func TestPGChatDetailsOnlineSecondaryPrivacyLockConflict(t *testing.T) {
	f := detailFixture(t)
	room := detailRoom(t, f, "privacy-lock", true)
	if _, e := f.chats.ChangeMember(f.contexts[f.other], f.other, room.ID, rooms.Join, ""); e != nil {
		t.Fatal(e)
	}
	p := presencepg.NewWithAuthority(f.db, f.guard)
	if _, e := p.Heartbeat(f.contexts[f.other], f.other, false); e != nil {
		t.Fatal(e)
	}
	blocker, e := f.db.BeginTx(f.base, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer blocker.Rollback()
	if _, e = blocker.ExecContext(f.base, `SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, f.other); e != nil {
		t.Fatal(e)
	}
	if _, e = blocker.ExecContext(f.base, `INSERT INTO owned_presence_preferences(account_id,online_visibility,last_seen_visibility,status_visibility,updated_at)VALUES($1,'private','private','private',clock_timestamp()) ON CONFLICT(account_id)DO UPDATE SET online_visibility='private'`, f.other); e != nil {
		t.Fatal(e)
	}
	got, e := f.chats.OnlineMembers(f.base, "", room.ID)
	if !errors.Is(e, rooms.ErrConflict) || got.OnlineCount != 0 || len(got.OnlineAccounts) != 0 {
		t.Fatal("in-flight privacy exposed", got, e)
	}
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	got, e = f.chats.OnlineMembers(f.base, "", room.ID)
	if e != nil || got.OnlineCount != 0 {
		t.Fatal("private member exposed", got, e)
	}
	// A relation/table wait inside status projection must use the database clock
	// after the wait. This also catches a stale Go timestamp for the whole batch.
	query(t, f.db, `UPDATE owned_presence_preferences SET online_visibility='public' WHERE account_id=$1`, f.other)
	var expires time.Time
	if e = f.db.QueryRow(`SELECT clock_timestamp()+interval '1 second'`).Scan(&expires); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_presence_sessions SET last_seen_at=$2::timestamptz-interval '1 second',expires_at=$2::timestamptz,idle_since=NULL WHERE account_id=$1::uuid`, f.other, expires)
	ctx, cancel := context.WithTimeout(f.base, 12*time.Second)
	defer cancel()
	hold, e := f.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer hold.Rollback()
	var pid int
	if e = hold.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = hold.ExecContext(ctx, `LOCK TABLE owned_presence_statuses IN ACCESS EXCLUSIVE MODE`); e != nil {
		t.Fatal(e)
	}
	type result struct {
		value rooms.OnlineMembers
		err   error
	}
	done := make(chan result, 1)
	go func() { value, err := f.chats.OnlineMembers(ctx, "", room.ID); done <- result{value, err} }()
	waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: ctx}, pid, "owned_presence_statuses")
	organizationWaitExpired(t, ctx, f.db, expires)
	if e = hold.Commit(); e != nil {
		t.Fatal(e)
	}
	v := <-done
	if v.err != nil || v.value.OnlineCount != 0 || len(v.value.OnlineAccounts) != 0 {
		t.Fatal("lease survived projection wait", v)
	}
}
func TestPGChatDetailsProfileWaitRechecksOriginalSessionAndScope(t *testing.T) {
	for _, mode := range []string{"session", "scope", "disabled", "expiry"} {
		t.Run(mode, func(t *testing.T) {
			f := detailFixture(t)
			clock := &atomic.Int64{}
			clock.Store(time.Now().UTC().Unix())
			if mode == "expiry" {
				config := authorityConfig(t, authorityKeys(t), clock)
				config.AccessTTL = time.Second
				store := authoritypg.New(f.db)
				issuer, x := authority.New(store, config)
				if x != nil {
					t.Fatal(x)
				}
				f.token = pgIssue(t, issuer, f.owner).Token
				f.verifier, x = authority.NewVerifier(store, config)
				if x != nil {
					t.Fatal(x)
				}
				proof, x := f.verifier.Authenticate(f.base, f.token)
				if x != nil {
					t.Fatal(x)
				}
				f.guard = authoritypg.NewTransactionGuard(store, f.verifier)
				f.contexts[f.owner] = f.guard.Bind(f.base, proof)
				f.chats, x = chatpg.NewWithAuthority(f.db, chatPolicy, f.guard)
				if x != nil {
					t.Fatal(x)
				}
				f.chats.WithSocial(socialpg.NewWithAuthority(f.db, f.guard)).WithProfilePerks(syntheticChatProfilePerk{})
			}
			room := detailRoom(t, f, "authority-wait", false)
			before := detailSnapshot(t, f.db, room.ID)
			proof, e := f.verifier.Authenticate(f.base, f.token)
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(f.contexts[f.owner], 12*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			blocker, pid := atomicBlocker(t, locks, "owned_authority_accounts", f.owner)
			if mode == "session" {
				_, e = blocker.ExecContext(ctx, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp(),epoch=epoch+1 WHERE id=$1`, proof.Session.ID)
			} else if mode == "scope" {
				_, e = blocker.ExecContext(ctx, `INSERT INTO owned_authority_grants(account_id,pattern,deny)VALUES($1,'chat.members.manage',true)`, f.owner)
			} else if mode == "disabled" {
				_, e = blocker.ExecContext(ctx, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.owner)
			}
			if e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() {
				_, e := f.chats.UpdateProfile(ctx, f.owner, room.ID, rooms.ProfileRequest{Nick: relationText("stale original proof")})
				done <- e
			}()
			waitAtomicBlocked(t, locks, pid, "owned_authority_accounts")
			if mode == "expiry" {
				clock.Add(2)
			}
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			e = <-done
			if mode == "session" && !errors.Is(e, authority.ErrRevoked) || mode == "scope" && !errors.Is(e, authority.ErrForbidden) || mode == "disabled" && !errors.Is(e, authority.ErrDisabled) || mode == "expiry" && !errors.Is(e, authority.ErrExpired) {
				t.Fatal(mode, e)
			}
			if got := detailSnapshot(t, f.db, room.ID); got != before {
				t.Fatal("denied profile wait changed room/member/audit state")
			}
		})
	}
}
