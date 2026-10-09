// SPDX-License-Identifier: AGPL-3.0-only
package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"solsynth.local/go-migration/ports/notifications"
	"testing"
	"time"
)

type guardedEventStore struct {
	checks   int
	checking bool
	allow    bool
	cancel   context.CancelFunc
}

func (s *guardedEventStore) Replay(_ context.Context, _, _ string, after int64, _ int) (notifications.Page, error) {
	return notifications.Page{Cursor: after, HighWater: after}, nil
}
func (s *guardedEventStore) RevalidateEvent(_ context.Context, _, _ string, e notifications.Event) (notifications.Event, bool, error) {
	s.checking = true
	defer func() { s.checking = false }()
	s.checks++
	if s.checks == 2 {
		s.cancel()
	}
	e.Data = json.RawMessage(`{"current":true}`)
	return e, s.allow, nil
}
func TestFriendPacketsRecheckAfterQueueBeforeSlowWrite(t *testing.T) {
	s := &guardedEventStore{allow: true}
	h, e := New(s, &identity{}, Config{App: notifications.DefaultApp, PollInterval: time.Second, WriteTimeout: 10 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	first := notifications.Page{Events: []notifications.Event{{Sequence: 1, Type: "account.status.updated", Data: json.RawMessage(`{"stale":true}`)}, {Sequence: 2, Type: "account.presence.activities.updated", Data: json.RawMessage(`{"stale":true}`)}}, Cursor: 2, HighWater: 2}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.cancel = cancel
	writes := 0
	e = h.pump(ctx, httptest.NewRequest("GET", "/ws", nil), testActor, 0, first, sinkFunc(func(_ context.Context, p packet) error {
		if s.checking || s.checks != 1 || string(p.Data) != `{"current":true}` {
			t.Fatal("queue payload or observation lock reached transport", s, p)
		}
		writes++
		s.allow = false
		return nil
	}), nil)
	if !errors.Is(e, context.Canceled) || writes != 1 || s.checks != 2 {
		t.Fatal(e, writes, s.checks)
	}
}
