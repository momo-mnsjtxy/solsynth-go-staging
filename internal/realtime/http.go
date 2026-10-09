// SPDX-License-Identifier: AGPL-3.0-only
// Package realtime serves a bounded account stream using the authored Solian
// type/data envelope. Cursor/replay and SSE are explicitly owned extensions.
package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/notifications"
)

type Config struct {
	Connections                   rooms.ChatConnections
	Commands                      CommandHandler
	App                           string
	PollInterval, WriteTimeout    time.Duration
	MaxConnections, MaxPerAccount int
}
type Handler struct {
	store    notifications.EventStore
	identity notifications.Identity
	cfg      Config
	mu       sync.Mutex
	active   map[string]int
	total    int
}

func New(store notifications.EventStore, identity notifications.Identity, c Config) (*Handler, error) {
	if store == nil || identity == nil || !notifications.ValidApp(c.App) {
		return nil, notifications.ErrUnavailable
	}
	if c.PollInterval == 0 {
		c.PollInterval = time.Second
	}
	if c.WriteTimeout == 0 {
		c.WriteTimeout = 5 * time.Second
	}
	if c.MaxConnections == 0 {
		c.MaxConnections = 128
	}
	if c.MaxPerAccount == 0 {
		c.MaxPerAccount = 4
	}
	if c.PollInterval < 10*time.Millisecond || c.PollInterval > 5*time.Second || c.WriteTimeout < 10*time.Millisecond || c.WriteTimeout > 10*time.Second || c.MaxConnections < 1 || c.MaxConnections > 4096 || c.MaxPerAccount < 1 || c.MaxPerAccount > 32 {
		return nil, notifications.ErrInvalid
	}
	return &Handler{store: store, identity: identity, cfg: c, active: map[string]int{}}, nil
}
func (h *Handler) acquire(a string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.total >= h.cfg.MaxConnections || h.active[a] >= h.cfg.MaxPerAccount {
		return false
	}
	h.total++
	h.active[a]++
	return true
}
func (h *Handler) release(a string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.total--
	h.active[a]--
	if h.active[a] == 0 {
		delete(h.active, a)
	}
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		notifications.WriteJSON(w, 405, map[string]string{"code": "METHOD_NOT_ALLOWED"})
		return
	}
	if r.URL.Path != "/ws" && r.URL.Path != "/api/realtime/events" && r.URL.Path != "/api/realtime/replay" {
		http.NotFound(w, r)
		return
	}
	actor, e := notifications.RequestScope(r, h.identity, h.cfg.App)
	if e != nil {
		notifications.WriteError(w, e)
		return
	}
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		notifications.WriteError(w, notifications.ErrInvalid)
		return
	}
	namespace, e := notifications.One(q, "namespace")
	if e != nil || namespace != "" && namespace != h.cfg.App {
		notifications.WriteError(w, notifications.ErrInvalid)
		return
	}
	app, e := notifications.One(q, "app")
	if e != nil || app != "" && app != h.cfg.App {
		notifications.WriteError(w, notifications.ErrInvalid)
		return
	}
	afterRaw, e := notifications.One(q, "after")
	if e != nil {
		notifications.WriteError(w, e)
		return
	}
	if len(r.Header.Values("Last-Event-ID")) > 1 {
		notifications.WriteError(w, notifications.ErrInvalid)
		return
	}
	last := r.Header.Get("Last-Event-ID")
	if last != "" {
		if afterRaw != "" && last != afterRaw {
			notifications.WriteError(w, notifications.ErrInvalid)
			return
		}
		afterRaw = last
	}
	after := int64(0)
	if afterRaw != "" {
		after, e = strconv.ParseInt(afterRaw, 10, 64)
		if e != nil || after < 0 {
			notifications.WriteError(w, notifications.ErrInvalid)
			return
		}
	} else if r.URL.Path != "/api/realtime/replay" {
		after = notifications.HeadCursor
	}
	r, e = bindDevice(r)
	if e != nil {
		notifications.WriteError(w, e)
		return
	}
	page, e := h.store.Replay(r.Context(), actor, h.cfg.App, after, 32)
	if e != nil {
		notifications.WriteError(w, e)
		return
	}
	if r.URL.Path == "/api/realtime/replay" {
		notifications.WriteJSON(w, 200, page)
		return
	}
	// The store establishes omitted-cursor connections at a transactionally
	// authenticated high-water mark. Explicit after/Last-Event-ID values opt in
	// to replay and receive a recoverable reset cursor when retention passed them.
	after = page.Cursor
	if !h.acquire(actor) {
		w.Header().Set("Retry-After", "1")
		notifications.WriteJSON(w, 429, map[string]string{"code": "REALTIME_CONNECTION_LIMIT"})
		return
	}
	defer h.release(actor)
	copy := r.Clone(r.Context())
	copy.Header.Del("Authorization")
	copy.Header.Del("Cookie")
	if r.URL.Path == "/ws" {
		h.websocket(w, copy, actor, after, page)
		return
	}
	h.sse(w, copy, actor, after, page)
}

type packet struct {
	Type     string          `json:"type"`
	Data     json.RawMessage `json:"data"`
	Sequence int64           `json:"sequence,omitempty"`
}
type sink interface {
	Send(context.Context, packet) error
}

// pump keeps only one bounded database page and one ping signal per connection.
// Slow writers stop fetching; transport deadlines close them. Nothing relies on
// an in-memory broadcast queue, so reconnect/restart replay comes from SQL.
func (h *Handler) pump(ctx context.Context, r *http.Request, actor string, after int64, first notifications.Page, s sink, pings <-chan struct{}) error {
	return h.pumpCommands(ctx, r, actor, after, first, s, pings, nil)
}
func (h *Handler) pumpCommands(ctx context.Context, r *http.Request, actor string, after int64, first notifications.Page, s sink, pings <-chan struct{}, commands <-chan Command) error {
	connection, e := h.openConnection(ctx, actor)
	if e != nil {
		return e
	}
	defer h.closeConnection(ctx, actor, connection)
	ctx = rooms.WithChatConnection(ctx, connection)
	r = r.WithContext(ctx)
	ticker := time.NewTicker(h.cfg.PollInterval)
	defer ticker.Stop()
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	page := first
	send := func(p packet) error {
		if p.Sequence == 0 || strings.HasPrefix(p.Type, "owned.") {
			if e := h.currentConnection(ctx, actor, connection); e != nil {
				return e
			}
			if a, e := h.identity.Account(r); e != nil {
				return e
			} else if a != actor {
				return notifications.ErrInvalid
			}
		}
		c, cancel := context.WithTimeout(ctx, h.cfg.WriteTimeout)
		defer cancel()
		return s.Send(c, p)
	}
	for {
		if e := h.currentConnection(ctx, actor, connection); e != nil {
			return e
		}
		for _, event := range page.Events {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if e := h.currentConnection(ctx, actor, connection); e != nil {
				return e
			}
			// Identity is rechecked before each write, in addition to the transactional
			// repository check at every poll. Bytes already in flight cannot be recalled.
			a, e := h.identity.Account(r)
			if e != nil {
				return e
			}
			if a != actor {
				return notifications.ErrInvalid
			}
			if validator, ok := h.store.(notifications.EventRevalidator); ok {
				fresh, visible, x := validator.RevalidateEvent(ctx, actor, h.cfg.App, event)
				if x != nil {
					return x
				}
				if !visible {
					after = event.Sequence
					continue
				}
				event = fresh
			}
			if e = send(packet{Type: event.Type, Data: event.Data, Sequence: event.Sequence}); e != nil {
				return e
			}
			after = event.Sequence
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if page.Cursor > after {
			if e := send(packet{Type: "owned.cursor", Data: json.RawMessage(`null`), Sequence: page.Cursor}); e != nil {
				return e
			}
			after = page.Cursor
		}
		// Drain a backlog without a poll delay, but reauthenticate for every page.
		if page.Cursor < page.HighWater {
			p, e := h.store.Replay(ctx, actor, h.cfg.App, after, 32)
			if e != nil {
				return e
			}
			if p.Cursor <= after {
				return notifications.ErrUnavailable
			}
			page = p
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case command := <-commands:
			fresh, e := h.identity.Account(r)
			if e != nil {
				return e
			}
			if fresh != actor {
				return notifications.ErrInvalid
			}
			var data json.RawMessage
			if command.Type == "messages.subscribe" || command.Type == "messages.unsubscribe" {
				data, e = h.handleSubscription(ctx, actor, connection, command)
			} else if h.cfg.Commands == nil {
				return notifications.ErrInvalid
			} else {
				data, e = h.cfg.Commands.HandleCommand(ctx, actor, command)
			}
			if e != nil {
				return e
			}
			fresh, e = h.identity.Account(r)
			if e != nil {
				return e
			}
			if fresh != actor {
				return notifications.ErrInvalid
			}
			if e = h.currentConnection(ctx, actor, connection); e != nil {
				return e
			}
			if e = send(packet{Type: command.Type, Data: data}); e != nil {
				return e
			}
		case <-pings:
			if _, e := h.store.Replay(ctx, actor, h.cfg.App, after, 1); e != nil {
				return e
			}
			if e := send(packet{Type: "pong", Data: json.RawMessage(`null`)}); e != nil {
				return e
			}
		case <-heartbeat.C:
			if _, e := h.store.Replay(ctx, actor, h.cfg.App, after, 1); e != nil {
				return e
			}
			if e := send(packet{Type: "owned.heartbeat", Data: json.RawMessage(`null`)}); e != nil {
				return e
			}
		case <-ticker.C:
		}
		var e error
		page, e = h.store.Replay(ctx, actor, h.cfg.App, after, 32)
		if e != nil {
			return e
		}
	}
}

func (h *Handler) handleSubscription(ctx context.Context, actor, connection string, command Command) (json.RawMessage, error) {
	if h.cfg.Connections == nil || connection == "" || command.Endpoint != "DysonNetwork.Messager" {
		return nil, notifications.ErrInvalid
	}
	var input struct {
		RoomID string `json:"chat_room_id"`
	}
	d := json.NewDecoder(strings.NewReader(string(command.Data)))
	d.DisallowUnknownFields()
	if e := d.Decode(&input); e != nil || !authority.ValidID(input.RoomID) {
		return nil, notifications.ErrInvalid
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return nil, notifications.ErrInvalid
	}
	if e := h.cfg.Connections.SetSubscription(ctx, actor, connection, input.RoomID, command.Type == "messages.subscribe"); e != nil {
		return nil, e
	}
	return json.RawMessage(`null`), nil
}

type wsSink struct{ c *websocket.Conn }

func (s wsSink) Send(ctx context.Context, p packet) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return notifications.ErrUnavailable
	}
	if e := s.c.SetWriteDeadline(deadline); e != nil {
		return e
	}
	return s.c.WriteJSON(p)
}
func (h *Handler) websocket(w http.ResponseWriter, r *http.Request, actor string, after int64, page notifications.Page) {
	// Gorilla's default origin check permits absent Origin (native clients) and
	// same-host browser requests only. No permissive cross-origin callback.
	upgrader := websocket.Upgrader{HandshakeTimeout: h.cfg.WriteTimeout, ReadBufferSize: 1024, WriteBufferSize: 4096, EnableCompression: false}
	w.Header().Set("Cache-Control", "no-store")
	c, e := upgrader.Upgrade(w, r, nil)
	if e != nil {
		return
	}
	defer c.Close()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	r = r.WithContext(ctx)
	pings := make(chan struct{}, 1)
	commands := make(chan Command)
	readerDone := make(chan struct{})
	c.SetReadLimit(32768)
	go func() {
		defer close(readerDone)
		defer cancel()
		for {
			_ = c.SetReadDeadline(time.Now().Add(120 * time.Second))
			kind, b, e := c.ReadMessage()
			if e != nil {
				return
			}
			if kind != websocket.TextMessage {
				return
			}
			p, e := decodeCommand(b)
			if e != nil {
				return
			}
			if p.Type != "ping" {
				isSubscription := p.Type == "messages.subscribe" || p.Type == "messages.unsubscribe"
				if h.cfg.Commands == nil && (!isSubscription || h.cfg.Connections == nil) {
					return
				}
				select {
				case commands <- p:
				case <-ctx.Done():
					return
				}
				continue
			}
			if p.Endpoint != "" {
				return
			}

			select {
			case pings <- struct{}{}:
			default:
				return
			}
		}
	}()
	e = h.pumpCommands(ctx, r, actor, after, page, wsSink{c}, pings, commands)
	code := websocket.ClosePolicyViolation
	reason := "stream authorization ended"
	if errors.Is(e, context.Canceled) {
		code = websocket.CloseNormalClosure
		reason = "stream closed"
	}
	_ = c.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second))
	_ = c.Close()
	<-readerDone
}

type sseSink struct {
	w          http.ResponseWriter
	controller *http.ResponseController
}

func (s sseSink) Send(ctx context.Context, p packet) error {
	deadline, _ := ctx.Deadline()
	if e := s.controller.SetWriteDeadline(deadline); e != nil && !errors.Is(e, http.ErrNotSupported) {
		return e
	}
	b, e := json.Marshal(p)
	if e != nil {
		return e
	}
	if p.Sequence > 0 {
		if _, e = fmt.Fprintf(s.w, "id: %d\n", p.Sequence); e != nil {
			return e
		}
	}
	if _, e = fmt.Fprintf(s.w, "data: %s\n\n", b); e != nil {
		return e
	}
	return s.controller.Flush()
}
func (h *Handler) sse(w http.ResponseWriter, r *http.Request, actor string, after int64, page notifications.Page) {
	if _, ok := w.(http.Flusher); !ok {
		notifications.WriteError(w, notifications.ErrUnavailable)
		return
	}
	c := http.NewResponseController(w)
	// A live transport must expose write deadlines. ResponseRecorder's controlled
	// in-memory sink is used only by unit tests, never by a production host.
	if e := c.SetWriteDeadline(time.Now().Add(h.cfg.WriteTimeout)); e != nil {
		notifications.WriteError(w, notifications.ErrUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	if e := c.Flush(); e != nil {
		return
	}
	_ = h.pump(r.Context(), r, actor, after, page, sseSink{w, c}, nil)
}
