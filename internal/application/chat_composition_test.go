// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"encoding/json"
	"testing"

	"solsynth.local/go-migration/ports/messager/rooms"
)

func chatCompositionID(n string) string { return "10000000-0000-4000-8000-" + n }

func TestChatCompositionExplicitEmptyEmbedsWinsOverTypedRemoval(t *testing.T) {
	empty := []map[string]any{}
	p := rooms.TextRequest{Embeds: &empty, FundSet: true}
	prior := json.RawMessage(`{"embeds":[{"type":"fund","id":"10000000-0000-0000-0000-000000000001"}]}`)
	got, err := (chatMessageComposer{}).Compose(context.Background(), nil, "", p, prior)
	if err != nil {
		t.Fatal(err)
	}
	if got.Embeds == nil || len(*got.Embeds) != 0 {
		t.Fatal("explicit empty embeds list was replaced from prior metadata", got.Embeds)
	}
}

func TestChatCanonicalReferencesRejectRawProjectionFieldsAndDeduplicate(t *testing.T) {
	id := chatCompositionID("000000000001")
	embeds := []map[string]any{{"type": "meet", "id": id}, {"type": "meet", "id": id}}
	refs, err := chatCanonicalReferences(rooms.TextRequest{MeetID: &id, Embeds: &embeds})
	if err != nil || len(refs) != 1 || refs[0] != (chatCanonicalReference{kind: "meet", id: id}) {
		t.Fatal(refs, err)
	}
	embeds = []map[string]any{{"type": "calendar_event", "id": id, "title": "client copy"}}
	if _, err = chatCanonicalReferences(rooms.TextRequest{Embeds: &embeds}); err != rooms.ErrInvalid {
		t.Fatal("canonical raw projection fields accepted", err)
	}
}
