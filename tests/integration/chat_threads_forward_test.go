// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
)

func threadRoom(t *testing.T, f chatRelationsFixture, public bool, members ...string) rooms.Room {
	t.Helper()
	r, e := f.chats.Create(f.contexts[f.owner], f.owner, rooms.RoomPatch{Name: relationText("Synthetic thread room"), IsPublic: &public})
	if e != nil {
		t.Fatal(e)
	}
	for _, actor := range members {
		threadJoin(t, f, r.ID, actor)
	}
	return r
}
func threadJoin(t *testing.T, f chatRelationsFixture, rid, actor string) {
	t.Helper()
	if _, e := f.chats.ChangeMember(f.contexts[f.owner], f.owner, rid, rooms.Invite, actor); e != nil {
		t.Fatal(e)
	}
	if _, e := f.chats.ChangeMember(f.contexts[actor], actor, rid, rooms.Accept, ""); e != nil {
		t.Fatal(e)
	}
}
func threadSend(t *testing.T, f chatRelationsFixture, actor, rid string, p rooms.TextRequest) messager.MessageProjection {
	t.Helper()
	m, e := f.chats.Send(f.contexts[actor], actor, rid, p)
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func threadGet(t *testing.T, f chatRelationsFixture, actor, rid, mid string) messager.MessageProjection {
	t.Helper()
	m, e := f.read.GetMessage(f.base, messager.MessageQuery{AccountID: actor, RoomID: rid, MessageID: mid})
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func redirectMeta(t *testing.T, m messager.MessageProjection) map[string]any {
	t.Helper()
	var meta map[string]json.RawMessage
	if e := json.Unmarshal(m.Meta, &meta); e != nil {
		t.Fatal(e)
	}
	var out map[string]any
	if e := json.Unmarshal(meta["redirect"], &out); e != nil {
		t.Fatal("missing server snapshot", string(m.Meta), e)
	}
	return out
}
func noThreadEffects(t *testing.T, f chatRelationsFixture, rid string) {
	t.Helper()
	var n int
	if e := f.db.QueryRowContext(f.base, `SELECT count(*) FROM chat_messages WHERE chat_room_id=$1`, rid).Scan(&n); e != nil || n != 0 {
		t.Fatal("rolled-back message effects", n, e)
	}
	if e := f.db.QueryRowContext(f.base, `SELECT count(*) FROM owned_chat_redirects WHERE destination_room_id=$1`, rid).Scan(&n); e != nil || n != 0 {
		t.Fatal("rolled-back receipt effects", n, e)
	}
	var sequence int64
	if e := f.db.QueryRowContext(f.base, `SELECT owned_next_sequence FROM chat_rooms WHERE id=$1`, rid).Scan(&sequence); e != nil || sequence != 0 {
		t.Fatal("rolled-back sequence", sequence, e)
	}
}

func TestPGChatThreadsRootReplyIsolationAndPagination(t *testing.T) {
	f := newChatRelationsFixture(t)
	room := threadRoom(t, f, false, f.other)
	outside := threadRoom(t, f, false)
	root := threadSend(t, f, f.owner, room.ID, rooms.TextRequest{Content: "Root", IsThreadRoot: true})
	unrelated := relationSend(t, f, f.owner, room.ID, "Other root")
	quoted := threadSend(t, f, f.owner, room.ID, rooms.TextRequest{Content: "Quote", RepliedMessageID: &root.ID})
	first := threadSend(t, f, f.other, room.ID, rooms.TextRequest{Content: "First", ThreadID: &root.ID})
	key := "thread-replay"
	request := rooms.TextRequest{Content: "Second", ThreadID: &root.ID, RepliedMessageID: &first.ID, ClientMessageID: &key}
	second := threadSend(t, f, f.other, room.ID, request)
	if again := threadSend(t, f, f.other, room.ID, request); again.ID != second.ID {
		t.Fatal("thread replay duplicated", again)
	}
	third := threadSend(t, f, f.owner, room.ID, rooms.TextRequest{Content: "Third", ThreadID: &root.ID, RepliedMessageID: &second.ID})
	for _, p := range []rooms.TextRequest{{Content: "Invalid", ThreadID: &quoted.ID}, {Content: "Invalid", ThreadID: &first.ID}, {Content: "Invalid", ThreadID: &root.ID, RepliedMessageID: &unrelated.ID}} {
		if _, e := f.chats.Send(f.contexts[f.other], f.other, room.ID, p); !errors.Is(e, rooms.ErrInvalid) {
			t.Fatal("invalid thread ancestry", e)
		}
	}
	if _, e := f.chats.Send(f.contexts[f.owner], f.owner, outside.ID, rooms.TextRequest{Content: "Cross room", ThreadID: &root.ID}); !errors.Is(e, rooms.ErrNotFound) {
		t.Fatal("cross-room root", e)
	}
	if _, e := f.chats.Thread(f.contexts[f.third], f.third, room.ID, root.ID, 20, 0); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("outsider thread", e)
	}
	page, e := f.chats.Thread(f.contexts[f.other], f.other, room.ID, root.ID, 2, 0)
	if e != nil || page.Total != 3 || !page.Root.IsThreadRoot || page.Root.ThreadRepliesCount != 3 || len(page.Replies) != 2 || page.Replies[0].Message.ID != third.ID || page.Replies[0].Depth != 2 || page.Replies[1].Depth != 1 {
		t.Fatal("thread page/depth", page, e)
	}
	tail, e := f.chats.Thread(f.contexts[f.owner], f.owner, room.ID, root.ID, 2, 2)
	if e != nil || len(tail.Replies) != 1 || tail.Replies[0].Message.ID != first.ID || tail.Replies[0].Depth != 0 {
		t.Fatal(tail, e)
	}
	list, e := f.read.ListMessages(f.base, messager.MessageListQuery{RoomID: room.ID, AccountID: f.owner, Take: 100})
	if e != nil {
		t.Fatal(e)
	}
	for _, m := range list.Messages {
		if m.ThreadID != nil {
			t.Fatal("thread leaked into main list", m)
		}
		if m.ID == root.ID && (!m.IsThreadRoot || m.ThreadRepliesCount != 3) {
			t.Fatal("main root projection", m)
		}
	}
	if edited, e := f.chats.Edit(f.contexts[f.other], f.other, room.ID, second.ID, rooms.TextRequest{Content: "Edited"}); e != nil || edited.ThreadID == nil || *edited.ThreadID != root.ID || edited.RepliedMessageID == nil || *edited.RepliedMessageID != first.ID {
		t.Fatal("edit changed ancestry", edited, e)
	}
	if _, e := f.chats.Edit(f.contexts[f.other], f.other, room.ID, second.ID, rooms.TextRequest{Content: "Invalid edit", ThreadID: &unrelated.ID}); !errors.Is(e, rooms.ErrConflict) {
		t.Fatal(e)
	}
	if e := f.chats.DeleteMessage(f.contexts[f.other], f.other, room.ID, second.ID); e != nil {
		t.Fatal(e)
	}
	page, e = f.chats.Thread(f.contexts[f.owner], f.owner, room.ID, root.ID, 20, 0)
	if e != nil || page.Total != 2 || len(page.Replies) != 2 || page.Replies[0].Depth != 2 {
		t.Fatal("tombstoned ancestor lost lineage", page, e)
	}
	if e := f.chats.DeleteMessage(f.contexts[f.owner], f.owner, room.ID, root.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.chats.Thread(f.contexts[f.owner], f.owner, room.ID, root.ID, 20, 0); !errors.Is(e, rooms.ErrNotFound) {
		t.Fatal("deleted thread root", e)
	}
	limitRoot := relationSend(t, f, f.owner, outside.ID, "Bounded depth root")
	leaf := threadSend(t, f, f.owner, outside.ID, rooms.TextRequest{Content: "Depth zero", ThreadID: &limitRoot.ID})
	for depth := 1; depth <= 32; depth++ {
		leaf = threadSend(t, f, f.owner, outside.ID, rooms.TextRequest{Content: "Nested reply", ThreadID: &limitRoot.ID, RepliedMessageID: &leaf.ID})
	}
	if _, e := f.chats.Send(f.contexts[f.owner], f.owner, outside.ID, rooms.TextRequest{Content: "Depth 33", ThreadID: &limitRoot.ID, RepliedMessageID: &leaf.ID}); !errors.Is(e, rooms.ErrInvalid) {
		t.Fatal("unbounded thread nesting", e)
	}
}

func TestPGChatLiveForwardVisibilityAndNestedThreads(t *testing.T) {
	f := newChatRelationsFixture(t)
	source := threadRoom(t, f, false, f.other)
	destination := threadRoom(t, f, false, f.other, f.third)
	nestedRoom := threadRoom(t, f, false, f.third)
	original := threadSend(t, f, f.owner, source.ID, rooms.TextRequest{Content: "Private source", Meta: json.RawMessage(`{"private_marker":"never copy","embeds":[]}`)})
	forwarded := threadSend(t, f, f.owner, destination.ID, rooms.TextRequest{ForwardedMessageID: &original.ID})
	if forwarded.ForwardedMessage == nil || forwarded.ForwardedMessage.Content == nil || *forwarded.ForwardedMessage.Content != "Private source" {
		t.Fatal("source member preview", forwarded)
	}
	masked := threadGet(t, f, f.third, destination.ID, forwarded.ID)
	if masked.ForwardedMessage != nil || masked.ForwardedMessageID != nil || !masked.ForwardUnavailable {
		t.Fatal("private source visible to target-only member", masked)
	}
	if visible := threadGet(t, f, f.other, destination.ID, forwarded.ID); visible.ForwardedMessage == nil {
		t.Fatal("allowed source hidden", visible)
	}
	root := relationSend(t, f, f.owner, nestedRoom.ID, "Nested root")
	reply := threadSend(t, f, f.owner, nestedRoom.ID, rooms.TextRequest{Content: "Copied commentary", ThreadID: &root.ID, ForwardedMessageID: &forwarded.ID})
	page, e := f.chats.Thread(f.contexts[f.third], f.third, nestedRoom.ID, root.ID, 20, 0)
	if e != nil || len(page.Replies) != 1 || page.Replies[0].Message.ID != reply.ID {
		t.Fatal(page, e)
	}
	preview := page.Replies[0].Message.ForwardedMessage
	if preview == nil || preview.ForwardedMessage != nil || !preview.ForwardUnavailable {
		t.Fatal("nested private reference was not masked", preview)
	}
	for _, read := range []func() ([]messager.MessageProjection, error){func() ([]messager.MessageProjection, error) {
		p, e := f.read.ListMessages(f.base, messager.MessageListQuery{RoomID: destination.ID, AccountID: f.third, Take: 100})
		return p.Messages, e
	}, func() ([]messager.MessageProjection, error) {
		p, e := f.read.Sync(f.base, messager.SyncQuery{RoomID: destination.ID, AccountID: f.third})
		return p.Messages, e
	}} {
		items, e := read()
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, m := range items {
			if m.ID == forwarded.ID {
				found = true
				if m.ForwardedMessage != nil || !m.ForwardUnavailable {
					t.Fatal("list/sync visibility", m)
				}
			}
		}
		if !found {
			t.Fatal("reference missing")
		}
	}
	if _, e := f.chats.ChangeMember(f.contexts[f.other], f.other, source.ID, rooms.Leave, ""); e != nil {
		t.Fatal(e)
	}
	if m := threadGet(t, f, f.other, destination.ID, forwarded.ID); m.ForwardedMessage != nil || !m.ForwardUnavailable {
		t.Fatal("source leave did not revoke preview", m)
	}
	if e := f.chats.DeleteMessage(f.contexts[f.owner], f.owner, source.ID, original.ID); e != nil {
		t.Fatal(e)
	}
	if m := threadGet(t, f, f.owner, destination.ID, forwarded.ID); m.ForwardedMessage != nil || !m.ForwardUnavailable {
		t.Fatal("deleted source visible", m)
	}
}

func TestPGChatRedirectSnapshotProvenanceAndReplay(t *testing.T) {
	f := newChatRelationsFixture(t)
	source := threadRoom(t, f, false, f.other)
	destination := threadRoom(t, f, false, f.third)
	original := threadSend(t, f, f.other, source.ID, rooms.TextRequest{Content: "Explicit copied text", Meta: json.RawMessage(`{"private_marker":"never copy","embeds":[]}`)})
	second := relationSend(t, f, f.owner, source.ID, "Second copied text")
	copied, e := f.chats.Redirect(f.contexts[f.owner], f.owner, destination.ID, []string{original.ID}, "single-copy")
	if e != nil {
		t.Fatal(e)
	}
	meta := redirectMeta(t, copied)
	if meta["version"] != float64(1) || meta["source_content"] != "Explicit copied text" || meta["source_sender_id"] != f.other {
		t.Fatal(meta)
	}
	for _, forbidden := range []string{"private_marker", "never copy", "profile", "birthday", "encryption_meta", "source_meta", "attachments"} {
		if bytes.Contains(copied.Meta, []byte(forbidden)) {
			t.Fatal("copied private/unproved material", forbidden, string(copied.Meta))
		}
	}
	again, e := f.chats.Redirect(f.contexts[f.owner], f.owner, destination.ID, []string{original.ID}, "single-copy")
	if e != nil || again.ID != copied.ID {
		t.Fatal("redirect replay", again, e)
	}
	if _, e = f.chats.Redirect(f.contexts[f.owner], f.owner, destination.ID, []string{second.ID}, "single-copy"); !errors.Is(e, rooms.ErrConflict) {
		t.Fatal("redirect key collision", e)
	}
	many, e := f.chats.Redirect(f.contexts[f.owner], f.owner, destination.ID, []string{second.ID, original.ID}, "multiple-copy")
	if e != nil {
		t.Fatal(e)
	}
	history := redirectMeta(t, many)
	if history["version"] != float64(2) || history["kind"] != "history_segment" || len(history["messages"].([]any)) != 2 || history["messages"].([]any)[0].(map[string]any)["id"] != original.ID {
		t.Fatal(history)
	}
	if _, e = f.chats.Redirect(f.contexts[f.owner], f.owner, destination.ID, []string{original.ID, original.ID}, ""); !errors.Is(e, rooms.ErrInvalid) {
		t.Fatal("duplicate source ID", e)
	}
	tooMany := make([]string, 101)
	for i := range tooMany {
		tooMany[i] = authorityUUID(t)
	}
	if _, e = f.chats.Redirect(f.contexts[f.owner], f.owner, destination.ID, tooMany, ""); !errors.Is(e, rooms.ErrInvalid) {
		t.Fatal("redirect cap", e)
	}
	if before := threadGet(t, f, f.third, destination.ID, copied.ID); !reflect.DeepEqual(redirectMeta(t, before), meta) {
		t.Fatal("explicit copy missing")
	}
	if e = f.chats.DeleteMessage(f.contexts[f.other], f.other, source.ID, original.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.chats.ChangeMember(f.contexts[f.owner], f.owner, source.ID, rooms.Remove, f.other); e != nil {
		t.Fatal(e)
	}
	if after := threadGet(t, f, f.third, destination.ID, copied.ID); !reflect.DeepEqual(redirectMeta(t, after), meta) {
		t.Fatal("source deletion changed snapshot", after)
	}
	if _, e = f.chats.Redirect(f.contexts[f.owner], f.owner, destination.ID, []string{original.ID}, "single-copy"); !errors.Is(e, rooms.ErrNotFound) {
		t.Fatal("replay bypassed source existence", e)
	}
	tx, e := f.db.BeginTx(f.base, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(f.base, `UPDATE owned_chat_redirects SET snapshot='{}'::jsonb WHERE message_id=$1`, copied.ID); e == nil {
		t.Fatal("receipt mutation permitted")
	}
	if e = tx.Rollback(); e != nil {
		t.Fatal(e)
	}
	if got := threadGet(t, f, f.third, destination.ID, copied.ID); !reflect.DeepEqual(redirectMeta(t, got), meta) {
		t.Fatal("failed mutation changed receipt")
	}
}

func TestPGChatForwardRedirectWaitRevocationAndRollback(t *testing.T) {
	for _, kind := range []string{"reference-source", "reference-destination", "redirect-source", "redirect-destination", "redirect-session"} {
		t.Run(kind, func(t *testing.T) {
			f := newChatRelationsFixture(t)
			source := threadRoom(t, f, false, f.other)
			destination := threadRoom(t, f, false, f.other)
			original := relationSend(t, f, f.owner, source.ID, "Current source")
			ctx, cancel := context.WithTimeout(f.contexts[f.other], 12*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			table, id := "chat_rooms", source.ID
			if strings.HasSuffix(kind, "destination") {
				id = destination.ID
			}
			if strings.HasSuffix(kind, "session") {
				table, id = "owned_authority_accounts", f.other
			}
			blocker, pid := atomicBlocker(t, locks, table, id)
			if table == "chat_rooms" {
				if _, e := blocker.ExecContext(ctx, `UPDATE chat_members SET leave_at=clock_timestamp() WHERE chat_room_id=$1 AND account_id=$2`, id, f.other); e != nil {
					t.Fatal(e)
				}
			} else {
				proof, e := f.verifier.Authenticate(f.base, f.otherToken)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = blocker.ExecContext(ctx, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp(),epoch=epoch+1 WHERE id=$1`, proof.Session.ID); e != nil {
					t.Fatal(e)
				}
			}
			done := make(chan error, 1)
			go func() {
				var e error
				if strings.HasPrefix(kind, "reference") {
					_, e = f.chats.Send(ctx, f.other, destination.ID, rooms.TextRequest{ForwardedMessageID: &original.ID})
				} else {
					_, e = f.chats.Redirect(ctx, f.other, destination.ID, []string{original.ID}, "wait-copy")
				}
				done <- e
			}()
			waitAtomicBlocked(t, locks, pid, table)
			if e := blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			want := error(rooms.ErrForbidden)
			if table != "chat_rooms" {
				want = authority.ErrRevoked
			}
			if e := <-done; !errors.Is(e, want) {
				t.Fatal("stale admission after wait", kind, e)
			}
			noThreadEffects(t, f, destination.ID)
		})
	}
}

func TestPGChatRedirectConcurrencyAndMembershipIncarnation(t *testing.T) {
	f := newChatRelationsFixture(t)
	a := threadRoom(t, f, false, f.other)
	b := threadRoom(t, f, false, f.other)
	ma := relationSend(t, f, f.owner, a.ID, "From A")
	mb := relationSend(t, f, f.other, b.ID, "From B")
	type result struct {
		message messager.MessageProjection
		err     error
	}
	done := make(chan result, 4)
	for _, task := range []struct{ actor, room, message, key string }{{f.owner, b.ID, ma.ID, "opposing-a"}, {f.other, a.ID, mb.ID, "opposing-b"}, {f.owner, b.ID, ma.ID, "same-key"}, {f.owner, b.ID, ma.ID, "same-key"}} {
		go func(actor, rid, mid, key string) {
			m, e := f.chats.Redirect(f.contexts[actor], actor, rid, []string{mid}, key)
			done <- result{m, e}
		}(task.actor, task.room, task.message, task.key)
	}
	for i := 0; i < 4; i++ {
		v := <-done
		if v.err != nil {
			t.Fatal("opposing redirect or replay", v.err)
		}
	}
	var n int
	if e := f.db.QueryRowContext(f.base, `SELECT count(*) FROM owned_chat_redirects WHERE actor_id=$1 AND destination_room_id=$2 AND request_key='same-key'`, f.owner, b.ID).Scan(&n); e != nil || n != 1 {
		t.Fatal("concurrent replay duplicated", n, e)
	}
	copied, e := f.chats.Redirect(f.contexts[f.other], f.other, b.ID, []string{ma.ID}, "incarnation")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.chats.ChangeMember(f.contexts[f.other], f.other, a.ID, rooms.Leave, ""); e != nil {
		t.Fatal(e)
	}
	threadJoin(t, f, a.ID, f.other)
	if _, e = f.chats.Redirect(f.contexts[f.other], f.other, b.ID, []string{ma.ID}, "incarnation"); !errors.Is(e, rooms.ErrConflict) {
		t.Fatal("source membership ABA reused receipt", e)
	}
	if got := threadGet(t, f, f.other, b.ID, copied.ID); got.ID != copied.ID {
		t.Fatal("copy remains readable", got)
	}
	fresh, e := f.chats.Redirect(f.contexts[f.other], f.other, b.ID, []string{ma.ID}, "destination-incarnation")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.chats.ChangeMember(f.contexts[f.other], f.other, b.ID, rooms.Leave, ""); e != nil {
		t.Fatal(e)
	}
	threadJoin(t, f, b.ID, f.other)
	if _, e = f.chats.Redirect(f.contexts[f.other], f.other, b.ID, []string{ma.ID}, "destination-incarnation"); !errors.Is(e, rooms.ErrConflict) {
		t.Fatal("destination membership ABA reused receipt", fresh, e)
	}
}

func TestPGChatThreadForwardMediaAndEncryptionBoundaries(t *testing.T) {
	f := newChatMediaFixture(t)
	source := chatMediaRoom(t, f, false)
	target := chatMediaRoom(t, f, false)
	chatMediaJoin(t, f, source.ID)
	chatMediaJoin(t, f, target.ID)
	file := chatMediaFile(t, f, "forward-private", "private original bytes")
	original, e := f.chat.Send(f.ctx, f.actor, source.ID, rooms.TextRequest{Content: "Attached original", AttachmentsID: mediaIDs(file.ID)})
	if e != nil {
		t.Fatal(e)
	}
	mediaRead(t, f.repo, f.otherCtx, f.other, file.ID, "private original bytes")
	if _, e = f.chat.Redirect(f.otherCtx, f.other, target.ID, []string{original.ID}, ""); !errors.Is(e, rooms.ErrUnsupported) {
		t.Fatal("unproved attachment redistribution", e)
	}
	if _, e = f.chat.Send(f.otherCtx, f.other, target.ID, rooms.TextRequest{ForwardedMessageID: &original.ID, AttachmentsID: mediaIDs(file.ID)}); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("reference transferred file ownership", e)
	}
	ref, e := f.chat.Send(f.otherCtx, f.other, target.ID, rooms.TextRequest{ForwardedMessageID: &original.ID})
	if e != nil || ref.ForwardedMessage == nil || bytes.Contains(ref.Attachments, []byte(file.ID)) {
		t.Fatal("text-only reference", ref, e)
	}
	embedded, e := f.chat.Send(f.ctx, f.actor, source.ID, rooms.TextRequest{Content: "Embedded source", Meta: json.RawMessage(`{"embeds":[{"url":"https://invalid.example/no-fetch"}]}`)})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.chat.Redirect(f.ctx, f.actor, target.ID, []string{embedded.ID}, ""); !errors.Is(e, rooms.ErrUnsupported) {
		t.Fatal("embedded URL accepted as redirect", e)
	}
	query(t, f.db, `UPDATE chat_messages SET encryption_meta='{"ciphertext":"synthetic-opaque"}'::jsonb WHERE id=$1`, embedded.ID)
	if _, e = f.chat.Send(f.ctx, f.actor, target.ID, rooms.TextRequest{ForwardedMessageID: &embedded.ID}); !errors.Is(e, rooms.ErrUnsupported) {
		t.Fatal("opaque envelope downgraded", e)
	}
	if _, e = f.chat.Redirect(f.ctx, f.actor, target.ID, []string{embedded.ID}, ""); !errors.Is(e, rooms.ErrUnsupported) {
		t.Fatal("opaque snapshot", e)
	}
	query(t, f.db, `UPDATE chat_rooms SET encryption_mode=1 WHERE id=$1`, source.ID)
	if got := chatMediaMessage(t, f, f.actor, target.ID, ref.ID); got.ForwardedMessage != nil || !got.ForwardUnavailable {
		t.Fatal("encrypted source preview", got)
	}
	if _, e = f.chat.Thread(f.ctx, f.actor, source.ID, original.ID, 20, 0); !errors.Is(e, rooms.ErrUnsupported) {
		t.Fatal("encrypted thread plaintext read", e)
	}
}

func TestPGBackendChatThreadsForwardAndRedirectComposition(t *testing.T) {
	f := newChatMediaFixture(t)
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, MediaStore: f.store, MaxMediaBytes: 65536})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	source := backendDecode[rooms.Room](t, backendCall(t, app, "POST", "/messager/chat", f.token, `{"name":"Private source"}`, 200))
	target := backendDecode[rooms.Room](t, backendCall(t, app, "POST", "/messager/chat", f.token, `{"name":"Public target","is_public":true}`, 200))
	original := backendDecode[messager.MessageProjection](t, backendCall(t, app, "POST", "/messager/chat/"+source.ID+"/messages", f.token, `{"content":"Secret source"}`, 200))
	base := "/messager/chat/" + target.ID + "/messages"
	root := backendDecode[messager.MessageProjection](t, backendCall(t, app, "POST", base, f.token, `{"content":"Thread root","is_thread_root":true}`, 200))
	reply := backendDecode[messager.MessageProjection](t, backendCall(t, app, "POST", base, f.token, fmt.Sprintf(`{"thread_id":%q,"replied_message_id":%q,"forwarded_message_id":%q,"nonce":"thread-http"}`, root.ID, root.ID, original.ID), 200))
	page := backendDecode[rooms.ThreadPage](t, backendCall(t, app, "GET", base+"/"+root.ID+"/thread", "", "", 200))
	if len(page.Replies) != 1 || page.Replies[0].Message.ID != reply.ID || !page.Replies[0].Message.ForwardUnavailable {
		t.Fatal("anonymous nested hydration", page)
	}
	backendCall(t, app, "GET", base+"/"+root.ID+"/thread", "invalid", "", 401)
	edited := backendDecode[messager.MessageProjection](t, backendCall(t, app, "PATCH", base+"/"+reply.ID, f.token, `{"content":"Edited commentary","thread_id":null,"forwarded_message_id":null,"replied_message_id":null}`, 200))
	if edited.ThreadID == nil || *edited.ThreadID != root.ID || edited.ForwardedMessageID == nil || *edited.ForwardedMessageID != original.ID {
		t.Fatal("caller nulls erased immutable relations", edited)
	}
	request := httptest.NewRequest("POST", base+"/redirect", strings.NewReader(fmt.Sprintf(`{"message_ids":[%q]}`, original.ID)))
	request.Header.Set("Authorization", "Bearer "+f.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "redirect-http")
	w := httptest.NewRecorder()
	app.ServeHTTP(w, request)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	copy := backendDecode[messager.MessageProjection](t, w)
	public := backendDecode[messager.MessageProjection](t, backendCall(t, app, "GET", base+"/"+copy.ID, "", "", 200))
	if redirectMeta(t, public)["source_content"] != "Secret source" {
		t.Fatal("explicit-copy audience", public)
	}
	limited, e := f.issuer.IssueSession(f.ctx, f.actor, authority.SessionOptions{Label: "Synthetic read scope", Scopes: []string{"chat.view"}})
	if e != nil {
		t.Fatal(e)
	}
	backendCall(t, app, "POST", base+"/redirect", limited.Token, fmt.Sprintf(`{"message_ids":[%q]}`, original.ID), 403)
	backendCall(t, app, "POST", base, f.token, `{"content":"forged","meta":{"redirect":{"version":1}}}`, 400)
}

func TestPGChatThreadsForwardRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	type record struct {
		Source, Destination, Root, Reply, Reference, Snapshot string
		Page                                                  map[string]any
		Copy                                                  map[string]any
	}
	var saved record
	canonical := func(v any) map[string]any {
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		var m map[string]any
		if e = json.Unmarshal(b, &m); e != nil {
			t.Fatal(e)
		}
		return m
	}
	if phase == "before" {
		f := newChatRelationsFixture(t)
		source := threadRoom(t, f, false)
		target := threadRoom(t, f, true)
		original := relationSend(t, f, f.owner, source.ID, "Saved explicit source")
		root := relationSend(t, f, f.owner, target.ID, "Saved thread root")
		reply := threadSend(t, f, f.owner, target.ID, rooms.TextRequest{Content: "Saved reply", ThreadID: &root.ID, ForwardedMessageID: &original.ID})
		ref := threadSend(t, f, f.owner, target.ID, rooms.TextRequest{ForwardedMessageID: &original.ID})
		copy, e := f.chats.Redirect(f.contexts[f.owner], f.owner, target.ID, []string{original.ID}, "restart-copy")
		if e != nil {
			t.Fatal(e)
		}
		if e = f.chats.DeleteMessage(f.contexts[f.owner], f.owner, source.ID, original.ID); e != nil {
			t.Fatal(e)
		}
		page, e := f.chats.Thread(f.base, "", target.ID, root.ID, 20, 0)
		if e != nil {
			t.Fatal(e)
		}
		saved = record{Source: source.ID, Destination: target.ID, Root: root.ID, Reply: reply.ID, Reference: ref.ID, Snapshot: copy.ID, Page: canonical(page), Copy: canonical(threadGet(t, f, "", target.ID, copy.ID))}
		saveRestartFixture(t, "chat-thread-forward.json", saved)
		return
	}
	loadRestartFixture(t, "chat-thread-forward.json", &saved)
	// Reconstruct adapters and read saved rows. No state is seeded after restart.
	chat, e := chatpg.NewForCompatibilityTests(db, chatPolicy)
	if e != nil {
		t.Fatal(e)
	}
	chat.WithSocial(socialpg.New(db))
	reader, e := messagerpg.NewWithIndependentPolicy(db, messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30})
	if e != nil {
		t.Fatal(e)
	}
	reader.WithRoomAccess(chat)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	page, e := chat.Thread(ctx, "", saved.Destination, saved.Root, 20, 0)
	if e != nil || !reflect.DeepEqual(canonical(page), saved.Page) {
		t.Fatal("thread restart changed", page, e)
	}
	copy, e := reader.GetMessage(ctx, messager.MessageQuery{RoomID: saved.Destination, MessageID: saved.Snapshot})
	if e != nil || !reflect.DeepEqual(canonical(copy), saved.Copy) {
		t.Fatal("snapshot restart changed", copy, e)
	}
	ref, e := reader.GetMessage(ctx, messager.MessageQuery{RoomID: saved.Destination, MessageID: saved.Reference})
	if e != nil || ref.ForwardedMessage != nil || !ref.ForwardUnavailable {
		t.Fatal("restart revived deleted source", ref, e)
	}
}

func TestPGChatForwardFileWaitCurrentSourcePolicy(t *testing.T) {
	f := newChatMediaFixture(t)
	source, e := f.chat.CreateDirect(f.ctx, f.actor, rooms.DirectRequest{RelatedUserID: f.other})
	if e != nil {
		t.Fatal(e)
	}
	original, e := f.chat.Send(f.ctx, f.actor, source.ID, rooms.TextRequest{Content: "Direct source before scheduled restriction"})
	if e != nil {
		t.Fatal(e)
	}
	target := chatMediaRoom(t, f, false)
	file := chatMediaFile(t, f, "forward-wait", "synthetic owned attachment")
	ctx, cancel := context.WithTimeout(f.ctx, 12*time.Second)
	defer cancel()
	locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
	blocker, pid := atomicBlocker(t, locks, "owned_media_files", file.ID)
	var deadline time.Time
	if e = f.db.QueryRowContext(ctx, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at)VALUES($1,'disable_account',clock_timestamp()+interval '2 seconds') RETURNING starts_at`, f.other).Scan(&deadline); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.chat.Send(ctx, f.actor, target.ID, rooms.TextRequest{ForwardedMessageID: &original.ID, AttachmentsID: mediaIDs(file.ID)})
		done <- err
	}()
	waitAtomicBlocked(t, locks, pid, "owned_media_files")
	// The restriction was already scheduled before admission. No authority writer
	// bypasses the held account locks; the database clock advances during the wait.
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var active bool
		if e = f.db.QueryRowContext(ctx, `SELECT clock_timestamp()>=$1`, deadline).Scan(&active); e != nil {
			t.Fatal(e)
		}
		if active {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("file wait used expired source authority", e)
	}
	var messages, links int
	if e = f.db.QueryRowContext(ctx, `SELECT count(*) FROM chat_messages WHERE chat_room_id=$1`, target.ID).Scan(&messages); e != nil || messages != 0 {
		t.Fatal("failed forward committed text", messages, e)
	}
	if e = f.db.QueryRowContext(ctx, `SELECT count(*) FROM owned_chat_media link JOIN chat_messages message ON message.id=link.message_id WHERE message.chat_room_id=$1`, target.ID).Scan(&links); e != nil || links != 0 {
		t.Fatal("failed forward committed file grant", links, e)
	}
}
