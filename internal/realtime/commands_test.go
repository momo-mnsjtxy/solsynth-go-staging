// SPDX-License-Identifier: AGPL-3.0-only
package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/notifications"
	"testing"
)

func TestCommandEnvelopeExactAuthoredNames(t *testing.T) {
	for _, raw := range []string{`{"type":"messages.placeholder.finalize","endpoint":"DysonNetwork.Messager","data":{"message_id":"synthetic"},"error_message":null}`, `{"type":"ping","data":null,"endpoint":null,"errorMessage":null}`} {
		if _, e := decodeCommand([]byte(raw)); e != nil {
			t.Fatal(raw, e)
		}
	}
	for _, raw := range []string{`{"type":"ping","type":"messages.placeholder.finalize"}`, `{"type":"ping","version":2}`, `{"type":"ping","account_id":"spoofed"}`, `{"type":"ping","errorMessage":"spoofed"}`, `{"type":"ping"} {}`, `null`} {
		if _, e := decodeCommand([]byte(raw)); e == nil {
			t.Fatal(raw)
		}
	}
}

func TestCommandPumpOriginalContextAndPostCommandRevocation(t *testing.T) {
	for _, revokeBefore := range []bool{false, true} {
		i := &identity{}
		store := &eventStore{fn: func(after int64) (notifications.Page, error) {
			return notifications.Page{Cursor: after, HighWater: after}, nil
		}}
		h := fixture(t, store, i)
		type key struct{}
		ctx := context.WithValue(context.Background(), key{}, "original-proof")
		calls := 0
		h.cfg.Commands = CommandHandlerFunc(func(c context.Context, actor string, p Command) (json.RawMessage, error) {
			calls++
			if c.Value(key{}) != "original-proof" || actor != testActor {
				t.Fatal("lost original proof context")
			}
			i.revoked.Store(true)
			return json.RawMessage(`{}`), nil
		})
		commands := make(chan Command, 1)
		commands <- Command{Type: "messages.placeholder.finalize", Endpoint: "DysonNetwork.Messager", Data: json.RawMessage(`{}`)}
		if revokeBefore {
			i.revoked.Store(true)
		}
		e := h.pumpCommands(ctx, httptest.NewRequest("GET", "/ws", nil).WithContext(ctx), testActor, 0, notifications.Page{}, sinkFunc(func(context.Context, packet) error { t.Fatal("revoked command emitted payload"); return nil }), nil, commands)
		if !errors.Is(e, authority.ErrRevoked) || calls != map[bool]int{true: 0, false: 1}[revokeBefore] {
			t.Fatal(calls, e)
		}
	}
}
