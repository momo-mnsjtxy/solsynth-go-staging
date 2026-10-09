// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"encoding/json"
	"solsynth.local/go-migration/ports/authority/signin"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/passport/accounts"
	"strings"
	"testing"
	"time"
)

func TestDirectRecoveryDoesNotCopyNestedOrCachedFields(t *testing.T) {
	now := time.Now()
	key := "synthetic-hidden-key"
	peer := &accounts.Account{ID: "peer", Name: "synthetic-hidden-name", Profile: &accounts.Profile{Bio: "synthetic-hidden-bio", Location: "synthetic-hidden-location", Picture: map[string]any{"content": "synthetic-hidden-message"}}, Contacts: []signin.Contact{{Content: "synthetic-hidden-contact"}}}
	own := rooms.Member{ID: "self-member", AccountID: "self", ChatRoomID: "room", JoinedAt: &now, ConfirmedAt: &now, Account: peer, Room: &rooms.Room{Name: "synthetic-hidden-cached-room"}}
	source := rooms.Room{ID: "room", AccountID: "unexpected-owner", Name: "synthetic-hidden-room", Description: "synthetic-hidden-description", MLSGroupID: &key, MLSEpoch: 12, MLSStateVersion: 7, Members: []rooms.Member{own, {AccountID: "peer", Account: peer}}, DirectMembers: []rooms.Member{{AccountID: "unexpected-third", Account: peer}}}
	source.Picture = map[string]any{"content": "synthetic-hidden-picture"}
	source.Background = map[string]any{"key": "synthetic-hidden-background"}
	got := directRecoveryRoom(source, own, "peer")
	raw, e := json.Marshal(got)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(raw), "synthetic-hidden") || strings.Contains(string(raw), "unexpected-third") || strings.Contains(string(raw), "unexpected-owner") {
		t.Fatal("nested or cached recovery disclosure", string(raw))
	}
	if !got.RecoveryOnly || got.AccountID != "" || got.ID != "room" || len(got.Members) != 1 || got.Members[0].AccountID != "self" || got.Members[0].ConfirmedAt == nil || len(got.DirectMembers) != 1 || got.DirectMembers[0].AccountID != "peer" || got.MLSGroupID != nil {
		t.Fatal(got)
	}
}
