// SPDX-License-Identifier: AGPL-3.0-only
package messagerpg

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"solsynth.local/go-migration/ports/messager"
)

const testRoom = "a1000000-0000-0000-0000-000000000001"
const testAccount = "a2000000-0000-0000-0000-000000000001"
const testSender = "a3000000-0000-0000-0000-000000000001"
const testMessage = "a4000000-0000-0000-0000-000000000001"
const testMessage2 = "a4000000-0000-0000-0000-000000000002"

type queryStep struct {
	contains string
	args     []any
	rows     [][]driver.Value
	err      error
}
type recorder struct {
	mu                 sync.Mutex
	t                  *testing.T
	steps              []queryStep
	queries            []string
	options            driver.TxOptions
	commits, rollbacks int
}
type connector struct{ r *recorder }
type recordDriver struct{}
type recordConn struct{ r *recorder }
type recordTx struct{ r *recorder }
type recordRows struct {
	values [][]driver.Value
	width  int
}

func (c connector) Connect(context.Context) (driver.Conn, error) { return &recordConn{c.r}, nil }
func (c connector) Driver() driver.Driver                        { return recordDriver{} }
func (recordDriver) Open(string) (driver.Conn, error)            { return nil, errors.New("use connector") }
func (c *recordConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare forbidden")
}
func (c *recordConn) Close() error              { return nil }
func (c *recordConn) Begin() (driver.Tx, error) { return nil, errors.New("use BeginTx") }
func (c *recordConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.r.mu.Lock()
	defer c.r.mu.Unlock()
	c.r.options = opts
	return &recordTx{c.r}, nil
}
func (c *recordConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.r.mu.Lock()
	defer c.r.mu.Unlock()
	c.r.queries = append(c.r.queries, q)
	if len(c.r.steps) == 0 {
		c.r.t.Errorf("unexpected query: %s", q)
		return nil, errors.New("unexpected query")
	}
	step := c.r.steps[0]
	c.r.steps = c.r.steps[1:]
	if !strings.Contains(q, step.contains) {
		c.r.t.Errorf("query %s does not contain %s", q, step.contains)
	}
	if step.args != nil {
		got := make([]any, len(args))
		for i, a := range args {
			got[i] = a.Value
		}
		if !reflect.DeepEqual(got, step.args) {
			c.r.t.Errorf("args %v want %v", got, step.args)
		}
	}
	if step.err != nil {
		return nil, step.err
	}
	width := 1
	if len(step.rows) > 0 {
		width = len(step.rows[0])
	}
	return &recordRows{values: step.rows, width: width}, nil
}
func (t *recordTx) Commit() error { t.r.mu.Lock(); defer t.r.mu.Unlock(); t.r.commits++; return nil }
func (t *recordTx) Rollback() error {
	t.r.mu.Lock()
	defer t.r.mu.Unlock()
	t.r.rollbacks++
	return nil
}
func (r *recordRows) Columns() []string {
	a := make([]string, r.width)
	for i := range a {
		a[i] = "value"
	}
	return a
}
func (r *recordRows) Close() error { return nil }
func (r *recordRows) Next(dst []driver.Value) error {
	if len(r.values) == 0 {
		return io.EOF
	}
	copy(dst, r.values[0])
	r.values = r.values[1:]
	return nil
}
func recorded(t *testing.T, steps ...queryStep) (*Repository, *recorder) {
	t.Helper()
	r := &recorder{t: t, steps: steps}
	db := sql.OpenDB(connector{r})
	t.Cleanup(func() {
		db.Close()
		if len(r.steps) != 0 {
			t.Errorf("unused steps: %d", len(r.steps))
		}
	})
	repo := NewSyntheticProjectionRepository(db, 77)
	repo.now = func() time.Time { return time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC) }
	return repo, r
}
func messageRow(id string, seq int64, when time.Time, encrypted bool) []driver.Value {
	var encryption driver.Value
	if encrypted {
		encryption = []byte(`{"epoch":9007199254740993,"ciphertext":"abc","unknown":[true]}`)
	}
	return []driver.Value{id, testRoom, testSender, "text", seq, "# heading", nil, when, when, nil, nil, []byte(`{"large":9007199254740993}`), nil, []byte(`[]`), encryption, []byte(`{"heart":2}`), nil, nil, nil}
}
func roomStep(public bool, mode int64) queryStep {
	return queryStep{contains: "FROM chat_rooms", args: []any{testRoom}, rows: [][]driver.Value{{public, mode}}}
}
func TestDefaultRepositoriesAreDenied(t *testing.T) {
	for _, r := range []*Repository{nil, {}, New(nil)} {
		if _, err := r.Sync(context.Background(), messager.SyncQuery{}); !errors.Is(err, ErrUnverifiedPolicy) {
			t.Fatal(err)
		}
		if _, err := r.ListMessages(context.Background(), messager.MessageListQuery{}); !errors.Is(err, ErrUnverifiedPolicy) {
			t.Fatal(err)
		}
		if _, err := r.GetMessage(context.Background(), messager.MessageQuery{}); !errors.Is(err, ErrUnverifiedPolicy) {
			t.Fatal(err)
		}
	}
	r := NewSyntheticProjectionRepository(nil, 77)
	q := messager.SyncQuery{RoomID: testRoom, AccountID: testAccount}
	if _, err := r.Sync(context.Background(), q); !errors.Is(err, ErrDatabaseRequired) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Sync(ctx, q); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	q.AccountID = ""
	if _, err := r.Sync(context.Background(), q); !errors.Is(err, messager.ErrSyncUnauthorized) {
		t.Fatal(err)
	}
}
func TestSequenceSelectionUsesBoundInt64Arrays(t *testing.T) {
	q := messager.SyncQuery{RoomID: testRoom, AccountID: testAccount, Request: messager.SyncRequest{LastSyncTimestamp: math.MaxInt64, MissingSequences: []int64{math.MaxInt64, 5, 5, -1, 0}, MissingSequenceRanges: []messager.SequenceRange{{StartSequence: 9, EndSequence: 7}, {StartSequence: 10, EndSequence: 12}, {StartSequence: -3, EndSequence: 1}}}}
	if err := messager.ValidateSyncQuery(q); err != nil {
		t.Fatal(err)
	}
	where, args := selection(q)
	if strings.Contains(where, "created_at >") || !strings.Contains(where, "m.thread_id IS NULL") || !reflect.DeepEqual(args, []any{testRoom, "{5,9223372036854775807}", "{7}", "{12}"}) {
		t.Fatal(where, args)
	}
	q.Request = messager.SyncRequest{LastSyncTimestamp: 1234, MissingSequences: []int64{-1, 0}, MissingSequenceRanges: []messager.SequenceRange{{StartSequence: -1, EndSequence: 3}}}
	where, args = selection(q)
	if !strings.Contains(where, "created_at > $2") || args[1] != time.UnixMilli(1234).UTC() {
		t.Fatal(where, args)
	}
}
func TestSyncSnapshotHydratesWithoutFabricatingAccounts(t *testing.T) {
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	r, rec := recorded(t, roomStep(false, 77), queryStep{contains: "joined_at IS NOT NULL AND leave_at IS NULL", args: []any{testRoom, testAccount}, rows: [][]driver.Value{{true}}}, queryStep{contains: "SELECT count(*)", rows: [][]driver.Value{{int64(3)}}}, queryStep{contains: "ORDER BY m.room_sequence ASC,m.created_at ASC LIMIT 1000", rows: [][]driver.Value{messageRow(testMessage, 1, newer, false), messageRow(testMessage2, 2, older, true)}}, queryStep{contains: "sender.account_id=$2", rows: [][]driver.Value{{testMessage, "heart"}}}, queryStep{contains: "thread_id=ANY($1::uuid[]) AND deleted_at IS NULL", rows: [][]driver.Value{{testMessage, int64(2)}}})
	got, err := r.Sync(context.Background(), messager.SyncQuery{RoomID: testRoom, AccountID: testAccount})
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalCount != 3 || len(got.Messages) != 2 || !got.CurrentTimestamp.Equal(older) {
		t.Fatal(got)
	}
	a, b := got.Messages[0], got.Messages[1]
	if *a.Content != "\\# heading" || *b.Content != "# heading" || !a.ReactionsMade["heart"] || len(b.ReactionsMade) != 0 || !a.IsThreadRoot || a.ThreadRepliesCount != 2 || b.IsThreadRoot {
		t.Fatal(a, b)
	}
	raw, err := json.Marshal(got)
	if err != nil || !strings.Contains(string(raw), "9007199254740993") || strings.Contains(string(raw), `"sender":`) || strings.Contains(string(raw), `"current_message_id":`) {
		t.Fatal(string(raw), err)
	}
	if !rec.options.ReadOnly || rec.options.Isolation != driver.IsolationLevel(sql.LevelRepeatableRead) || rec.commits != 1 || rec.rollbacks != 0 {
		t.Fatal(rec)
	}
}
func TestAccessIsCheckedBeforeMessageSelection(t *testing.T) {
	for _, tc := range []struct {
		name       string
		room       queryStep
		membership *bool
		account    string
		want       error
	}{{"missing", queryStep{contains: "FROM chat_rooms"}, nil, testAccount, messager.ErrSyncRoomNotFound}, {"private-member-denied", roomStep(false, 77), new(bool), testAccount, messager.ErrSyncForbidden}, {"encrypted-public-denied", roomStep(true, 88), new(bool), testAccount, messager.ErrSyncForbidden}, {"private-anonymous", roomStep(false, 77), nil, "", messager.ErrSyncUnauthorized}} {
		t.Run(tc.name, func(t *testing.T) {
			steps := []queryStep{tc.room}
			if tc.membership != nil {
				steps = append(steps, queryStep{contains: "SELECT EXISTS", rows: [][]driver.Value{{*tc.membership}}})
			}
			r, rec := recorded(t, steps...)
			_, err := r.ListMessages(context.Background(), messager.MessageListQuery{RoomID: testRoom, AccountID: tc.account, Take: 20})
			if !errors.Is(err, tc.want) || rec.commits != 0 || rec.rollbacks != 1 {
				t.Fatal(err, rec)
			}
		})
	}
}
func TestEmptySyncUsesClockAndNonNullArray(t *testing.T) {
	r, rec := recorded(t, roomStep(true, 77), queryStep{contains: "SELECT count(*)", rows: [][]driver.Value{{int64(0)}}}, queryStep{contains: "m.thread_id IS NULL"})
	got, err := r.Sync(context.Background(), messager.SyncQuery{RoomID: testRoom, AccountID: testAccount})
	if err != nil || got.Messages == nil || !got.CurrentTimestamp.Equal(r.now()) || rec.commits != 1 {
		t.Fatal(got, err)
	}
}
func TestListAndSingleSourceSelection(t *testing.T) {
	r, _ := recorded(t, roomStep(true, 77), queryStep{contains: "m.thread_id IS NULL", rows: [][]driver.Value{{int64(10)}}}, queryStep{contains: "ORDER BY m.room_sequence DESC,m.created_at DESC LIMIT $2 OFFSET $3", args: []any{testRoom, int64(0), int64(9)}})
	got, err := r.ListMessages(context.Background(), messager.MessageListQuery{RoomID: testRoom, Take: 0, Offset: 9})
	if err != nil || got.TotalCount != 10 || got.Messages == nil {
		t.Fatal(got, err)
	}
	r, rec := recorded(t, roomStep(true, 77), queryStep{contains: "m.id=$2 LIMIT 1", args: []any{testRoom, testMessage}})
	_, err = r.GetMessage(context.Background(), messager.MessageQuery{RoomID: testRoom, MessageID: testMessage})
	if !errors.Is(err, messager.ErrSyncMessageNotFound) || strings.Contains(rec.queries[1], "thread_id IS NULL") {
		t.Fatal(err, rec.queries)
	}
}
func TestBadHydrationRollsBack(t *testing.T) {
	for _, invalid := range []string{"json", "duplicate-reaction", "thread-overflow"} {
		t.Run(invalid, func(t *testing.T) {
			row := messageRow(testMessage, 1, time.Now(), false)
			if invalid == "json" {
				row[13] = []byte(`{}`)
			}
			steps := []queryStep{roomStep(true, 77), {contains: "SELECT count(*)", rows: [][]driver.Value{{int64(1)}}}, {contains: "ORDER BY", rows: [][]driver.Value{row}}}
			if invalid == "duplicate-reaction" {
				steps = append(steps, queryStep{contains: "FROM chat_reactions", rows: [][]driver.Value{{testMessage, "x"}, {testMessage, "x"}}})
			}
			if invalid == "thread-overflow" {
				steps = append(steps, queryStep{contains: "FROM chat_reactions"}, queryStep{contains: "GROUP BY thread_id", rows: [][]driver.Value{{testMessage, int64(math.MaxInt32) + 1}}})
			}
			r, rec := recorded(t, steps...)
			_, err := r.Sync(context.Background(), messager.SyncQuery{RoomID: testRoom, AccountID: testAccount})
			if !errors.Is(err, ErrInvalidStoredProjection) || rec.commits != 0 || rec.rollbacks != 1 {
				t.Fatal(err, rec)
			}
		})
	}
}
func TestReadQueryFailureRollsBack(t *testing.T) {
	failure := errors.New("private database detail")
	r, rec := recorded(t, roomStep(true, 77), queryStep{contains: "SELECT count(*)", err: failure})
	_, err := r.Sync(context.Background(), messager.SyncQuery{RoomID: testRoom, AccountID: testAccount})
	if !errors.Is(err, failure) || rec.rollbacks != 1 || rec.commits != 0 {
		t.Fatal(err, rec)
	}
}

func TestUnreadUsesSourceExclusionsAndIndependentPreference(t *testing.T) {
	r, rec := recorded(t, queryStep{contains: "left(msg.type,9)<>'messages.'", args: []any{testAccount, int64(11), int64(22)}, rows: [][]driver.Value{{testRoom, int64(0), int64(4)}, {"a1000000-0000-0000-0000-000000000002", int64(2), int64(3)}}})
	r.unreadPolicy = &SyntheticUnreadPolicy{All: 11, Mentions: 22, None: 33}
	got, err := r.Unread(context.Background(), testAccount)
	if err != nil || got[testRoom].UnreadCount != 0 || !got[testRoom].HasUnread || rec.commits != 1 {
		t.Fatal(got, err)
	}
	q := rec.queries[0]
	if strings.Contains(q, "thread_id") || strings.Contains(q, "chat_rooms") || strings.Contains(q, "sender.deleted_at") || !strings.Contains(q, "msg.created_at>member.last_read_at") || !strings.Contains(q, "jsonb_build_array($1::text)") {
		t.Fatal(q)
	}
}
func TestUnreadRequiresIndependentMappingAndChecksOverflow(t *testing.T) {
	if _, err := NewSyntheticProjectionRepositoryWithUnread(nil, 77, SyntheticUnreadPolicy{All: 1, Mentions: 1, None: 3}); err == nil {
		t.Fatal("ambiguous mapping")
	}
	r := NewSyntheticProjectionRepository(nil, 77)
	if _, err := r.Unread(context.Background(), testAccount); !errors.Is(err, ErrUnverifiedPolicy) {
		t.Fatal(err)
	}
	r, rec := recorded(t, queryStep{contains: "GROUP BY member.chat_room_id", rows: [][]driver.Value{{testRoom, int64(0), int64(math.MaxInt32) + 1}}})
	r.unreadPolicy = &SyntheticUnreadPolicy{All: 11, Mentions: 22, None: 33}
	if _, err := r.Unread(context.Background(), testAccount); !errors.Is(err, messager.ErrUnreadCountOverflow) || rec.commits != 0 || rec.rollbacks != 1 {
		t.Fatal(err, rec)
	}
}
