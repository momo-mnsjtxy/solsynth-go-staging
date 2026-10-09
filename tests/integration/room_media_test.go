// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/internal/mediastore"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/roommediapg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/passport/social"
)

type roomMediaFixture struct {
	chatMediaFixture
	images *roommediapg.Repository
}

func roomMediaPool(t *testing.T, f chatMediaFixture, db *sql.DB) roomMediaFixture {
	t.Helper()
	f = chatMediaPool(t, f, db)
	links := roommediapg.New(db, f.chat, 77)
	f.chat.WithRoomMedia(links)
	f.repo.WithReadVisibility(mediapg.AnyReadVisibility{links, f.links})
	if e := links.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	return roomMediaFixture{f, links}
}
func newRoomMediaFixture(t *testing.T) roomMediaFixture {
	t.Helper()
	f := newChatMediaFixture(t)
	return roomMediaPool(t, f, f.db)
}
func roomMediaFile(t *testing.T, f roomMediaFixture, key string, other, public bool) media.File {
	t.Helper()
	body := string(profilePNG(t))
	u := mediaUpload(key, body, public)
	u.Name = "room.png"
	u.MIMEType = "image/png"
	ctx, actor := f.ctx, f.actor
	if other {
		ctx, actor = f.otherCtx, f.other
	}
	file, _, e := f.repo.Upload(ctx, actor, u, strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	return file
}
func roomMediaCount(t *testing.T, f roomMediaFixture, rid string) int {
	t.Helper()
	var n int
	if e := f.db.QueryRow(`SELECT count(*) FROM chat_messages WHERE chat_room_id=$1 AND type='system.chat.updated'`, rid).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func roomMediaImage(t *testing.T, v rooms.Room, slot, id string) {
	t.Helper()
	p := v.Picture
	if slot == "background" {
		p = v.Background
	}
	if id == "" {
		if p != nil {
			t.Fatal("unexpected image", p)
		}
		return
	}
	if p["id"] != id {
		t.Fatal(slot, p, id)
	}
}
func TestPGRoomMediaCanonicalLifecycleAndAudience(t *testing.T) {
	f := newRoomMediaFixture(t)
	file := roomMediaFile(t, f, "one", false, false)
	second := roomMediaFile(t, f, "two", false, false)
	name := "room images"
	room, e := f.chat.Create(f.ctx, f.actor, rooms.RoomPatch{Name: &name, PictureID: &file.ID, BackgroundID: &file.ID})
	if e != nil {
		t.Fatal(e)
	}
	roomMediaImage(t, room, "picture", file.ID)
	roomMediaImage(t, room, "background", file.ID)
	var used int64
	if e = f.db.QueryRow(`SELECT used_bytes FROM owned_media_quotas WHERE account_id=$1`, f.actor).Scan(&used); e != nil || used != file.Size+second.Size {
		t.Fatal(used, e)
	}
	noChatMedia(t, f.chatMediaFixture, context.Background(), "", file.ID)
	noChatMedia(t, f.chatMediaFixture, f.otherCtx, f.other, file.ID)
	chatMediaJoin(t, f.chatMediaFixture, room.ID)
	mediaRead(t, f.repo, f.otherCtx, f.other, file.ID, string(profilePNG(t)))
	list, e := f.chat.List(f.otherCtx, f.other, 100, 0)
	if e != nil || len(list) != 1 {
		t.Fatal(list, e)
	}
	roomMediaImage(t, list[0], "picture", file.ID)
	room, e = f.chat.Update(f.ctx, f.actor, room.ID, rooms.RoomPatch{PictureID: &second.ID})
	if e != nil {
		t.Fatal(e)
	}
	roomMediaImage(t, room, "picture", second.ID)
	mediaRead(t, f.repo, f.otherCtx, f.other, file.ID, string(profilePNG(t)))
	if _, e = f.chat.Update(f.ctx, f.actor, room.ID, rooms.RoomPatch{PictureID: &second.ID}); e != nil {
		t.Fatal(e)
	}
	if roomMediaCount(t, f, room.ID) != 1 {
		t.Fatal("same reference emitted duplicate event")
	}
	if _, e = f.chat.Update(f.ctx, f.actor, room.ID, rooms.RoomPatch{RemoveBackground: true}); e != nil {
		t.Fatal(e)
	}
	noChatMedia(t, f.chatMediaFixture, f.otherCtx, f.other, file.ID)
	if _, e = f.chat.Update(f.ctx, f.actor, room.ID, rooms.RoomPatch{RemoveBackground: true}); e != nil {
		t.Fatal(e)
	}
	if roomMediaCount(t, f, room.ID) != 2 {
		t.Fatal("remove replay emitted duplicate event")
	}
	public := true
	if _, e = f.chat.Update(f.ctx, f.actor, room.ID, rooms.RoomPatch{IsPublic: &public}); e != nil {
		t.Fatal(e)
	}
	mediaRead(t, f.repo, context.Background(), "", second.ID, string(profilePNG(t)))
	public = false
	if _, e = f.chat.Update(f.ctx, f.actor, room.ID, rooms.RoomPatch{IsPublic: &public}); e != nil {
		t.Fatal(e)
	}
	noChatMedia(t, f.chatMediaFixture, context.Background(), "", second.ID)
	if _, e = f.chat.ChangeMember(f.ctx, f.actor, room.ID, rooms.Remove, f.other); e != nil {
		t.Fatal(e)
	}
	noChatMedia(t, f.chatMediaFixture, f.otherCtx, f.other, second.ID)
	if _, e = f.repo.Delete(f.ctx, f.actor, second.ID); e != nil {
		t.Fatal(e)
	}
	room, e = f.chat.Get(f.ctx, f.actor, room.ID)
	if e != nil {
		t.Fatal(e)
	}
	roomMediaImage(t, room, "picture", "")
	noChatMedia(t, f.chatMediaFixture, f.ctx, f.actor, second.ID)
	var pub bool
	if e = f.db.QueryRow(`SELECT is_public FROM owned_media_files WHERE id=$1`, file.ID).Scan(&pub); e != nil || pub {
		t.Fatal("link changed canonical privacy", pub, e)
	}
}
func TestPGRoomMediaOwnershipAndAtomicRollback(t *testing.T) {
	f := newRoomMediaFixture(t)
	room := chatMediaRoom(t, f.chatMediaFixture, false)
	own := roomMediaFile(t, f, "own", false, false)
	foreign := roomMediaFile(t, f, "foreign", true, true)
	changed := "must roll back"
	if _, e := f.chat.Update(f.ctx, f.actor, room.ID, rooms.RoomPatch{Name: &changed, PictureID: &foreign.ID}); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal(e)
	}
	chatMediaJoin(t, f.chatMediaFixture, room.ID)
	if _, e := f.chat.Update(f.otherCtx, f.other, room.ID, rooms.RoomPatch{PictureID: &foreign.ID}); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("member became manager", e)
	}
	text := chatMediaFile(t, f.chatMediaFixture, "text", "not an image")
	if _, e := f.chat.Update(f.ctx, f.actor, room.ID, rooms.RoomPatch{PictureID: &text.ID}); !errors.Is(e, rooms.ErrInvalid) {
		t.Fatal(e)
	}
	// Failure after room/reference mutation and sequence allocation must roll it all back.
	constraint := "room_media_fail_" + strings.ReplaceAll(room.ID, "-", "")
	query(t, f.db, `ALTER TABLE chat_messages ADD CONSTRAINT `+constraint+` CHECK(chat_room_id<>'`+room.ID+`'::uuid OR type<>'system.chat.updated')`)
	if _, e := f.chat.Update(f.ctx, f.actor, room.ID, rooms.RoomPatch{Name: &changed, PictureID: &own.ID}); e == nil {
		t.Fatal("event failure succeeded")
	}
	query(t, f.db, `ALTER TABLE chat_messages DROP CONSTRAINT `+constraint)
	v, e := f.chat.Get(f.ctx, f.actor, room.ID)
	if e != nil || v.Name != room.Name || v.Picture != nil || roomMediaCount(t, f, room.ID) != 0 {
		t.Fatal(v, e)
	}
	var seq int
	if e = f.db.QueryRow(`SELECT owned_next_sequence FROM chat_rooms WHERE id=$1`, room.ID).Scan(&seq); e != nil || seq != 0 {
		t.Fatal(seq, e)
	}
	var before, after int
	if e = f.db.QueryRow(`SELECT count(*) FROM chat_rooms WHERE owned_owner_id=$1`, f.actor).Scan(&before); e != nil {
		t.Fatal(e)
	}
	if _, e = f.chat.Create(f.ctx, f.actor, rooms.RoomPatch{Name: &changed, PictureID: &own.ID, BackgroundID: &foreign.ID}); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal(e)
	}
	if e = f.db.QueryRow(`SELECT count(*) FROM chat_rooms WHERE owned_owner_id=$1`, f.actor).Scan(&after); e != nil || before != after {
		t.Fatal("failed create persisted room", before, after, e)
	}
	if _, e = f.chat.Update(f.ctx, f.actor, room.ID, rooms.RoomPatch{PictureID: &own.ID}); e != nil {
		t.Fatal(e)
	}
	if e = f.chat.Delete(f.ctx, f.actor, room.ID); e != nil {
		t.Fatal(e)
	}
	noChatMedia(t, f.chatMediaFixture, f.otherCtx, f.other, own.ID)
}
func TestPGRoomMediaWaitRevalidatesFileMembershipAndAuthority(t *testing.T) {
	for _, kind := range []string{"file", "membership", "grant", "expiry"} {
		t.Run(kind, func(t *testing.T) {
			f := newRoomMediaFixture(t)
			var clock atomic.Int64
			clock.Store(f.config.Now().Unix())
			if kind == "expiry" {
				f.config.Now = func() time.Time { return time.Unix(clock.Load(), 0) }
				f = roomMediaPool(t, f.chatMediaFixture, f.db)
			}
			room := chatMediaRoom(t, f.chatMediaFixture, false)
			file := roomMediaFile(t, f, "wait", false, false)
			ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			table, id := "owned_media_files", file.ID
			if kind == "membership" {
				table, id = "chat_rooms", room.ID
			}
			if kind == "grant" {
				table, id = "owned_authority_accounts", f.actor
			}
			blocker, pid := atomicBlocker(t, locks, table, id)
			switch kind {
			case "file":
				_, _ = blocker.Exec(`UPDATE owned_media_files SET state='deleting' WHERE id=$1`, file.ID)
			case "membership":
				_, _ = blocker.Exec(`UPDATE chat_members SET leave_at=clock_timestamp() WHERE chat_room_id=$1 AND account_id=$2`, room.ID, f.actor)
			case "grant":
				_, _ = blocker.Exec(`DELETE FROM owned_authority_grants WHERE account_id=$1`, f.actor)
			}
			done := make(chan error, 1)
			go func() { _, e := f.chat.Update(ctx, f.actor, room.ID, rooms.RoomPatch{PictureID: &file.ID}); done <- e }()
			waitAtomicBlocked(t, locks, pid, table)
			if kind == "expiry" {
				clock.Add(int64((11 * time.Minute) / time.Second))
			}
			if e := blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			e := <-done
			want := error(rooms.ErrForbidden)
			if kind == "file" {
				want = rooms.ErrNotFound
			}
			if kind == "grant" {
				want = authority.ErrForbidden
			}
			if kind == "expiry" {
				want = authority.ErrExpired
			}
			if !errors.Is(e, want) {
				t.Fatal(kind, e)
			}
			var n int
			if e = f.db.QueryRow(`SELECT count(*) FROM owned_chat_room_media WHERE room_id=$1`, room.ID).Scan(&n); e != nil || n != 0 || roomMediaCount(t, f, room.ID) != 0 {
				t.Fatal(n, e)
			}
		})
	}
}

func TestPGRoomMediaRealmRevocationAndDirectAudience(t *testing.T) {
	f := newRoomMediaFixture(t)
	// Realm creation requires a current canonical quota grant; media fixtures
	// otherwise provision only media and chat authority.
	query(t, f.db, `INSERT INTO owned_realm_quota(account_id,total,starts_at)VALUES($1,1,clock_timestamp()-interval '1 second')`, f.actor)
	socialRepo := socialpg.New(f.db)
	realm := socialRealm(t, socialRepo, f.actor, true, true)
	realmPermissionJoin(t, socialRepo, f.other, realm.ID)
	query(t, f.db, `UPDATE owned_realms SET boost_level=2 WHERE id=$1`, realm.ID)
	realmPermissionRole(t, socialRepo, f.actor, realm.ID, f.other, 50)
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_realms r JOIN owned_realm_members m ON m.realm_id=r.id WHERE r.id=$1 AND r.boost_level=2 AND m.account_id=$2 AND m.role=50 AND m.joined_at IS NOT NULL AND m.leave_at IS NULL`, realm.ID, f.other); n != 1 {
		t.Fatal("moderator fixture not established", n)
	}
	name := "realm images"
	room, e := f.chat.Create(f.ctx, f.actor, rooms.RoomPatch{Name: &name, RealmID: &realm.ID})
	if e != nil {
		t.Fatal(e)
	}
	chatMediaJoin(t, f.chatMediaFixture, room.ID)
	file := roomMediaFile(t, f, "moderator", true, false)
	if _, e = f.chat.Update(f.otherCtx, f.other, room.ID, rooms.RoomPatch{PictureID: &file.ID}); e != nil {
		t.Fatal(e)
	}
	mediaRead(t, f.repo, f.ctx, f.actor, file.ID, string(profilePNG(t)))
	no := false
	if _, e = socialRepo.SetUserPermission(context.Background(), f.actor, realm.ID, social.UserPermissionRequest{AccountID: f.other, PermissionOverrides: social.PermissionOverrides{CanUploadMedia: &no}}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.chat.Update(f.otherCtx, f.other, room.ID, rooms.RoomPatch{PictureID: &file.ID}); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("upload permission revoked", e)
	}
	// Removing one's prior room grant remains possible without upload permission.
	if _, e = f.chat.Update(f.otherCtx, f.other, room.ID, rooms.RoomPatch{RemovePicture: true}); e != nil {
		t.Fatal(e)
	}
	if _, e = socialRepo.SetUserPermission(context.Background(), f.actor, realm.ID, social.UserPermissionRequest{AccountID: f.other}); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(f.otherCtx, 10*time.Second)
	defer cancel()
	locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
	blocker, pid := atomicBlocker(t, locks, "owned_realms", realm.ID)
	if _, e = blocker.Exec(`UPDATE owned_realm_role_permissions SET permission_mask=permission_mask & (~32) WHERE realm_id=$1 AND role_level=50`, realm.ID); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := f.chat.Update(ctx, f.other, room.ID, rooms.RoomPatch{PictureID: &file.ID}); done <- e }()
	waitAtomicBlocked(t, locks, pid, "owned_realms")
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("wait retained realm moderation", e)
	}
	own := roomMediaFile(t, f, "owner", false, false)
	if _, e = f.chat.Update(f.ctx, f.actor, room.ID, rooms.RoomPatch{PictureID: &own.ID}); e != nil {
		t.Fatal(e)
	}
	if _, e = socialRepo.ChangeMember(context.Background(), f.other, realm.ID, social.MemberChange{Action: social.Leave}); e != nil {
		t.Fatal(e)
	}
	noChatMedia(t, f.chatMediaFixture, f.otherCtx, f.other, own.ID)
	// A direct room shares images only while both canonical participants remain admitted.
	direct, e := f.chat.CreateDirect(f.ctx, f.actor, rooms.DirectRequest{RelatedUserID: f.other})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.chat.Update(f.ctx, f.actor, direct.ID, rooms.RoomPatch{PictureID: &own.ID}); e != nil {
		t.Fatal(e)
	}
	mediaRead(t, f.repo, f.otherCtx, f.other, own.ID, string(profilePNG(t)))
	if _, e = f.chat.ChangeDirect(f.otherCtx, f.other, direct.ID, "block"); e != nil {
		t.Fatal(e)
	}
	noChatMedia(t, f.chatMediaFixture, f.otherCtx, f.other, own.ID)
}

func TestPGRoomMediaReadWaitObservesRevokedGrantAndFileOwner(t *testing.T) {
	for _, kind := range []string{"reference", "membership", "owner", "room"} {
		t.Run(kind, func(t *testing.T) {
			f := newRoomMediaFixture(t)
			file := roomMediaFile(t, f, "readwait", false, false)
			room := chatMediaRoom(t, f.chatMediaFixture, false)
			chatMediaJoin(t, f.chatMediaFixture, room.ID)
			if _, e := f.chat.Update(f.ctx, f.actor, room.ID, rooms.RoomPatch{PictureID: &file.ID}); e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(f.otherCtx, 10*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			blocker, pid := atomicBlocker(t, locks, "owned_media_files", file.ID)
			var e error
			switch kind {
			case "reference":
				_, e = blocker.Exec(`DELETE FROM owned_chat_room_media WHERE room_id=$1`, room.ID)
			case "membership":
				_, e = blocker.Exec(`UPDATE chat_members SET leave_at=clock_timestamp() WHERE chat_room_id=$1 AND account_id=$2`, room.ID, f.other)
			case "owner":
				_, e = blocker.Exec(`UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
			case "room":
				_, e = blocker.Exec(`UPDATE chat_rooms SET deleted_at=clock_timestamp() WHERE id=$1`, room.ID)
			}
			if e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() {
				_, body, e := f.repo.Open(ctx, f.other, file.ID)
				if body != nil {
					body.Close()
				}
				done <- e
			}()
			waitAtomicBlocked(t, locks, pid, "owned_media_files")
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-done; !errors.Is(e, media.ErrNotFound) {
				t.Fatal("stale image grant", kind, e)
			}
		})
	}
}

func TestPGRoomMediaEncryptedAudienceDoesNotUsePublicFlag(t *testing.T) {
	f := newRoomMediaFixture(t)
	f.chat.WithMLSDelivery()
	file := roomMediaFile(t, f, "mls", false, false)
	room := chatMediaRoom(t, f.chatMediaFixture, true)
	chatMediaJoin(t, f.chatMediaFixture, room.ID)
	if _, e := f.chat.Update(f.ctx, f.actor, room.ID, rooms.RoomPatch{PictureID: &file.ID}); e != nil {
		t.Fatal(e)
	}
	mediaRead(t, f.repo, context.Background(), "", file.ID, string(profilePNG(t)))
	if _, e := f.chat.MLS(f.ctx, f.actor, "", "enable", room.ID, rooms.MLSRequest{}); e != nil {
		t.Fatal(e)
	}
	noChatMedia(t, f.chatMediaFixture, context.Background(), "", file.ID)
	mediaRead(t, f.repo, f.otherCtx, f.other, file.ID, string(profilePNG(t)))
	// Room artwork is ordinary member-visible room metadata, not MLS ciphertext.
	if _, e := f.chat.Update(f.ctx, f.actor, room.ID, rooms.RoomPatch{BackgroundID: &file.ID}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.chat.ChangeMember(f.ctx, f.actor, room.ID, rooms.Remove, f.other); e != nil {
		t.Fatal(e)
	}
	noChatMedia(t, f.chatMediaFixture, f.otherCtx, f.other, file.ID)
}

func TestPGRoomMediaConcurrentSlotsAndBackendComposition(t *testing.T) {
	f := newRoomMediaFixture(t)
	one := roomMediaFile(t, f, "concurrent-one", false, false)
	two := roomMediaFile(t, f, "concurrent-two", false, false)
	room := chatMediaRoom(t, f.chatMediaFixture, false)
	var wg sync.WaitGroup
	fail := make(chan error, 2)
	for _, p := range []rooms.RoomPatch{{PictureID: &one.ID}, {BackgroundID: &two.ID}} {
		wg.Add(1)
		go func(p rooms.RoomPatch) { defer wg.Done(); _, e := f.chat.Update(f.ctx, f.actor, room.ID, p); fail <- e }(p)
	}
	wg.Wait()
	close(fail)
	for e := range fail {
		if e != nil {
			t.Fatal(e)
		}
	}
	v, e := f.chat.Get(f.ctx, f.actor, room.ID)
	if e != nil {
		t.Fatal(e)
	}
	roomMediaImage(t, v, "picture", one.ID)
	roomMediaImage(t, v, "background", two.ID)
	syncResult, e := f.reader.Sync(context.Background(), messager.SyncQuery{RoomID: room.ID, AccountID: f.actor})
	if e != nil || len(syncResult.Messages) != 2 {
		t.Fatal(syncResult, e)
	}
	for _, m := range syncResult.Messages {
		if m.Type != "system.chat.updated" || !strings.Contains(string(m.Meta), "chat_info_updated") {
			t.Fatal(m)
		}
	}
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, MediaStore: f.store, MaxMediaBytes: 65536})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	httpRoom := backendDecode[rooms.Room](t, backendCall(t, app, "POST", "/messager/chat", f.token, fmt.Sprintf(`{"name":"images HTTP","picture_id":%q,"background_id":%q}`, one.ID, two.ID), 200))
	roomMediaImage(t, httpRoom, "picture", one.ID)
	target := "/messager/chat/" + httpRoom.ID
	retained := backendDecode[rooms.Room](t, backendCall(t, app, "PATCH", target, f.token, `{"picture_id":null,"background_id":null}`, 200))
	roomMediaImage(t, retained, "picture", one.ID)
	roomMediaImage(t, retained, "background", two.ID)
	backendCall(t, app, "GET", "/api/files/"+one.ID, "", "", 404)
	backendCall(t, app, "GET", "/api/media/files/"+one.ID, f.otherToken, "", 404)
	backendCall(t, app, "DELETE", target+"/media/picture", f.otherToken, "", 403)
	backendCall(t, app, "DELETE", target+"/media/picture", f.token, "", 204)
	backendCall(t, app, "DELETE", target+"/media/background", f.token, "", 204)
	cleared := backendDecode[rooms.Room](t, backendCall(t, app, "GET", target, f.token, "", 200))
	roomMediaImage(t, cleared, "picture", "")
	roomMediaImage(t, cleared, "background", "")
	backendCall(t, app, "PATCH", target, f.token, `{"picture_id":"https://example.invalid/image.png"}`, 400)
}

type roomMediaSaved struct{ Room, File, Removed, Actor, Objects, Snapshot string }

func roomMediaSnapshot(t *testing.T, db *sql.DB, rid string) string {
	t.Helper()
	var out string
	e := db.QueryRow(`SELECT jsonb_build_object('room',(SELECT to_jsonb(r) FROM chat_rooms r WHERE id=$1),'media',(SELECT coalesce(jsonb_agg(to_jsonb(l) ORDER BY slot),'[]'::jsonb) FROM owned_chat_room_media l WHERE room_id=$1),'events',(SELECT coalesce(jsonb_agg(to_jsonb(m) ORDER BY room_sequence),'[]'::jsonb) FROM chat_messages m WHERE chat_room_id=$1))::text`, rid).Scan(&out)
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func TestPGRoomMediaSavedRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	if phase == "before" {
		f := newRoomMediaFixture(t)
		objects := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "room-media-objects")
		if e := os.Mkdir(objects, 0700); e != nil {
			t.Fatal(e)
		}
		f.root = objects
		f = roomMediaPool(t, f.chatMediaFixture, db)
		one := roomMediaFile(t, f, "saved", false, false)
		removed := roomMediaFile(t, f, "removed", false, false)
		public := true
		name := "saved room images"
		room, e := f.chat.Create(f.ctx, f.actor, rooms.RoomPatch{Name: &name, IsPublic: &public, PictureID: &one.ID, BackgroundID: &removed.ID})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.chat.Update(f.ctx, f.actor, room.ID, rooms.RoomPatch{RemoveBackground: true}); e != nil {
			t.Fatal(e)
		}
		mediaRead(t, f.repo, context.Background(), "", one.ID, string(profilePNG(t)))
		noChatMedia(t, f.chatMediaFixture, context.Background(), "", removed.ID)
		saveRestartFixture(t, "room-media.json", roomMediaSaved{room.ID, one.ID, removed.ID, f.actor, objects, roomMediaSnapshot(t, db, room.ID)})
		return
	}
	var state roomMediaSaved
	loadRestartFixture(t, "room-media.json", &state)
	if snapshot := roomMediaSnapshot(t, db, state.Room); snapshot != state.Snapshot {
		t.Fatal("saved room/media/event state changed across restart")
	}
	chat, e := chatpg.New(db, chatPolicy)
	if e != nil {
		t.Fatal(e)
	}
	chat.WithSocial(socialpg.New(db))
	links := roommediapg.New(db, chat, 77)
	chat.WithRoomMedia(links)
	v, e := chat.Get(context.Background(), "", state.Room)
	if e != nil {
		t.Fatal(e)
	}
	roomMediaImage(t, v, "picture", state.File)
	roomMediaImage(t, v, "background", "")
	store, e := mediastore.NewFilesystem(state.Objects, 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	repo, e := mediapg.New(db, store, authoritypg.NewTransactionGuard(authoritypg.New(db), nil), mediapg.Config{MaxFileBytes: 1 << 20, PendingTTL: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	repo.WithReadVisibility(links)
	mediaRead(t, repo, context.Background(), "", state.File, string(profilePNG(t)))
	_, body, e := repo.Open(context.Background(), "", state.Removed)
	if body != nil {
		body.Close()
	}
	if !errors.Is(e, media.ErrNotFound) {
		t.Fatal("removed grant revived", e)
	}
	var meta []byte
	if e = db.QueryRow(`SELECT meta FROM chat_messages WHERE chat_room_id=$1 AND type='system.chat.updated'`, state.Room).Scan(&meta); e != nil {
		t.Fatal(e)
	}
	var event map[string]any
	if json.Unmarshal(meta, &event) != nil || event["event"] != "chat_info_updated" {
		t.Fatal(string(meta))
	}
}
