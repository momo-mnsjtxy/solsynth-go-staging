// SPDX-License-Identifier: AGPL-3.0-only
package friendeventpg_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/friendeventpg"
	"solsynth.local/go-migration/internal/notificationpg"
	"solsynth.local/go-migration/internal/presencepg"
	"solsynth.local/go-migration/ports/notifications"
	"solsynth.local/go-migration/ports/passport/presence"
)

const owner = "11111111-1111-4111-8111-111111111111"
const recipient = "99999999-9999-4999-8999-999999999999"
const session = "33333333-3333-4333-8333-333333333333"
const eventID = "44444444-4444-4444-8444-444444444444"

// This single-connection driver exercises the real source/status/queue methods
// without PostgreSQL, network, a bearer token, or an external delivery provider.
// Predicate evaluation and physical lock behavior remain separate PG obligations.
type eventConnector struct{ c *eventConn }

func (v eventConnector) Connect(context.Context) (driver.Conn, error) {
	v.c.connections++
	return v.c, nil
}
func (eventConnector) Driver() driver.Driver { return eventDriver{} }

type eventDriver struct{}

func (eventDriver) Open(string) (driver.Conn, error) { return nil, errors.New("network unavailable") }

type eventRows struct {
	count int
	data  [][]driver.Value
}

func (r *eventRows) Columns() []string { return make([]string, r.count) }
func (*eventRows) Close() error        { return nil }
func (r *eventRows) Next(dst []driver.Value) error {
	if len(r.data) == 0 {
		return io.EOF
	}
	copy(dst, r.data[0])
	r.data = r.data[1:]
	return nil
}
func values(count int, rows ...[]driver.Value) driver.Rows { return &eventRows{count, rows} }

type streamState struct {
	sequence          int64
	processed, marker bool
}
type eventConn struct {
	at                                      time.Time
	connections, commits, rollbacks, checks int
	locks                                   []string
	status                                  presence.StatusRequest
	generation, friendship, sourceOK        bool
	active                                  bool
	committed, working                      streamState
	failInsert, failComplete                bool
	afterStatus                             func()
}

func (*eventConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unexpected prepare") }
func (*eventConn) Close() error                        { return nil }
func (c *eventConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{Isolation: driver.IsolationLevel(sql.LevelReadCommitted)})
}
func (c *eventConn) BeginTx(_ context.Context, o driver.TxOptions) (driver.Tx, error) {
	if c.active || o.Isolation != driver.IsolationLevel(sql.LevelReadCommitted) {
		return nil, errors.New("invalid transaction boundary")
	}
	c.active = true
	c.working = c.committed
	c.locks = nil
	return eventTx{c}, nil
}
func (*eventConn) CheckNamedValue(*driver.NamedValue) error { return nil }

type eventTx struct{ c *eventConn }

func (t eventTx) Commit() error {
	t.c.committed = t.c.working
	t.c.active = false
	t.c.commits++
	return nil
}
func (t eventTx) Rollback() error { t.c.active = false; t.c.rollbacks++; return nil }
func (c *eventConn) QueryContext(ctx context.Context, q string, a []driver.NamedValue) (driver.Rows, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	switch {
	case strings.HasPrefix(q, "SELECT a.event_id::text"):
		if c.active {
			return nil, errors.New("pending discovery retained a transaction")
		}
		if c.committed.processed {
			return values(3), nil
		}
		return values(3, []driver.Value{eventID, recipient, owner}), nil
	case strings.HasPrefix(q, "SELECT NOT disabled"):
		c.locks = append(c.locks, a[0].Value.(string))
		return values(1, []driver.Value{true}), nil
	case strings.HasPrefix(q, "SELECT processed_at IS NULL"):
		if len(c.locks) != 2 || c.locks[0] != owner || c.locks[1] != recipient {
			return nil, errors.New("audience lock preceded sorted account locks")
		}
		return values(1, []driver.Value{!c.working.processed}), nil
	case strings.HasPrefix(q, "SELECT e.id::text"):
		return values(12, []driver.Value{eventID, owner, "", int64(4), session, int64(7), int64(3), c.at, "status", int64(4), int64(8), int64(9)}), nil
	case strings.HasPrefix(q, "SELECT COALESCE((SELECT revision"):
		if a[0].Value != owner || a[1].Value != int64(4) {
			return nil, errors.New("wrong presence incarnation")
		}
		return values(1, []driver.Value{c.generation}), nil
	case strings.HasPrefix(q, "SELECT EXISTS(SELECT 1 FROM owned_social_effective_relationships f JOIN"):
		if a[0].Value != owner || a[1].Value != recipient || a[2].Value != int64(8) || a[3].Value != int64(9) {
			return nil, errors.New("wrong friendship incarnation")
		}
		return values(1, []driver.Value{c.friendship}), nil
	case q == "SELECT clock_timestamp()":
		return values(1, []driver.Value{c.at}), nil
	case strings.HasPrefix(q, "SELECT online_visibility"):
		return values(3, []driver.Value{"private", "private", "public"}), nil
	case strings.HasPrefix(q, "SELECT id::text,created_at"):
		return values(2, []driver.Value{owner, c.at.Add(-time.Hour)}), nil
	case strings.HasPrefix(q, "SELECT EXISTS(SELECT 1 FROM owned_social_effective_relationships WHERE"):
		return values(2, []driver.Value{false, true}), nil
	case strings.HasPrefix(q, "SELECT id::text,account_id::text,attitude"):
		if c.afterStatus != nil {
			c.afterStatus()
			c.afterStatus = nil
		}
		return values(12, []driver.Value{eventID, owner, int64(c.status.Attitude), int64(c.status.Type), "Synthetic", nil, nil, false, []byte(c.status.Meta), nil, c.at, c.at}), nil
	case strings.HasPrefix(q, "SELECT COALESCE((SELECT type=3"):
		return values(1, []driver.Value{false}), nil
	case strings.HasPrefix(q, "SELECT COALESCE((SELECT status_visibility<>"):
		return values(1, []driver.Value{true}), nil
	case strings.HasPrefix(q, "SELECT EXISTS(SELECT 1 FROM notification_v1.events"):
		return values(1, []driver.Value{c.working.marker}), nil
	case strings.HasPrefix(q, "INSERT INTO notification_v1.streams"):
		c.working.sequence++
		return values(1, []driver.Value{c.working.sequence}), nil
	default:
		return nil, fmt.Errorf("unexpected query: %s", q)
	}
}
func (c *eventConn) ExecContext(ctx context.Context, q string, a []driver.NamedValue) (driver.Result, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	switch {
	case strings.HasPrefix(q, "INSERT INTO notification_v1.events"):
		if c.failInsert {
			return nil, errors.New("synthetic stream failure")
		}
		if a[4].Value != "account.status.updated" || a[5].Value != "{}" || a[6].Value != "friend" {
			return nil, errors.New("private payload persisted in marker")
		}
		c.working.marker = true
	case strings.HasPrefix(q, "UPDATE owned_friend_audiences"):
		if c.failComplete {
			return nil, errors.New("synthetic receipt failure")
		}
		c.working.processed = true
	default:
		return nil, fmt.Errorf("unexpected mutation: %s", q)
	}
	return driver.RowsAffected(1), nil
}

type eventGuard struct {
	c  *eventConn
	db *sql.DB
	tx *sql.Tx
}

func (g *eventGuard) StoredAuthenticationEligible(ctx context.Context, db *sql.DB, tx *sql.Tx, a, s string, v, e int64) (bool, error) {
	if db != g.db || tx == nil || a != owner || s != session || v != 7 || e != 3 {
		return false, errors.New("source admission replaced")
	}
	g.c.checks++
	return g.c.sourceOK, ctx.Err()
}

type noActivities struct{}

func (noActivities) FriendActivitiesInTransaction(context.Context, *sql.Tx, string, friendeventpg.Source) (json.RawMessage, bool, error) {
	return nil, false, errors.New("unexpected activity projection")
}
func fixture(t *testing.T) (*eventConn, *sql.DB, *friendeventpg.Repository, *notificationpg.Repository) {
	t.Helper()
	c := &eventConn{at: time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC), generation: true, friendship: true, sourceOK: true, status: presence.StatusRequest{Meta: json.RawMessage(`{"visible":true}`)}}
	db := sql.OpenDB(eventConnector{c})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	source := friendeventpg.New(db, &eventGuard{c: c, db: db}, noActivities{}).WithStatus(presencepg.New(db))
	return c, db, source, notificationpg.New(db, nil, notifications.DefaultApp).WithFriendEvents(source)
}
func TestFriendEventStatusEscapingBoundAndQueueRecovery(t *testing.T) {
	c, db, source, queue := fixture(t)
	c.status.Meta = json.RawMessage(`{"padding":"` + strings.Repeat("<", 15000) + `"}`)
	if e := presence.ValidateRequest(c.status); e != nil {
		t.Fatal("fixture must be valid", e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	tx, e := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		t.Fatal(e)
	}
	event, allowed, e := source.ProjectAudienceEvent(ctx, tx, recipient, eventID)
	tx.Rollback()
	if e != nil || allowed || len(event.Data) != 0 {
		t.Fatalf("oversized valid status escaped: allowed=%v bytes=%d error=%v", allowed, len(event.Data), e)
	}
	if n, e := queue.ProjectFriends(ctx, 32); e != nil || n != 1 || !c.committed.processed || c.committed.sequence != 0 {
		t.Fatal("oversized receipt poisoned queue", n, e, c.committed)
	}
	c.committed.processed = false
	c.status.Meta = json.RawMessage(`{"bounded":true}`)
	if n, e := queue.ProjectFriends(ctx, 32); e != nil || n != 1 || c.committed.sequence != 1 || !c.committed.marker {
		t.Fatal("bounded recovery failed", n, e, c.committed)
	}
	if n, e := queue.ProjectFriends(ctx, 32); e != nil || n != 0 || c.committed.sequence != 1 || c.connections != 1 {
		t.Fatal("duplicate or additional connection", n, e, c.connections, c.committed)
	}
}
func TestFriendEventImportRollbackAndRetry(t *testing.T) {
	for _, mode := range []string{"stream", "receipt"} {
		t.Run(mode, func(t *testing.T) {
			c, _, _, queue := fixture(t)
			c.failInsert = mode == "stream"
			c.failComplete = mode == "receipt"
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if n, e := queue.ProjectFriends(ctx, 32); e == nil || n != 0 || c.committed != (streamState{}) || c.rollbacks != 1 {
				t.Fatal(n, e, c.committed, c.rollbacks)
			}
			c.failInsert = false
			c.failComplete = false
			if n, e := queue.ProjectFriends(ctx, 32); e != nil || n != 1 || c.committed.sequence != 1 || !c.committed.processed {
				t.Fatal(n, e, c.committed)
			}
		})
	}
}
func TestFriendEventCurrentAdmissionAndPostProjectionSource(t *testing.T) {
	for _, mode := range []string{"allowed", "presence_aba", "friendship_aba", "source_withdrawn", "source_after_status", "cancel_after_status"} {
		t.Run(mode, func(t *testing.T) {
			c, db, source, _ := fixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			switch mode {
			case "presence_aba":
				c.generation = false
			case "friendship_aba":
				c.friendship = false
			case "source_withdrawn":
				c.sourceOK = false
			case "source_after_status":
				c.afterStatus = func() { c.sourceOK = false }
			case "cancel_after_status":
				c.afterStatus = cancel
			}
			tx, e := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback()
			event, allowed, e := source.ProjectAudienceEvent(ctx, tx, recipient, eventID)
			if mode == "cancel_after_status" {
				if !errors.Is(e, context.Canceled) && !errors.Is(e, context.DeadlineExceeded) {
					t.Fatal(e)
				}
				return
			}
			if e != nil || allowed != (mode == "allowed") {
				t.Fatal(mode, allowed, e)
			}
			if !allowed {
				if len(event.Data) != 0 {
					t.Fatal("hidden event body", string(event.Data))
				}
				return
			}
			var data map[string]json.RawMessage
			if e = json.Unmarshal(event.Data, &data); e != nil || len(data) != 3 || data["account_id"] == nil || data["status"] == nil || data["timestamp"] == nil || data["device_id"] != nil || c.checks != 2 {
				t.Fatal(data, e, c.checks)
			}
		})
	}
}
