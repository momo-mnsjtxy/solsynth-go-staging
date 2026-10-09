// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/passport/accounts"
)

func TestChatLiveWireKeepsActualPublicFieldsWithoutInventedSenderData(t *testing.T) {
	at := time.Date(2026, 10, 9, 7, 0, 0, 0, time.UTC)
	m := LiveMessage{MessageProjection: messager.MessageProjection{ID: "message", ChatRoomID: "room", SenderID: "member", Type: "text", CreatedAt: at, UpdatedAt: at, Meta: json.RawMessage(`{"large":9007199254740993}`), Attachments: json.RawMessage(`[]`), MembersMentioned: json.RawMessage(`[]`)}, Sender: rooms.Member{ID: "member", AccountID: "account", ChatRoomID: "room", Notify: 42, LastReadAt: &at, TimeoutUntil: &at, TimeoutCause: &rooms.TimeoutCause{Reason: new(string)}, Account: &accounts.Account{ID: "account", Name: "actual-name", Nick: "actual-nick", CreatedAt: at, UpdatedAt: &at}}}
	raw, e := json.Marshal(m)
	if e != nil {
		t.Fatal(e)
	}
	var got map[string]json.RawMessage
	if json.Unmarshal(raw, &got) != nil {
		t.Fatal(string(raw))
	}
	var sender, account map[string]json.RawMessage
	if json.Unmarshal(got["sender"], &sender) != nil || json.Unmarshal(sender["account"], &account) != nil {
		t.Fatal(string(raw))
	}
	for _, key := range []string{"notify", "last_read_at", "timeout_until", "timeout_cause", "chat_room", "created_at", "updated_at"} {
		if _, ok := sender[key]; ok {
			t.Fatal("invented or private member field", key, string(raw))
		}
	}
	for _, key := range []string{"is_superuser", "profile", "experience", "level", "perk_subscription", "birthday"} {
		if _, ok := account[key]; ok {
			t.Fatal("invented account privilege/profile/progression", key, string(raw))
		}
	}
	// These are the mandatory nested fields in the retained authored Swift
	// decoders. The excluded Flutter SDK decoder is not an oracle for this test.
	for _, key := range []string{"id", "name", "nick"} {
		var s string
		if json.Unmarshal(account[key], &s) != nil || s == "" {
			t.Fatal("missing native required account field", key)
		}
	}
	if string(account["updated_at"]) != `"2026-10-09T07:00:00Z"` || string(got["meta"]) != `{"large":9007199254740993}` {
		t.Fatal("known timestamp or large JSON integer changed", string(raw))
	}
}
func TestChatLiveSourceProvenancePins(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, e := os.ReadFile(filepath.Join(root, "docs", "chat-live-provenance.json"))
	if e != nil {
		t.Fatal(e)
	}
	var manifest struct {
		Files []struct{ Path, SHA256 string }
	}
	if e = json.Unmarshal(raw, &manifest); e != nil || len(manifest.Files) < 4 {
		t.Fatal(e)
	}
	for _, v := range manifest.Files {
		b, e := os.ReadFile(filepath.Join(root, v.Path))
		if e != nil {
			t.Fatal(e)
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != v.SHA256 {
			t.Fatal("retained evidence changed", v.Path)
		}
	}
}
