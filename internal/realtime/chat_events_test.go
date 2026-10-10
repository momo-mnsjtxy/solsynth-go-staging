// SPDX-License-Identifier: AGPL-3.0-only
package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/notifications"
)

type testChatConnections struct {
	mu                    sync.Mutex
	opens, closes, renews int
	revoked               bool
	device                string
	subscriptions         []testSubscription
}

type testSubscription struct {
	actor, connection, room string
	subscribed              bool
}

func (c *testChatConnections) OpenConnection(context.Context, string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.opens++
	return "synthetic-lease", nil
}
func (c *testChatConnections) RenewConnection(context.Context, string, string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.renews++
	if c.revoked {
		return authority.ErrRevoked
	}
	return nil
}
func (c *testChatConnections) CurrentConnectionDevice(context.Context, string, string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.revoked {
		return "", authority.ErrRevoked
	}
	return c.device, nil
}
func (c *testChatConnections) CloseConnection(context.Context, string, string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closes++
	return nil
}
func (c *testChatConnections) SetSubscription(_ context.Context, actor, connection, room string, subscribed bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.subscriptions = append(c.subscriptions, testSubscription{actor, connection, room, subscribed})
	return nil
}
func (c *testChatConnections) revoke() { c.mu.Lock(); defer c.mu.Unlock(); c.revoked = true }

func TestChatLiveRecipientLeaseRecheckedAfterBackpressure(t *testing.T) {
	leases := &testChatConnections{device: "synthetic-device"}
	store := &eventStore{fn: func(after int64) (notifications.Page, error) {
		return notifications.Page{Cursor: after, HighWater: after}, nil
	}}
	h, e := New(store, &identity{}, Config{App: notifications.DefaultApp, Connections: leases, PollInterval: time.Second, WriteTimeout: 20 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	ctx := rooms.WithMLSWrite(context.Background(), rooms.MLSWriteContext{Device: leases.device})
	first := notifications.Page{Events: []notifications.Event{{Sequence: 1, Type: "messages.new", Data: json.RawMessage(`{"id":"one"}`)}, {Sequence: 2, Type: "messages.placeholder.update", Data: json.RawMessage(`{"id":"two"}`)}}, Cursor: 2, HighWater: 2}
	writes := 0
	e = h.pump(ctx, httptest.NewRequest("GET", "/ws", nil).WithContext(ctx), testActor, 0, first, sinkFunc(func(ctx context.Context, p packet) error { writes++; leases.revoke(); return nil }), nil)
	if !errors.Is(e, authority.ErrRevoked) || writes != 1 || leases.opens != 1 || leases.closes != 1 || store.calls.Load() != 0 {
		t.Fatal("lease revocation did not stop queued recipient", e, writes, leases, store.calls.Load())
	}
}
func TestChatLiveSlowRecipientDoesNotAdvanceReplayCursor(t *testing.T) {
	leases := &testChatConnections{}
	store := &eventStore{fn: func(after int64) (notifications.Page, error) {
		return notifications.Page{Cursor: after, HighWater: after}, nil
	}}
	h, e := New(store, &identity{}, Config{App: notifications.DefaultApp, Connections: leases, PollInterval: time.Second, WriteTimeout: 10 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	first := notifications.Page{Events: []notifications.Event{{Sequence: 7, Type: "messages.new", Data: json.RawMessage(`{"id":"same-source"}`)}}, Cursor: 7, HighWater: 20}
	e = h.pump(context.Background(), httptest.NewRequest("GET", "/ws", nil), testActor, 6, first, sinkFunc(func(ctx context.Context, p packet) error { <-ctx.Done(); return ctx.Err() }), nil)
	if !errors.Is(e, context.DeadlineExceeded) || store.calls.Load() != 0 || leases.closes != 1 {
		t.Fatal(e, store.calls.Load(), leases)
	}
	// A reconnect starts from the last successfully received cursor. There is no
	// global acknowledgement or exactly-once claim across the failed write.
	ctx, cancel := context.WithCancel(context.Background())
	writes := 0
	e = h.pump(ctx, httptest.NewRequest("GET", "/ws", nil), testActor, 6, first, sinkFunc(func(_ context.Context, p packet) error {
		if p.Sequence != 7 {
			t.Fatal(p)
		}
		writes++
		cancel()
		return nil
	}), nil)
	if !errors.Is(e, context.Canceled) || writes != 1 || leases.opens != 2 || leases.closes != 2 {
		t.Fatal(e, writes, leases)
	}
}
func TestChatDeviceSelectorRejectsAmbiguityBeforeReplay(t *testing.T) {
	for _, headers := range [][]string{{"one", "two"}, {"bad device"}} {
		r := httptest.NewRequest("GET", "/api/realtime/replay", nil)
		for _, v := range headers {
			r.Header.Add("X-Device-Id", v)
		}
		if _, e := bindDevice(r); !errors.Is(e, notifications.ErrInvalid) {
			t.Fatal(headers, e)
		}
	}
	r := httptest.NewRequest("GET", "/ws", nil)
	r.Header.Set("X-Device-Id", "selected-device")
	r, e := bindDevice(r)
	if e != nil || rooms.MLSWriteFrom(r.Context()).Device != "selected-device" {
		t.Fatal(e)
	}
}

func TestChatSubscriptionCommandsBindCurrentConnection(t *testing.T) {
	const roomID = "20000000-0000-0000-0000-000000000002"
	for _, tc := range []struct {
		kind       string
		subscribed bool
	}{{"messages.subscribe", true}, {"messages.unsubscribe", false}} {
		leases := &testChatConnections{}
		store := &eventStore{fn: func(after int64) (notifications.Page, error) {
			return notifications.Page{Cursor: after, HighWater: after}, nil
		}}
		h, e := New(store, &identity{}, Config{App: notifications.DefaultApp, Connections: leases, PollInterval: time.Second, WriteTimeout: 20 * time.Millisecond})
		if e != nil {
			t.Fatal(e)
		}
		ctx, cancel := context.WithCancel(context.Background())
		commands := make(chan Command, 1)
		commands <- Command{Type: tc.kind, Endpoint: "DysonNetwork.Messager", Data: json.RawMessage(`{"chat_room_id":"` + roomID + `"}`)}
		e = h.pumpCommands(ctx, httptest.NewRequest("GET", "/ws", nil), testActor, 0, notifications.Page{}, sinkFunc(func(_ context.Context, p packet) error {
			if p.Type != tc.kind || string(p.Data) != "null" {
				t.Fatal(p)
			}
			cancel()
			return nil
		}), nil, commands)
		if !errors.Is(e, context.Canceled) || len(leases.subscriptions) != 1 {
			t.Fatal(tc.kind, e, leases.subscriptions)
		}
		got := leases.subscriptions[0]
		if got.actor != testActor || got.connection != "synthetic-lease" || got.room != roomID || got.subscribed != tc.subscribed {
			t.Fatal(tc.kind, got)
		}
	}
}

func TestChatSubscriptionCommandsRejectAmbiguousPayload(t *testing.T) {
	h := &Handler{cfg: Config{Connections: &testChatConnections{}}}
	for _, command := range []Command{
		{Type: "messages.subscribe", Endpoint: "wrong", Data: json.RawMessage(`{"chat_room_id":"20000000-0000-0000-0000-000000000002"}`)},
		{Type: "messages.subscribe", Endpoint: "DysonNetwork.Messager", Data: json.RawMessage(`{"chat_room_id":"20000000-0000-0000-0000-000000000002","extra":true}`)},
		{Type: "messages.unsubscribe", Endpoint: "DysonNetwork.Messager", Data: json.RawMessage(`{"chat_room_id":"bad"}`)},
	} {
		if _, e := h.handleSubscription(context.Background(), testActor, "synthetic-lease", command); !errors.Is(e, notifications.ErrInvalid) {
			t.Fatal(command, e)
		}
	}
}

func TestChatLiveTwoConnectionsRehydrateTheirOwnQueuedPackets(t *testing.T) {
	// Each connection has its own proof/context and cursor. The same durable
	// reference can produce different current visibility for two recipients.
	for _, allow := range []bool{true, false} {
		ctx, cancel := context.WithCancel(context.Background())
		store := &chatRevalidationStore{allow: allow, cancel: cancel}
		h, e := New(store, &identity{}, Config{App: notifications.DefaultApp, Connections: &testChatConnections{}, PollInterval: 10 * time.Millisecond, WriteTimeout: 10 * time.Millisecond})
		if e != nil {
			t.Fatal(e)
		}
		first := notifications.Page{Events: []notifications.Event{{Sequence: 1, Type: "messages.new", Data: json.RawMessage(`{"stale":"private"}`)}}, Cursor: 1, HighWater: 1}
		writes := 0
		e = h.pump(ctx, httptest.NewRequest("GET", "/ws", nil), testActor, 0, first, sinkFunc(func(_ context.Context, p packet) error {
			if p.Type != "messages.new" || string(p.Data) != `{"current":true}` {
				t.Fatal("buffered payload bypassed revalidation", p)
			}
			writes++
			cancel()
			return nil
		}), nil)
		cancel()
		if !errors.Is(e, context.Canceled) || store.checks != 1 || writes != boolInt(allow) {
			t.Fatal(allow, e, store.checks, writes)
		}
	}
}

type chatRevalidationStore struct {
	allow  bool
	checks int
	cancel context.CancelFunc
}

func (s *chatRevalidationStore) Replay(context.Context, string, string, int64, int) (notifications.Page, error) {
	s.cancel()
	return notifications.Page{}, context.Canceled
}
func (s *chatRevalidationStore) RevalidateEvent(_ context.Context, _, _ string, event notifications.Event) (notifications.Event, bool, error) {
	s.checks++
	event.Data = json.RawMessage(`{"current":true}`)
	if !s.allow {
		s.cancel()
	}
	return event, s.allow, nil
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
