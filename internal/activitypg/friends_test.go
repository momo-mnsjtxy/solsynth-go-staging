// SPDX-License-Identifier: AGPL-3.0-only
package activitypg

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/activities"
	"solsynth.local/go-migration/ports/passport/friends"
)

// This in-process SQL driver executes the real overview composition with one
// connection and controlled elapsed time/failures. It does not certify PostgreSQL
// query predicates, locking, catalog types or real bearer authentication; the
// registered TestPGFriendsOverview cases own those claims.
const overviewReader = "99999999-9999-4999-8999-999999999999"
const overviewFirst = "11111111-1111-4111-8111-111111111111"
const overviewSecond = "22222222-2222-4222-8222-222222222222"
const overviewSession = "33333333-3333-4333-8333-333333333333"
const overviewActivity = "44444444-4444-4444-8444-444444444444"
const overviewFile = "55555555-5555-4555-8555-555555555555"

type overviewProofKey struct{}
type overviewConnector struct{ c *overviewConn }

func (v overviewConnector) Connect(context.Context) (driver.Conn, error) {
	v.c.connections++
	return v.c, nil
}
func (overviewConnector) Driver() driver.Driver { return overviewDriver{} }

type overviewDriver struct{}

func (overviewDriver) Open(string) (driver.Conn, error) { return nil, errors.New("no network driver") }

type overviewRows struct {
	n    int
	data [][]driver.Value
}

func (r *overviewRows) Columns() []string { return make([]string, r.n) }
func (r *overviewRows) Close() error      { return nil }
func (r *overviewRows) Next(dst []driver.Value) error {
	if len(r.data) == 0 {
		return io.EOF
	}
	copy(dst, r.data[0])
	r.data = r.data[1:]
	return nil
}
func overviewValues(n int, data ...[]driver.Value) driver.Rows { return &overviewRows{n, data} }

type overviewTx struct{ c *overviewConn }

func (v overviewTx) Commit() error   { v.c.committed = true; return nil }
func (v overviewTx) Rollback() error { v.c.rolledBack = true; return nil }

type overviewConn struct {
	t                                              *testing.T
	now, initial                                   time.Time
	ids                                            []string
	current                                        []string
	locks, hashes                                  []string
	activity                                       activities.Activity
	recent                                         map[string]bool
	hidden                                         map[string]bool
	sourceOK                                       bool
	activitySeen, waited, committed, rolledBack    bool
	connections, checks, sourceChecks, discoveries int
	fileWaits, afterFileAt                         int
	artworkExpiry                                  map[string]time.Time
	afterFile, afterProfile                        func()
	proofError                                     error
}

func (*overviewConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (*overviewConn) Close() error                { return nil }
func (v *overviewConn) Begin() (driver.Tx, error) { return overviewTx{v}, nil }
func (v *overviewConn) BeginTx(_ context.Context, o driver.TxOptions) (driver.Tx, error) {
	if o.Isolation != driver.IsolationLevel(sql.LevelReadCommitted) {
		return nil, errors.New("wrong isolation")
	}
	return overviewTx{v}, nil
}
func (*overviewConn) CheckNamedValue(*driver.NamedValue) error { return nil }
func (v *overviewConn) ExecContext(ctx context.Context, q string, a []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch {
	case strings.Contains(q, "pg_advisory_xact_lock"):
		if len(v.locks) != len(v.ids)+1 {
			return nil, errors.New("artwork before complete account locks")
		}
		v.hashes = append(v.hashes, a[0].Value.(string))
	case strings.Contains(q, "owned_media_files") && strings.Contains(q, "FOR SHARE"):
		v.waited = true
		v.fileWaits++
		if v.afterFile != nil && (v.afterFileAt == 0 || v.fileWaits == v.afterFileAt) {
			v.afterFile()
			v.afterFile = nil
		}
	default:
		return nil, fmt.Errorf("unexpected mutation: %s", q)
	}
	return driver.RowsAffected(0), nil
}
func (v *overviewConn) QueryContext(ctx context.Context, q string, a []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	arg := func(i int) string { return a[i].Value.(string) }
	switch {
	case strings.Contains(q, "SELECT a.id::text FROM owned_authority_accounts a WHERE a.id<>"):
		v.discoveries++
		ids := v.ids
		if v.discoveries > 1 && v.current != nil {
			ids = v.current
		}
		rows := [][]driver.Value{}
		for _, id := range ids {
			rows = append(rows, []driver.Value{id})
		}
		return overviewValues(1, rows...), nil
	case strings.Contains(q, "FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE"):
		v.locks = append(v.locks, arg(0))
		return overviewValues(1, []driver.Value{arg(0)}), nil
	case strings.HasPrefix(q, "SELECT NOT a.disabled"):
		return overviewValues(1, []driver.Value{true}), nil
	case strings.Contains(q, "SELECT EXISTS(SELECT 1 FROM owned_social_effective_relationships"):
		return overviewValues(2, []driver.Value{false, true}), nil
	case strings.Contains(q, "SELECT COALESCE((SELECT status_visibility FROM"):
		visibility := "public"
		if v.hidden[arg(0)] {
			visibility = "private"
		}
		return overviewValues(2, []driver.Value{visibility, false}), nil
	case strings.Contains(q, "FROM owned_presence_activities WHERE account_id=ANY"):
		v.activitySeen = true
		act := v.activity
		if act.ID == "" {
			return overviewValues(17), nil
		}
		data, _ := json.Marshal(act.Content)
		return overviewValues(17, []driver.Value{act.ID, act.AccountID, nil, nil, data, int64(act.Visibility), overviewSession, int64(7), int64(3), int64(5), act.LeaseExpiresAt, nil, nil, v.initial, v.initial, nil, int64(1)}), nil
	case q == "SELECT clock_timestamp()":
		return overviewValues(1, []driver.Value{v.now}), nil
	case strings.HasPrefix(q, "SELECT f.id::text,f.account_id::text"):
		deadline, bounded := v.artworkExpiry[arg(0)]
		return overviewValues(3, []driver.Value{overviewFile, overviewFirst, !bounded || v.now.Before(deadline)}), nil
	case strings.HasPrefix(q, "SELECT f.state='ready'"):
		deadline, bounded := v.artworkExpiry[arg(0)]
		return overviewValues(1, []driver.Value{!bounded || v.now.Before(deadline)}), nil
	case strings.HasPrefix(q, "SELECT id::text,created_at FROM owned_authority_accounts"):
		return overviewValues(2, []driver.Value{arg(0), v.initial.Add(-48 * time.Hour)}), nil
	case strings.HasPrefix(q, "SELECT online_visibility,last_seen_visibility,status_visibility"):
		status := "public"
		if v.hidden[arg(0)] {
			status = "private"
		}
		return overviewValues(3, []driver.Value{"public", "friends", status}), nil
	case strings.Contains(q, "FROM owned_presence_statuses WHERE account_id=$1 AND"):
		if !v.hidden[arg(0)] {
			return overviewValues(12), nil
		}
		return overviewValues(12, []driver.Value{overviewActivity, arg(0), int64(0), int64(0), "secret-label", nil, nil, false, []byte(`{"private":"secret-meta"}`), nil, v.initial, v.initial}), nil
	case strings.HasPrefix(q, "SELECT count(*),count(*)FILTER"):
		return overviewValues(3, []driver.Value{int64(0), int64(0), nil}), nil
	case strings.HasPrefix(q, "SELECT last_seen_at"):
		return overviewValues(1), nil
	case strings.HasPrefix(q, "SELECT COALESCE((SELECT type=3"):
		return overviewValues(1, []driver.Value{false}), nil
	case strings.HasPrefix(q, "SELECT COALESCE((SELECT status_visibility<>"):
		return overviewValues(1, []driver.Value{v.recent[arg(0)] && !v.hidden[arg(0)]}), nil
	case strings.Contains(q, "LEFT JOIN owned_accounts_profiles"):
		if arg(0) == overviewSecond && v.afterProfile != nil {
			v.afterProfile()
			v.afterProfile = nil
		}
		return overviewValues(18, []driver.Value{arg(0), "synthetic", "Synthetic", "en", "", v.initial.Add(-48 * time.Hour), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil}), nil
	default:
		return nil, fmt.Errorf("unexpected query: %s", q)
	}
}

type overviewGuard struct {
	c  *overviewConn
	db *sql.DB
	tx *sql.Tx
}

func (g *overviewGuard) valid(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string) error {
	if db != g.db || tx == nil || actor != overviewReader || ctx.Value(overviewProofKey{}) != "original" {
		return authority.ErrInvalid
	}
	if g.tx != nil && g.tx != tx {
		return errors.New("projection left original transaction")
	}
	g.tx = tx
	return ctx.Err()
}
func (g *overviewGuard) Preflight(ctx context.Context, db *sql.DB, tx *sql.Tx, a string, _ ...string) (authority.Principal, error) {
	return authority.Principal{}, g.valid(ctx, db, tx, a)
}
func (g *overviewGuard) Authenticate(ctx context.Context, db *sql.DB, tx *sql.Tx, a string) (authority.Principal, error) {
	if e := g.valid(ctx, db, tx, a); e != nil {
		return authority.Principal{}, e
	}
	g.c.checks++
	if g.c.waited && g.c.proofError != nil {
		return authority.Principal{}, g.c.proofError
	}
	return authority.Principal{}, nil
}
func (g *overviewGuard) Authorize(ctx context.Context, db *sql.DB, tx *sql.Tx, a string, _ ...string) (authority.Principal, error) {
	return g.Authenticate(ctx, db, tx, a)
}
func (g *overviewGuard) StoredAuthenticationEligible(ctx context.Context, db *sql.DB, tx *sql.Tx, a, s string, version, epoch int64) (bool, error) {
	if db != g.db || tx != g.tx || a != overviewFirst || s != overviewSession || version != 7 || epoch != 3 {
		return false, errors.New("recorded source admission changed")
	}
	g.c.sourceChecks++
	return g.c.sourceOK, ctx.Err()
}
func newOverviewSynthetic(t *testing.T) (*overviewConn, *Repository, context.Context) {
	t.Helper()
	at := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	c := &overviewConn{t: t, initial: at, now: at, ids: []string{overviewFirst, overviewSecond}, recent: map[string]bool{overviewSecond: true}, hidden: map[string]bool{}, sourceOK: true}
	c.activity = activities.Activity{ID: overviewActivity, AccountID: overviewFirst, Visibility: activities.Public, LeaseExpiresAt: at.Add(time.Second), Content: activities.Content{Meta: json.RawMessage(`{"public":"allowed"}`)}}
	db := sql.OpenDB(overviewConnector{c})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	g := &overviewGuard{c: c, db: db}
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), overviewProofKey{}, "original"), time.Second)
	t.Cleanup(cancel)
	return c, New(db, g), ctx
}
func TestFriendsOverviewFinalLeaseFenceRestoresVisibleFallback(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprint(expired), func(t *testing.T) {
			c, r, ctx := newOverviewSynthetic(t)
			if expired {
				c.afterProfile = func() { c.now = c.now.Add(2 * time.Second) }
			}
			rows, total, e := r.Overview(ctx, overviewReader, friends.Filter{Paged: true, Take: 1})
			want := overviewFirst
			if expired {
				want = overviewSecond
			}
			if e != nil || total != 1 || len(rows) != 1 || rows[0].Account.ID != want {
				t.Fatal(rows, total, e)
			}
			if expired && len(rows[0].Activities) != 0 {
				t.Fatal("expired activity survived final fence", rows)
			}
			sorted := append([]string{}, c.locks...)
			sort.Strings(sorted)
			if !reflect.DeepEqual(c.locks, sorted) || c.connections != 1 || !c.committed || c.checks != 3 {
				t.Fatal("transaction boundary", c.locks, c.connections, c.committed, c.checks)
			}
		})
	}
}
func TestFriendsOverviewPostWaitSourceAndOriginalProof(t *testing.T) {
	for _, change := range []string{"live", "source", "reader", "cancel"} {
		t.Run(change, func(t *testing.T) {
			c, r, ctx := newOverviewSynthetic(t)
			hash := "sha256:" + strings.Repeat("a", 64)
			c.activity.LargeImage = &hash
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			c.afterFile = func() {
				switch change {
				case "source":
					c.sourceOK = false
				case "reader":
					c.proofError = authority.ErrExpired
				case "cancel":
					cancel()
				}
			}
			rows, total, e := r.Overview(ctx, overviewReader, friends.Filter{Paged: true, Take: 1})
			if !c.waited {
				t.Fatal("fixture did not reach file wait")
			}
			if change == "reader" || change == "cancel" {
				if e == nil || rows != nil || total != 0 || c.committed {
					t.Fatal("failed request exposed data", rows, total, e)
				}
				return
			}
			want := overviewFirst
			if change == "source" {
				want = overviewSecond
			}
			if e != nil || len(rows) != 1 || total != 1 || rows[0].Account.ID != want || c.sourceChecks != 1 {
				t.Fatal(rows, total, e, c.sourceChecks)
			}
		})
	}
}
func TestFriendsOverviewPrivateStatusAndCurrentFriendsBeforePage(t *testing.T) {
	for _, mode := range []string{"private", "removed", "offset"} {
		t.Run(mode, func(t *testing.T) {
			c, r, ctx := newOverviewSynthetic(t)
			c.activity = activities.Activity{}
			c.recent[overviewFirst] = true
			switch mode {
			case "private":
				c.hidden[overviewFirst] = true
			case "removed":
				c.current = []string{overviewSecond}
			}
			f := friends.Filter{Take: 1, Paged: true, IncludeOffline: true}
			if mode == "offset" {
				f.Offset = 1
			}
			rows, total, e := r.Overview(ctx, overviewReader, f)
			expectedTotal := 1
			if mode == "offset" {
				expectedTotal = 2
			}
			data, _ := json.Marshal(rows)
			if e != nil || total != expectedTotal || len(rows) != 1 || rows[0].Account.ID != overviewSecond || strings.Contains(string(data), "secret-") {
				t.Fatal(string(data), total, e)
			}
			if rows[0].Account.Profile == nil || rows[0].Activities == nil {
				t.Fatal("authored nested wire shape changed", rows)
			}
		})
	}
}

func TestFriendsOverviewEarlierArtworkExpiresDuringLaterWait(t *testing.T) {
	c, r, ctx := newOverviewSynthetic(t)
	first, second := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
	c.activity.LargeImage, c.activity.SmallImage = &first, &second
	c.activity.LeaseExpiresAt = c.now.Add(time.Hour)
	c.artworkExpiry = map[string]time.Time{first: c.now.Add(time.Second)}
	c.afterFileAt = 2
	c.afterFile = func() { c.now = c.now.Add(2 * time.Second) }
	rows, total, e := r.Overview(ctx, overviewReader, friends.Filter{Take: 1, Paged: true})
	if e != nil || len(rows) != 1 || total != 1 || len(rows[0].Activities) != 1 {
		t.Fatal(rows, total, e)
	}
	activity := rows[0].Activities[0]
	if activity.LargeImage != nil || activity.SmallImage == nil || *activity.SmallImage != second {
		t.Fatal("reference survived later retention wait", activity)
	}
	if len(c.hashes) < 2 || c.hashes[0] != first || c.hashes[1] != second {
		t.Fatal("full sorted associations did not precede files", c.hashes)
	}
}
