// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"solsynth.local/go-migration/ports/messager/rooms"
	"strings"
	"testing"
)

func TestRedirectSnapshotsKeepAuthoredTextEnvelopeAndMinimalAttribution(t *testing.T) {
	entries := []redirectEntry{{ID: "one", Type: "text", Content: "first", SenderID: "member", ChatRoomID: "source", CreatedAt: 123}, {ID: "two", Type: "text", Content: "second", SenderID: "member", ChatRoomID: "source", CreatedAt: 124}}
	actor := redirectActor{ID: "member", AccountID: "account", Account: redirectAccount{ID: "account", Name: "canonical", Nick: "Canonical"}}
	for _, n := range []int{1, 2} {
		snapshot := buildRedirectSnapshot("source", "target", entries[:n], map[string]redirectActor{"member": actor}, actor)
		b, e := json.Marshal(snapshot)
		if e != nil {
			t.Fatal(e)
		}
		for _, forbidden := range []string{"profile", "birthday", "attachments", "encryption_meta", "source_meta", "language", "realm_bio", "reactions_count", "client_message_id"} {
			if strings.Contains(string(b), `"`+forbidden+`"`) {
				t.Fatal("unexpected private field", forbidden, string(b))
			}
		}
		if n == 1 {
			if snapshot["version"] != 1 || snapshot["source_content"] != "first" || snapshot["source_sender_id"] != "account" {
				t.Fatal(snapshot)
			}
		} else {
			if snapshot["version"] != 2 || snapshot["kind"] != "history_segment" || snapshot["range"].(map[string]any)["message_count"] != 2 {
				t.Fatal(snapshot)
			}
		}
	}
}
func TestRedirectEmbedPresenceRejectsOnlyActualOrInvalidEmbeds(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{{"", false}, {"null", false}, {"[]", false}, {`[{"url":"https://invalid.example"}]`, true}, {`{}`, true}} {
		if got := hasRedirectEmbeds(map[string]json.RawMessage{"embeds": json.RawMessage(tc.raw)}); got != tc.want {
			t.Fatal(tc, got)
		}
	}
}

func TestRedirectBoundsBeforeAnyDatabaseAccess(t *testing.T) {
	var r *Repository
	actor, room := "c1000000-0000-4000-8000-000000000001", "c2000000-0000-4000-8000-000000000001"
	ids := []string{}
	for i := 1; i <= 100; i++ {
		ids = append(ids, fmt.Sprintf("c3000000-0000-4000-8000-%012d", i))
	}
	if _, e := r.Redirect(context.Background(), actor, room, ids, ""); !errors.Is(e, rooms.ErrUnavailable) {
		t.Fatal("100 distinct IDs should reach the database boundary", e)
	}
	for _, bad := range [][]string{nil, append(append([]string{}, ids...), "c4000000-0000-4000-8000-000000000001"), {ids[0], ids[0]}, {"invalid"}} {
		if _, e := r.Redirect(context.Background(), actor, room, bad, ""); !errors.Is(e, rooms.ErrInvalid) {
			t.Fatal("invalid batch reached database", e)
		}
	}
}
