// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
)

func TestPGChatForwardSourceDiscoveryRequiresBoundProof(t *testing.T) {
	f := newChatRelationsFixture(t)
	source := threadRoom(t, f, false)
	target := threadRoom(t, f, false)
	original := relationSend(t, f, f.owner, source.ID, "Synthetic private source")
	for _, mid := range []string{original.ID, authorityUUID(t)} {
		if _, e := f.chats.Send(context.Background(), f.owner, target.ID, rooms.TextRequest{ForwardedMessageID: &mid}); !errors.Is(e, authority.ErrInvalid) {
			t.Fatal("source existence affected unbound reference denial", e)
		}
		if _, e := f.chats.Redirect(context.Background(), f.owner, target.ID, []string{mid}, "unbound-source"); !errors.Is(e, authority.ErrInvalid) {
			t.Fatal("source existence affected unbound redirect denial", e)
		}
	}
	noThreadEffects(t, f, target.ID)
}

func TestPGChatRedirectPrecisionAndCanonicalReceipt(t *testing.T) {
	f := newChatRelationsFixture(t)
	source := threadRoom(t, f, false)
	target := threadRoom(t, f, false, f.third)
	ids := []string{}
	for _, text := range []string{"First authored text", "Second authored text", "Third authored text"} {
		ids = append(ids, relationSend(t, f, f.owner, source.ID, text).ID)
	}
	sort.Strings(ids)
	instant := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	// Distinct microseconds share the authored millisecond timestamp. UUID order
	// intentionally disagrees with chronology; equal instants require an ID tie.
	for i, offset := range []time.Duration{900 * time.Microsecond, 100 * time.Microsecond, 100 * time.Microsecond} {
		query(t, f.db, `UPDATE chat_messages SET created_at=$2 WHERE id=$1`, ids[i], instant.Add(offset))
	}
	copy, e := f.chats.Redirect(f.contexts[f.owner], f.owner, target.ID, []string{ids[2], ids[0], ids[1]}, "precision-copy")
	if e != nil {
		t.Fatal(e)
	}
	meta := redirectMeta(t, copy)
	entries := meta["messages"].([]any)
	want := []string{ids[1], ids[2], ids[0]}
	for i, entry := range entries {
		v := entry.(map[string]any)
		if v["id"] != want[i] || v["created_at"] != float64(instant.UnixMilli()) {
			t.Fatal("redirect order lost database timestamp precision or stable ID tie", meta)
		}
	}
	if _, e = f.chats.Edit(f.contexts[f.owner], f.owner, source.ID, ids[1], rooms.TextRequest{Content: "Later source edit"}); e != nil {
		t.Fatal(e)
	}
	replayed, e := f.chats.Redirect(f.contexts[f.owner], f.owner, target.ID, ids, "precision-copy")
	if e != nil || replayed.ID != copy.ID || !reflect.DeepEqual(meta, redirectMeta(t, replayed)) {
		t.Fatal("replay rewrote original snapshot", replayed, e)
	}
	// The public message cache cannot impersonate or replace an authored receipt.
	query(t, f.db, `UPDATE chat_messages SET meta='{"redirect":{"version":1,"source_content":"forged cached copy"}}'::jsonb WHERE id=$1`, copy.ID)
	if got := threadGet(t, f, f.third, target.ID, copy.ID); !reflect.DeepEqual(meta, redirectMeta(t, got)) {
		t.Fatal("cached metadata overrode canonical receipt", got)
	}
	forged := relationSend(t, f, f.owner, target.ID, "Untrusted historical metadata")
	query(t, f.db, `UPDATE chat_messages SET meta='{"redirect":{"version":1,"source_content":"forged without receipt"},"ordinary":"retained"}'::jsonb WHERE id=$1`, forged.ID)
	got := threadGet(t, f, f.third, target.ID, forged.ID)
	var ordinary map[string]json.RawMessage
	if e = json.Unmarshal(got.Meta, &ordinary); e != nil || len(ordinary["redirect"]) != 0 || bytes.Contains(got.Meta, []byte("forged without receipt")) || string(ordinary["ordinary"]) != `"retained"` {
		t.Fatal("unreceipted metadata forged a copy or lost unrelated metadata", got, e)
	}
}

func TestPGChatThreadCountsMatchVisibleReplyBoundary(t *testing.T) {
	f := newChatRelationsFixture(t)
	room := threadRoom(t, f, false, f.other)
	outside := threadRoom(t, f, false)
	root := relationSend(t, f, f.owner, room.ID, "Visible root")
	visible := threadSend(t, f, f.owner, room.ID, rooms.TextRequest{Content: "Visible reply", ThreadID: &root.ID})
	hidden := threadSend(t, f, f.other, room.ID, rooms.TextRequest{Content: "Deleted sender reply", ThreadID: &root.ID})
	encrypted := threadSend(t, f, f.owner, room.ID, rooms.TextRequest{Content: "Opaque legacy reply", ThreadID: &root.ID})
	event := threadSend(t, f, f.owner, room.ID, rooms.TextRequest{Content: "Non-text legacy row", ThreadID: &root.ID})
	foreign := relationSend(t, f, f.owner, outside.ID, "Other room")
	query(t, f.db, `UPDATE chat_members SET deleted_at=clock_timestamp() WHERE id=$1`, hidden.SenderID)
	query(t, f.db, `UPDATE chat_messages SET encryption_meta='{"ciphertext":"synthetic opaque"}'::jsonb WHERE id=$1`, encrypted.ID)
	query(t, f.db, `UPDATE chat_messages SET type='messages.update' WHERE id=$1`, event.ID)
	query(t, f.db, `UPDATE chat_messages SET thread_id=$2 WHERE id=$1`, foreign.ID, root.ID)
	page, e := f.chats.Thread(f.contexts[f.owner], f.owner, room.ID, root.ID, 100, 0)
	if e != nil || page.Total != 1 || len(page.Replies) != 1 || page.Replies[0].Message.ID != visible.ID {
		t.Fatal("thread visibility fixture", page, e)
	}
	check := func(m messager.MessageProjection) {
		t.Helper()
		if m.ID != root.ID || !m.IsThreadRoot || m.ThreadRepliesCount != 1 {
			t.Fatal("root count differs from readable thread replies", m)
		}
	}
	check(threadGet(t, f, f.owner, room.ID, root.ID))
	for _, read := range []func() ([]messager.MessageProjection, error){func() ([]messager.MessageProjection, error) {
		p, e := f.read.ListMessages(f.base, messager.MessageListQuery{RoomID: room.ID, AccountID: f.owner, Take: 100})
		return p.Messages, e
	}, func() ([]messager.MessageProjection, error) {
		p, e := f.read.Sync(f.base, messager.SyncQuery{RoomID: room.ID, AccountID: f.owner})
		return p.Messages, e
	}} {
		messages, e := read()
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, m := range messages {
			if m.ID == root.ID {
				found = true
				check(m)
			}
		}
		if !found {
			t.Fatal("root missing from ordinary reader")
		}
	}
	if e = f.chats.DeleteMessage(f.contexts[f.owner], f.owner, room.ID, visible.ID); e != nil {
		t.Fatal(e)
	}
	if empty := threadGet(t, f, f.owner, room.ID, root.ID); empty.IsThreadRoot || empty.ThreadRepliesCount != 0 {
		t.Fatal("hidden replies kept empty root marked as a thread", empty)
	}
}
