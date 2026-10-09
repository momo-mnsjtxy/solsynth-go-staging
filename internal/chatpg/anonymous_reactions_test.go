// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
	"testing"
)

func TestAnonymousMessageHydrationClearsPersonalReactions(t *testing.T) {
	r := &Repository{}
	v := messager.MessageProjection{ReactionsCount: json.RawMessage(`{"thumb_up":3}`), ReactionsMade: map[string]bool{"private-prior-state": true}}
	// No association/reference/MLS work is required. An anonymous actor must
	// never reach the UUID-bound personal reaction query (nil transaction).
	if e := r.loadMessage(context.Background(), nil, "", &v); e != nil {
		t.Fatal(e)
	}
	if string(v.ReactionsCount) != `{"thumb_up":3}` {
		t.Fatal("aggregate reactions changed")
	}
	if v.ReactionsMade == nil || len(v.ReactionsMade) != 0 {
		t.Fatal("anonymous hydration retained personal reactions")
	}
}

func TestThreadRejectsMalformedNonemptyIdentityBeforeDatabase(t *testing.T) {
	r := &Repository{}
	if _, e := r.Thread(context.Background(), "malformed", "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", 20, 0); !errors.Is(e, rooms.ErrInvalid) {
		t.Fatal(e)
	}
}
