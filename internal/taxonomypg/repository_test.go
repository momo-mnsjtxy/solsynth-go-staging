// SPDX-License-Identifier: AGPL-3.0-only
package taxonomypg

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"solsynth.local/go-migration/ports/sphere/taxonomy"
)

func ptr[T any](v T) *T { return &v }

const id = "10000000-0000-0000-0000-000000000001"
const owner = "10000000-0000-0000-0000-000000000002"

var now = time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)

func TestRepositoryProductionDefaults(t *testing.T) {
	for _, r := range []*Repository{nil, {}, New(nil)} {
		ctx := context.Background()
		if _, e := r.List(ctx, taxonomy.Query{Kind: taxonomy.Tag}); !errors.Is(e, taxonomy.ErrPolicyUnverified) {
			t.Fatal(e)
		}
		if _, e := r.Get(ctx, taxonomy.Tag, "x", false); !errors.Is(e, taxonomy.ErrPolicyUnverified) {
			t.Fatal(e)
		}
		if _, e := r.Create(ctx, taxonomy.Record{}); !errors.Is(e, taxonomy.ErrPolicyUnverified) {
			t.Fatal(e)
		}
		if _, e := r.Mutate(ctx, taxonomy.Tag, "x", taxonomy.Delete, taxonomy.Change{}, nil); !errors.Is(e, taxonomy.ErrPolicyUnverified) {
			t.Fatal(e)
		}
		if _, e := r.Quota(ctx, owner); !errors.Is(e, taxonomy.ErrPolicyUnverified) {
			t.Fatal(e)
		}
		if _, _, e := r.Subscriptions(ctx, id, 0, 20); !errors.Is(e, taxonomy.ErrPolicyUnverified) {
			t.Fatal(e)
		}
		if _, _, e := r.Subscription(ctx, taxonomy.Tag, "x", id, "subscribe", now); !errors.Is(e, taxonomy.ErrPolicyUnverified) {
			t.Fatal(e)
		}
	}
	r := NewSyntheticRepository(nil, 99)
	if _, e := r.List(context.Background(), taxonomy.Query{Kind: taxonomy.Tag}); !errors.Is(e, taxonomy.ErrDatabaseRequired) {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := r.List(ctx, taxonomy.Query{Kind: taxonomy.Tag}); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
func TestListSQLUsesSourcePredicatesAndBoundArguments(t *testing.T) {
	for _, k := range []taxonomy.Kind{taxonomy.Tag, taxonomy.Category} {
		q := taxonomy.Query{Kind: k, Search: " %' OR true -- ", Order: "popularity", Now: now, Offset: 5, Take: 0}
		count, rows, args, e := buildList(q, 99)
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(count, q.Search) || strings.Contains(rows, q.Search) || !strings.Contains(count, "t.slug ILIKE $1") || strings.Contains(count, "t.name ILIKE") || !strings.Contains(rows, "p.drafted_at IS NULL") || !strings.Contains(rows, "p.visibility=$2") || !strings.Contains(rows, "p.created_at>=$3 AND p.created_at<=$4") || !strings.Contains(rows, "OFFSET $5 LIMIT $6") {
			t.Fatal(count, rows)
		}
		if len(args) != 6 || args[0] != "%"+q.Search+"%" || args[1] != int32(99) || args[4] != 5 || args[5] != 0 || argumentCount(count) != 1 {
			t.Fatal(args)
		}
	}
	q := taxonomy.Query{Kind: taxonomy.Tag, Admin: true, Search: "  needle  ", Order: "usage", OwnerPublisherID: ptr(owner), IsProtected: ptr(false), IsEvent: ptr(true), Unowned: ptr(true), Offset: -2, Take: 500}
	count, rows, args, e := buildList(q, 42)
	if e != nil || len(args) != 6 || args[0] != "%needle%" || args[4] != 0 || args[5] != 200 || !strings.Contains(count, "t.description ILIKE $1") || !strings.Contains(count, "t.owner_publisher_id IS NULL") || !strings.Contains(rows, "ORDER BY usage DESC,t.slug ASC") {
		t.Fatal(count, rows, args, e)
	}
	for _, tc := range []struct {
		admin       bool
		order, want string
	}{{false, "", "t.name ASC"}, {false, "created", "t.created_at DESC"}, {false, "unknown", "t.created_at DESC"}, {true, "name", "coalesce(t.name,t.slug) ASC"}, {true, "created", "t.created_at DESC"}, {true, "unknown", "t.updated_at DESC"}} {
		_, q, _, e := buildList(taxonomy.Query{Kind: taxonomy.Category, Admin: tc.admin, Order: tc.order, Take: 20}, 0)
		if e != nil || !strings.Contains(q, "ORDER BY "+tc.want) {
			t.Fatal(tc, q, e)
		}
	}
	if argumentCount("x=$1 AND y=$12 AND z=$2") != 12 {
		t.Fatal("placeholder counting")
	}
}

type step struct {
	kind, contains string
	args           []any
	data           [][]driver.Value
	err            error
}
type recorder struct {
	mu     sync.Mutex
	t      *testing.T
	steps  []step
	events []string
}

func (rec *recorder) next(kind, q string, args []driver.NamedValue) (step, error) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.events = append(rec.events, kind)
	if len(rec.steps) == 0 {
		rec.t.Errorf("unexpected %s: %s", kind, q)
		return step{}, errors.New("unexpected SQL")
	}
	s := rec.steps[0]
	rec.steps = rec.steps[1:]
	if s.kind != kind || !strings.Contains(q, s.contains) {
		rec.t.Errorf("got %s %s, want %s containing %s", kind, q, s.kind, s.contains)
	}
	if s.args != nil {
		v := []any{}
		for _, a := range args {
			v = append(v, a.Value)
		}
		if !reflect.DeepEqual(v, s.args) {
			rec.t.Errorf("args %+v want %+v", v, s.args)
		}
	}
	return s, s.err
}
func (rec *recorder) event(s string) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.events = append(rec.events, s)
}

type connector struct{ rec *recorder }

func (c connector) Connect(context.Context) (driver.Conn, error) { return &conn{rec: c.rec}, nil }
func (c connector) Driver() driver.Driver                        { return drv{} }

type drv struct{}

func (drv) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type conn struct{ rec *recorder }

func (c *conn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unexpected prepare") }
func (c *conn) Close() error                        { return nil }
func (c *conn) Begin() (driver.Tx, error)           { c.rec.event("begin"); return tx{c.rec}, nil }
func (c *conn) BeginTx(_ context.Context, o driver.TxOptions) (driver.Tx, error) {
	c.rec.event("begin")
	return tx{c.rec}, nil
}
func (c *conn) QueryContext(_ context.Context, q string, a []driver.NamedValue) (driver.Rows, error) {
	s, e := c.rec.next("query", q, a)
	if e != nil {
		return nil, e
	}
	n := 1
	if len(s.data) > 0 {
		n = len(s.data[0])
	}
	cols := make([]string, n)
	return &rows{cols: cols, data: s.data}, nil
}
func (c *conn) ExecContext(_ context.Context, q string, a []driver.NamedValue) (driver.Result, error) {
	_, e := c.rec.next("exec", q, a)
	return driver.RowsAffected(1), e
}

type tx struct{ rec *recorder }

func (t tx) Commit() error   { t.rec.event("commit"); return nil }
func (t tx) Rollback() error { t.rec.event("rollback"); return nil }

type rows struct {
	cols []string
	data [][]driver.Value
}

func (r *rows) Columns() []string { return r.cols }
func (r *rows) Close() error      { return nil }
func (r *rows) Next(v []driver.Value) error {
	if len(r.data) == 0 {
		return io.EOF
	}
	copy(v, r.data[0])
	r.data = r.data[1:]
	return nil
}
func database(t *testing.T, steps ...step) (*sql.DB, *recorder) {
	t.Helper()
	rec := &recorder{t: t, steps: steps}
	db := sql.OpenDB(connector{rec})
	t.Cleanup(func() {
		db.Close()
		if len(rec.steps) != 0 {
			t.Errorf("%d unexecuted SQL steps", len(rec.steps))
		}
	})
	return db, rec
}
func recordValues() []driver.Value {
	return []driver.Value{id, "tag", nil, "desc", owner, false, false, nil, now, now, int64(0)}
}
func TestRecorderListAndDetail(t *testing.T) {
	db, rec := database(t, step{kind: "query", contains: "SELECT count(*)", data: [][]driver.Value{{int64(12)}}}, step{kind: "query", contains: "ORDER BY usage DESC", data: [][]driver.Value{recordValues()}})
	result, e := NewSyntheticRepository(db, 99).List(context.Background(), taxonomy.Query{Kind: taxonomy.Tag, Order: "usage", Take: 20})
	if e != nil || result.Total != 12 || len(result.Rows) != 1 || result.Rows[0].Name != nil || *result.Rows[0].OwnerPublisherID != owner {
		t.Fatal(result, e)
	}
	if !reflect.DeepEqual(rec.events, []string{"begin", "query", "query", "commit"}) {
		t.Fatal(rec.events)
	}
	db2, _ := database(t, step{kind: "query", contains: "lower(t.slug)=$1", args: []any{"tag"}, data: [][]driver.Value{recordValues()}})
	row, e := NewSyntheticRepository(db2, 99).Get(context.Background(), taxonomy.Tag, " TaG ", false)
	if e != nil || row.Slug != "tag" {
		t.Fatal(row, e)
	}
}
func TestRecorderCreateDuplicateRollback(t *testing.T) {
	db, rec := database(t, step{kind: "exec", contains: "LOCK TABLE taxonomy_candidate.post_tags"}, step{kind: "query", contains: "lower(slug)=$1", data: [][]driver.Value{{true}}})
	_, e := NewSyntheticRepository(db, 0).Create(context.Background(), taxonomy.Record{AuditActorID: id, Kind: taxonomy.Tag, ID: id, Slug: "tag", CreatedAt: now, UpdatedAt: now})
	var p *taxonomy.DomainError
	if !errors.As(e, &p) || p.Code != "TAG_CREATE_FAILED" || rec.events[len(rec.events)-1] != "rollback" {
		t.Fatal(e, rec.events)
	}
}
func TestRecorderMutationValidationIsAtomic(t *testing.T) {
	db, rec := database(t, step{kind: "exec", contains: "LOCK TABLE"}, step{kind: "query", contains: "FOR UPDATE", data: [][]driver.Value{recordValues()}}, step{kind: "query", contains: "FROM taxonomy_candidate.publishers", data: [][]driver.Value{{true}}}, step{kind: "query", contains: "AND is_protected", data: [][]driver.Value{{int64(1)}}})
	_, err := NewSyntheticRepository(db, 0).Mutate(context.Background(), taxonomy.Tag, "tag", taxonomy.Protect, taxonomy.Change{IsProtected: true}, func(r taxonomy.Record, state taxonomy.MutationState) (taxonomy.Record, error) {
		if !state.PublisherExists || state.ProtectedUsed != 1 {
			t.Fatal(state)
		}
		return r, taxonomy.ErrForbidden
	})
	if !errors.Is(err, taxonomy.ErrForbidden) || rec.events[len(rec.events)-1] != "rollback" {
		t.Fatal(err, rec.events)
	}
}
func TestRecorderDeleteFailurePreservesTransaction(t *testing.T) {
	db, rec := database(t, step{kind: "exec", contains: "LOCK TABLE"}, step{kind: "query", contains: "FOR UPDATE", data: [][]driver.Value{recordValues()}}, step{kind: "exec", contains: "DELETE FROM taxonomy_candidate.post_tag_links"}, step{kind: "exec", contains: "UPDATE taxonomy_candidate.post_category_subscriptions"}, step{kind: "exec", contains: "UPDATE taxonomy_candidate.post_tags SET deleted_at", err: errors.New("constraint failure")})
	_, err := NewSyntheticRepository(db, 0).Mutate(context.Background(), taxonomy.Tag, "tag", taxonomy.Delete, taxonomy.Change{}, func(r taxonomy.Record, _ taxonomy.MutationState) (taxonomy.Record, error) {
		r.AuditActorID = id
		return r, nil
	})
	if err == nil || rec.events[len(rec.events)-1] != "rollback" {
		t.Fatal(err, rec.events)
	}
}
func TestRecorderSubscriptionWindowAndIdempotency(t *testing.T) {
	db, _ := database(t, step{kind: "query", contains: "SELECT count(*)", data: [][]driver.Value{{int64(5)}}}, step{kind: "query", contains: "OFFSET $2 LIMIT $3", args: []any{id, int64(3), int64(0)}})
	rows, total, err := NewSyntheticRepository(db, 0).Subscriptions(context.Background(), id, 3, 2)
	if err != nil || rows == nil || len(rows) != 0 || total != 5 {
		t.Fatal(rows, total, err)
	}
	db, rec := database(t, step{kind: "exec", contains: "IN SHARE MODE"}, step{kind: "exec", contains: "post_category_subscriptions IN SHARE ROW EXCLUSIVE MODE"}, step{kind: "query", contains: "lower(slug)=$1", args: []any{" tag "}, data: [][]driver.Value{{id}}}, step{kind: "query", contains: "tag_id=$1", data: [][]driver.Value{{id, id, nil, id, nil}}})
	row, created, err := NewSyntheticRepository(db, 0).Subscription(context.Background(), taxonomy.Tag, " TAG ", id, "subscribe", now)
	if err != nil || created || row.ID != id || rec.events[len(rec.events)-1] != "commit" {
		t.Fatal(row, created, err, rec.events)
	}
}

func TestRecorderAuditFailureRollsBackCreate(t *testing.T) {
	db, rec := database(t, step{kind: "exec", contains: "LOCK TABLE"}, step{kind: "query", contains: "SELECT EXISTS", data: [][]driver.Value{{false}}}, step{kind: "exec", contains: "INSERT INTO taxonomy_candidate.post_categories"}, step{kind: "exec", contains: "INSERT INTO taxonomy_candidate.actions", err: errors.New("ledger unavailable")})
	_, err := NewSyntheticRepository(db, 0).Create(context.Background(), taxonomy.Record{Kind: taxonomy.Category, ID: id, Slug: "audit", AuditActorID: owner, AuditAdmin: true, CreatedAt: now, UpdatedAt: now})
	if err == nil || rec.events[len(rec.events)-1] != "rollback" {
		t.Fatal(err, rec.events)
	}
}
