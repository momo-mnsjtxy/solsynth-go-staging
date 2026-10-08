// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/callpg"
	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/notificationpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/calls"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/passport/social"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type chatRelationsFixture struct {
	callFixture
	chats                   *chatpg.Repository
	social                  *socialpg.Repository
	read                    *messagerpg.Repository
	notifications           *notificationpg.Repository
	roomHTTP                http.Handler
	contexts                map[string]context.Context
	outsider, outsiderToken string
	base                    context.Context
}

func newChatRelationsFixture(t *testing.T) chatRelationsFixture {
	t.Helper()
	f := chatRelationsFixture{callFixture: newCallFixture(t), outsider: authorityUUID(t), contexts: map[string]context.Context{}}
	for _, schema := range []string{accountpg.Schema, socialpg.Schema, chatpg.RelationsSchema} {
		query(t, f.db, schema)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	f.base = ctx
	if e := authoritypg.SeedSynthetic(ctx, f.db, f.outsider, "synthetic_chat_relations_"+strings.ReplaceAll(f.outsider, "-", ""), time.Now().UTC()); e != nil {
		t.Fatal(e)
	}
	if e := authoritypg.New(f.db).ReplacePolicy(ctx, f.outsider, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	f.outsiderToken = pgIssue(t, f.issuer, f.outsider).Token
	for _, p := range []struct{ id, token string }{{f.owner, f.token}, {f.other, f.otherToken}, {f.third, f.thirdToken}, {f.outsider, f.outsiderToken}} {
		proof, e := f.verifier.Authenticate(ctx, p.token)
		if e != nil {
			t.Fatal(e)
		}
		f.contexts[p.id] = f.guard.Bind(ctx, proof)
		query(t, f.db, `INSERT INTO owned_realm_quota(account_id,total,starts_at)VALUES($1,10,clock_timestamp()-interval '1 second')`, p.id)
	}
	f.social = socialpg.NewWithAuthority(f.db, f.guard)
	var e error
	f.chats, e = chatpg.NewWithAuthority(f.db, chatPolicy, f.guard)
	if e != nil {
		t.Fatal(e)
	}
	f.chats.WithSocial(f.social)
	f.read, e = messagerpg.NewWithIndependentPolicy(f.db, messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30})
	if e != nil {
		t.Fatal(e)
	}
	f.read.WithRoomAccess(f.chats)
	f.repo.WithRoomAccess(f.chats)
	f.notifications = notificationpg.New(f.db, f.guard, "dev.solsynth.solian").WithRoomAccess(f.chats)
	f.roomHTTP = (&rooms.Service{Repository: f.chats, Authority: f.verifier, Reader: f.read, Bind: f.guard.Bind}).Handler()
	if e = f.chats.RelationsReady(ctx); e != nil {
		t.Fatal(e)
	}
	return f
}
func relationText(v string) *string { return &v }
func relationDirect(t *testing.T, f chatRelationsFixture) rooms.Room {
	t.Helper()
	v, e := f.chats.CreateDirect(f.contexts[f.owner], f.owner, rooms.DirectRequest{RelatedUserID: f.other})
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func relationSend(t *testing.T, f chatRelationsFixture, actor, rid, body string) messager.MessageProjection {
	t.Helper()
	v, e := f.chats.Send(f.contexts[actor], actor, rid, rooms.TextRequest{Content: body})
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func relationRead(f chatRelationsFixture, actor, rid string) error {
	_, e := f.read.ListMessages(f.base, messager.MessageListQuery{RoomID: rid, AccountID: actor, Take: 20})
	return e
}
func relationRealm(t *testing.T, f chatRelationsFixture, public bool) social.Realm {
	t.Helper()
	slug := "synthetic-realm-" + authorityUUID(t)
	name := "Synthetic linked realm"
	v, e := f.social.CreateRealm(f.contexts[f.owner], f.owner, social.RealmPatch{Slug: &slug, Name: &name, IsPublic: &public, IsCommunity: &public})
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range []struct {
		id   string
		role social.Role
	}{{f.other, social.Moderator}, {f.third, social.Normal}} {
		if _, e = f.social.ChangeMember(f.contexts[f.owner], f.owner, v.ID, social.MemberChange{Action: social.Invite, AccountID: entry.id, Role: entry.role}); e != nil {
			t.Fatal(e)
		}
		if _, e = f.social.ChangeMember(f.contexts[entry.id], entry.id, v.ID, social.MemberChange{Action: social.Accept}); e != nil {
			t.Fatal(e)
		}
	}
	return v
}
func relationRoom(t *testing.T, f chatRelationsFixture, realm social.Realm) rooms.Room {
	t.Helper()
	yes := true
	v, e := f.chats.Create(f.contexts[f.other], f.other, rooms.RoomPatch{Name: relationText("Synthetic realm room"), RealmID: &realm.ID, IsPublic: &yes, IsCommunity: &yes})
	if e != nil {
		t.Fatal(e)
	}
	for _, actor := range []string{f.owner, f.third} {
		if _, e = f.chats.ChangeMember(f.contexts[actor], actor, v.ID, rooms.Join, ""); e != nil {
			t.Fatal(e)
		}
	}
	return v
}
func relationProjection(t *testing.T, f chatRelationsFixture) {
	t.Helper()
	notificationDrain(t, func() (int, error) { return f.notifications.ProjectChat(f.base, 100, 77) })
}
func relationEvents(t *testing.T, f chatRelationsFixture, actor string) int {
	t.Helper()
	v, e := f.notifications.Replay(f.contexts[actor], actor, "dev.solsynth.solian", 0, 100)
	if e != nil {
		t.Fatal(e)
	}
	return len(v.Events)
}

func TestPGChatDirectLifecycleAndCrossPathPrivacy(t *testing.T) {
	f := newChatRelationsFixture(t)
	room := relationDirect(t, f)
	if room.Type != 1 || room.IsPublic || len(room.Members) != 2 || len(room.DirectMembers) != 1 || room.DirectMembers[0].Account == nil {
		t.Fatal("canonical direct projection", room)
	}
	if _, e := f.chats.CreateDirect(f.contexts[f.other], f.other, rooms.DirectRequest{RelatedUserID: f.owner}); !errors.Is(e, rooms.ErrConflict) {
		t.Fatal("reverse duplicate pair", e)
	}
	if _, e := f.chats.Send(f.contexts[f.other], f.other, room.ID, rooms.TextRequest{Content: "Unconfirmed sender"}); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal(e)
	}
	relationSend(t, f, f.owner, room.ID, "Synthetic initial message")
	if e := relationRead(f, f.other, room.ID); e != nil {
		t.Fatal(e)
	}
	if e := relationRead(f, f.third, room.ID); !errors.Is(e, messager.ErrSyncForbidden) {
		t.Fatal("nonparticipant read", e)
	}
	relationProjection(t, f)
	if relationEvents(t, f, f.other) == 0 {
		t.Fatal("recipient event missing")
	}
	if _, e := f.repo.Start(f.contexts[f.owner], f.owner, room.ID); !errors.Is(e, calls.ErrForbidden) {
		t.Fatal("call before confirmation", e)
	}
	chatCall(t, f.roomHTTP, "POST", "/api/chat/"+room.ID+"/block", f.otherToken, "{}", 204)
	if _, e := f.chats.Send(f.contexts[f.owner], f.owner, room.ID, rooms.TextRequest{Content: "Blocked delivery"}); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal(e)
	}
	if e := relationRead(f, f.owner, room.ID); !errors.Is(e, messager.ErrSyncForbidden) {
		t.Fatal("sender read after recipient block", e)
	}
	if relationEvents(t, f, f.other) != 0 {
		t.Fatal("blocked recipient replay")
	}
	if _, e := f.chats.GetDirect(f.contexts[f.other], f.other, f.owner); e != nil {
		t.Fatal("own recovery metadata unavailable", e)
	}
	chatCall(t, f.roomHTTP, "DELETE", "/api/chat/"+room.ID+"/block", f.token, "{}", 403)
	chatCall(t, f.roomHTTP, "DELETE", "/api/chat/"+room.ID+"/block", f.otherToken, "{}", 200)
	chatCall(t, f.roomHTTP, "POST", "/api/chat/"+room.ID+"/confirm", f.otherToken, "{}", 200)
	relationSend(t, f, f.other, room.ID, "Synthetic confirmed reply")
	if _, e := f.repo.Start(f.contexts[f.owner], f.owner, room.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.social.ChangeRelationship(f.contexts[f.other], f.other, f.owner, social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	if e := relationRead(f, f.owner, room.ID); !errors.Is(e, messager.ErrSyncForbidden) {
		t.Fatal("social block bypass", e)
	}
	unread, e := f.read.Unread(f.base, f.owner)
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := unread[room.ID]; ok {
		t.Fatal("blocked unread leak")
	}
	if _, e = f.repo.Join(f.contexts[f.owner], f.owner, room.ID, ""); e == nil {
		t.Fatal("blocked call admission")
	}
}

func TestPGChatDirectConcurrentPairAndRollback(t *testing.T) {
	f := newChatRelationsFixture(t)
	var wg sync.WaitGroup
	var winners atomic.Int32
	errs := make(chan error, 8)
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			actor, target := f.owner, f.other
			if i%2 == 1 {
				actor, target = target, actor
			}
			_, e := f.chats.CreateDirect(f.contexts[actor], actor, rooms.DirectRequest{RelatedUserID: target})
			if e == nil {
				winners.Add(1)
			}
			errs <- e
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil && !errors.Is(e, rooms.ErrConflict) {
			t.Fatal(e)
		}
	}
	if winners.Load() != 1 {
		t.Fatal("duplicate direct rooms", winners.Load())
	}
	constraint := "synthetic_chat_relation_fail_" + strings.ReplaceAll(f.owner, "-", "")
	query(t, f.db, `ALTER TABLE owned_chat_relations_audit ADD CONSTRAINT `+constraint+` CHECK(actor_id<>'`+f.owner+`'::uuid)NOT VALID`)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`ALTER TABLE owned_chat_relations_audit DROP CONSTRAINT IF EXISTS ` + constraint)
	})
	if _, e := f.chats.CreateDirect(f.contexts[f.owner], f.owner, rooms.DirectRequest{RelatedUserID: f.third}); e == nil {
		t.Fatal("injected failure committed")
	}
	var n int
	if e := f.db.QueryRow(`SELECT count(*) FROM owned_chat_direct_pairs WHERE(account_low=$1 AND account_high=$2)OR(account_low=$2 AND account_high=$1)`, f.owner, f.third).Scan(&n); e != nil || n != 0 {
		t.Fatal("orphan pair", n, e)
	}
	if e := f.db.QueryRow(`SELECT count(*) FROM chat_rooms room JOIN chat_members member ON member.chat_room_id=room.id WHERE room.owned_type=1 AND room.owned_owner_id=$1 AND member.account_id=$2`, f.owner, f.third).Scan(&n); e != nil || n != 0 {
		t.Fatal("orphan room or member", n, e)
	}
}

func TestPGChatRealmModerationAndRevocationAcrossReaders(t *testing.T) {
	f := newChatRelationsFixture(t)
	realm := relationRealm(t, f, false)
	if _, e := f.chats.Create(f.contexts[f.third], f.third, rooms.RoomPatch{Name: relationText("Denied normal member"), RealmID: &realm.ID}); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("normal member created realm room", e)
	}
	room := relationRoom(t, f, realm)
	if room.RealmID == nil || *room.RealmID != realm.ID || room.Realm == nil {
		t.Fatal("canonical realm link missing")
	}
	relationSend(t, f, f.third, room.ID, "Synthetic realm content")
	relationProjection(t, f)
	chatCall(t, f.roomHTTP, "GET", "/api/realms/"+realm.Slug+"/chat", f.token, "", 200)
	chatCall(t, f.roomHTTP, "GET", "/api/realms/"+realm.Slug+"/chat", f.outsiderToken, "", 403)
	if e := relationRead(f, f.outsider, room.ID); !errors.Is(e, messager.ErrSyncForbidden) {
		t.Fatal("private realm/public room leaked", e)
	}
	closed, e := messagerpg.NewWithIndependentPolicy(f.db, messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = closed.ListMessages(f.base, messager.MessageListQuery{RoomID: room.ID, AccountID: f.owner, Take: 20}); !errors.Is(e, messager.ErrSyncForbidden) {
		t.Fatal("missing access provider opened linked room", e)
	}
	if _, e = f.social.ChangeMember(f.contexts[f.owner], f.owner, realm.ID, social.MemberChange{Action: social.ChangeRole, AccountID: f.other, Role: social.Normal}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.chats.Update(f.contexts[f.other], f.other, room.ID, rooms.RoomPatch{Name: relationText("Must not change")}); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("former moderator retained owner shortcut", e)
	}
	if _, e = f.chats.Update(f.contexts[f.owner], f.owner, room.ID, rooms.RoomPatch{Name: relationText("Synthetic owner update")}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.social.ChangeMember(f.contexts[f.owner], f.owner, realm.ID, social.MemberChange{Action: social.Remove, AccountID: f.third}); e != nil {
		t.Fatal(e)
	}
	if e = relationRead(f, f.third, room.ID); !errors.Is(e, messager.ErrSyncForbidden) {
		t.Fatal("realm removal left message access", e)
	}
	if _, e = f.chats.Send(f.contexts[f.third], f.third, room.ID, rooms.TextRequest{Content: "Must not commit"}); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal(e)
	}
	if relationEvents(t, f, f.third) != 0 {
		t.Fatal("realm removal left replay access")
	}
	if _, e = f.repo.Join(f.contexts[f.third], f.third, room.ID, ""); e == nil {
		t.Fatal("realm removal left call access")
	}
}

func TestPGChatTimeoutAndCallAdmissionRecovery(t *testing.T) {
	f := newChatRelationsFixture(t)
	realm := relationRealm(t, f, false)
	room := relationRoom(t, f, realm)
	message := relationSend(t, f, f.third, room.ID, "Synthetic before timeout")
	c, e := f.repo.Start(f.contexts[f.other], f.other, room.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.Join(f.contexts[f.third], f.third, room.ID, ""); e != nil {
		t.Fatal(e)
	}
	f.callFixture.connect(t, c, f.third, "")
	var now time.Time
	if e = f.db.QueryRow(`SELECT clock_timestamp()`).Scan(&now); e != nil {
		t.Fatal(e)
	}
	until := now.Add(time.Minute).UTC().Truncate(time.Microsecond)
	if e = f.chats.Timeout(f.contexts[f.other], f.other, room.ID, f.owner, &rooms.TimeoutRequest{TimeoutUntil: until}); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("moderator timed out realm owner", e)
	}
	if e = f.chats.Timeout(f.contexts[f.owner], f.owner, room.ID, f.third, &rooms.TimeoutRequest{Reason: relationText("Synthetic moderation"), TimeoutUntil: until}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.chats.Send(f.contexts[f.third], f.third, room.ID, rooms.TextRequest{Content: "Timed out"}); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal(e)
	}
	if _, e = f.chats.Edit(f.contexts[f.third], f.third, room.ID, message.ID, rooms.TextRequest{Content: "Timed-out edit"}); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal(e)
	}
	if e = relationRead(f, f.third, room.ID); e != nil {
		t.Fatal("timeout hid history", e)
	}
	if _, e = f.repo.ProcessDue(f.base, 100); e != nil {
		t.Fatal(e)
	}
	var state string
	if e = f.db.QueryRow(`SELECT state FROM call_v1.members WHERE call_id=$1 AND account_id=$2`, c.ID, f.third).Scan(&state); e != nil || state != "left" {
		t.Fatal("timeout did not revoke admission", state, e)
	}
	query(t, f.db, `UPDATE chat_members SET owned_timeout_until=clock_timestamp()-interval '1 microsecond' WHERE chat_room_id=$1 AND account_id=$2`, room.ID, f.third)
	relationSend(t, f, f.third, room.ID, "Synthetic after timeout")
	if e = f.chats.Timeout(f.contexts[f.owner], f.owner, room.ID, f.third, &rooms.TimeoutRequest{TimeoutUntil: now.Add(-time.Second)}); !errors.Is(e, rooms.ErrInvalid) {
		t.Fatal("past timeout accepted", e)
	}
}

func TestPGChatRealmTransferAndMissingCallPolicy(t *testing.T) {
	f := newChatRelationsFixture(t)
	first := relationRealm(t, f, false)
	second := relationRealm(t, f, false)
	room := relationRoom(t, f, first)
	query(t, f.db, `UPDATE owned_realm_members SET role=0 WHERE realm_id=$1 AND account_id=$2`, second.ID, f.other)
	if _, e := f.chats.Update(f.contexts[f.other], f.other, room.ID, rooms.RoomPatch{RealmID: &second.ID}); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("unauthorized transfer", e)
	}
	v, e := f.chats.Update(f.contexts[f.owner], f.owner, room.ID, rooms.RoomPatch{RealmID: &second.ID})
	if e != nil || v.RealmID == nil || *v.RealmID != second.ID {
		t.Fatal("authorized transfer", v, e)
	}
	closed, e := callpg.New(f.db, f.guard, &callProvider{}, callpg.Config{NoneEncryptionMode: 77, TokenTTL: time.Minute, CallTTL: time.Hour, NotificationApp: "dev.solsynth.solian"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = closed.Start(f.contexts[f.owner], f.owner, room.ID); !errors.Is(e, calls.ErrForbidden) {
		t.Fatal("missing call policy opened linked room", e)
	}
	query(t, f.db, `UPDATE owned_realm_members SET leave_at=clock_timestamp() WHERE realm_id=$1 AND account_id=$2`, second.ID, f.third)
	if e = relationRead(f, f.third, room.ID); !errors.Is(e, messager.ErrSyncForbidden) {
		t.Fatal("old realm membership authorized transferred room", e)
	}
}

func TestPGChatRelationsDisableAndProofDuringLockWait(t *testing.T) {
	f := newChatRelationsFixture(t)
	room := relationDirect(t, f)
	if _, e := f.chats.ChangeDirect(f.contexts[f.other], f.other, room.ID, "confirm"); e != nil {
		t.Fatal(e)
	}
	if _, e := f.chats.Send(context.Background(), f.owner, room.ID, rooms.TextRequest{Content: "Unbound"}); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal(e)
	}
	base := atomicAuthorityFixture{db: f.db, ctx: f.base, actor: f.other}
	blocker, pid := atomicBlocker(t, base, "owned_authority_accounts", f.other)
	if _, e := blocker.ExecContext(f.base, `UPDATE owned_authority_accounts SET disabled=true,version=version+1 WHERE id=$1`, f.other); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, e := f.chats.Send(f.contexts[f.owner], f.owner, room.ID, rooms.TextRequest{Content: "Disabled peer"})
		done <- e
	}()
	waitAtomicBlocked(t, base, pid, "owned_authority_accounts")
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("peer disable crossed send boundary", e)
	}
	if e := relationRead(f, f.owner, room.ID); !errors.Is(e, messager.ErrSyncForbidden) {
		t.Fatal("disabled peer read leaked", e)
	}
}

func TestPGChatRelationsDeletionAndRestartPersistence(t *testing.T) {
	db := database(t)
	path := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "synthetic-chat-relations-restart.json")
	type state struct{ Owner, Peer, Room, Message string }
	var saved state
	if os.Getenv("PG_TEST_PHASE") == "before" {
		f := newChatRelationsFixture(t)
		room := relationDirect(t, f)
		if e := f.chats.Delete(f.contexts[f.owner], f.owner, room.ID); e != nil {
			t.Fatal(e)
		}
		room = relationDirect(t, f)
		if _, e := f.chats.ChangeDirect(f.contexts[f.other], f.other, room.ID, "confirm"); e != nil {
			t.Fatal(e)
		}
		message := relationSend(t, f, f.other, room.ID, "Synthetic persisted direct message")
		saved = state{f.owner, f.other, room.ID, message.ID}
		raw, _ := json.Marshal(saved)
		if e := os.WriteFile(path, raw, 0600); e != nil {
			t.Fatal(e)
		}
	} else if os.Getenv("PG_TEST_PHASE") == "after" {
		raw, e := os.ReadFile(path)
		if e != nil || json.Unmarshal(raw, &saved) != nil {
			t.Fatal("missing chat relation state", e)
		}
	} else {
		t.Fatal("PG_TEST_PHASE must be before or after")
	}
	chat, e := chatpg.New(db, chatPolicy)
	if e != nil {
		t.Fatal(e)
	}
	chat.WithSocial(socialpg.New(db))
	room, e := chat.GetDirect(context.Background(), saved.Owner, saved.Peer)
	if e != nil || room.ID != saved.Room || len(room.Members) != 2 {
		t.Fatal("canonical pair did not persist", room, e)
	}
	reader, e := messagerpg.NewWithIndependentPolicy(db, messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30})
	if e != nil {
		t.Fatal(e)
	}
	reader.WithRoomAccess(chat)
	m, e := reader.GetMessage(context.Background(), messager.MessageQuery{RoomID: saved.Room, AccountID: saved.Owner, MessageID: saved.Message})
	if e != nil || m.Content == nil || *m.Content != "Synthetic persisted direct message" {
		t.Fatal("persisted direct message unavailable", e)
	}
}
