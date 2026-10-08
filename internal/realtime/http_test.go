// SPDX-License-Identifier: AGPL-3.0-only
package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/notifications"
)

const testActor = "10000000-0000-0000-0000-000000000001"

type identity struct{ revoked atomic.Bool }

func (i *identity) Account(*http.Request) (string, error) {
	if i.revoked.Load() {
		return "", authority.ErrRevoked
	}
	return testActor, nil
}

type eventStore struct {
	calls atomic.Int32
	fn    func(int64) (notifications.Page, error)
}

func (s *eventStore) Replay(_ context.Context, a, app string, after int64, take int) (notifications.Page, error) {
	s.calls.Add(1)
	if a != testActor || app != notifications.DefaultApp || take < 1 || take > 32 {
		return notifications.Page{}, notifications.ErrInvalid
	}
	return s.fn(after)
}

type sinkFunc func(context.Context, packet) error

func (f sinkFunc) Send(c context.Context, p packet) error { return f(c, p) }
func fixture(t *testing.T, s *eventStore, i *identity) *Handler {
	t.Helper()
	h, e := New(s, i, Config{App: notifications.DefaultApp, PollInterval: 10 * time.Millisecond, WriteTimeout: 20 * time.Millisecond, MaxConnections: 2, MaxPerAccount: 1})
	if e != nil {
		t.Fatal(e)
	}
	return h
}
func TestReplayAndHandshakeRejectBeforeUpgrade(t *testing.T) {
	i := &identity{}
	s := &eventStore{fn: func(after int64) (notifications.Page, error) {
		if after > 3 {
			return notifications.Page{}, notifications.ErrCursor
		}
		return notifications.Page{Events: []notifications.Event{}, Cursor: 3, HighWater: 3}, nil
	}}
	h := fixture(t, s, i)
	for _, tc := range []struct {
		path   string
		status int
	}{{"/api/realtime/replay?after=0", 200}, {"/ws?after=9", 409}, {"/ws?namespace=another", 400}, {"/ws?tk=secret", 400}, {"/ws?after=1&after=2", 400}, {"/ws?after=-1", 400}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
		if w.Code != tc.status {
			t.Fatal(tc.path, w.Code, w.Body.String())
		}
	}
	i.revoked.Store(true)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/ws", nil))
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	if !h.acquire(testActor) || h.acquire(testActor) || !h.acquire("other") || h.acquire("third") {
		t.Fatal("connection bound")
	}
	h.release(testActor)
	if !h.acquire("third") {
		t.Fatal("release")
	}
}
func TestPumpReplayOrderAndRevocation(t *testing.T) {
	i := &identity{}
	s := &eventStore{fn: func(after int64) (notifications.Page, error) {
		if i.revoked.Load() {
			return notifications.Page{}, authority.ErrRevoked
		}
		return notifications.Page{Events: []notifications.Event{{Sequence: after + 1, Type: "notifications.new", Data: json.RawMessage(`{"id":"synthetic"}`)}}, Cursor: after + 1, HighWater: 3}, nil
	}}
	h := fixture(t, s, i)
	got := []int64{}
	first, _ := s.fn(0)
	e := h.pump(context.Background(), httptest.NewRequest("GET", "/ws", nil), testActor, 0, first, sinkFunc(func(ctx context.Context, p packet) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("no write deadline")
		}
		got = append(got, p.Sequence)
		if len(got) == 2 {
			i.revoked.Store(true)
		}
		return nil
	}), nil)
	if !errors.Is(e, authority.ErrRevoked) || len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatal(got, e)
	}
}
func TestPumpBackpressureAndCancellation(t *testing.T) {
	i := &identity{}
	s := &eventStore{fn: func(after int64) (notifications.Page, error) {
		return notifications.Page{Cursor: after, HighWater: after}, nil
	}}
	h := fixture(t, s, i)
	first := notifications.Page{Events: []notifications.Event{{Sequence: 1, Type: "notifications.new", Data: json.RawMessage(`{}`)}}, Cursor: 1, HighWater: 100}
	e := h.pump(context.Background(), httptest.NewRequest("GET", "/ws", nil), testActor, 0, first, sinkFunc(func(ctx context.Context, p packet) error { <-ctx.Done(); return ctx.Err() }), nil)
	if !errors.Is(e, context.DeadlineExceeded) || s.calls.Load() != 0 {
		t.Fatal("slow writer fetched ahead", e, s.calls.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e = h.pump(ctx, httptest.NewRequest("GET", "/ws", nil), testActor, 0, notifications.Page{}, sinkFunc(func(context.Context, packet) error { t.Fatal("write on canceled context"); return nil }), nil)
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
