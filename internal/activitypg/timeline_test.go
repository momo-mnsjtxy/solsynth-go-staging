// SPDX-License-Identifier: AGPL-3.0-only
package activitypg

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"reflect"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"testing"
	"time"

	"solsynth.local/go-migration/ports/passport/activities"
	"solsynth.local/go-migration/ports/passport/history"
)

// Reuses the one-connection, no-network SQL fixture to execute real timeline
// composition. PostgreSQL predicates remain covered by the guarded PG cases.
type timelineConnector struct{ c *timelineConn }

func (v timelineConnector) Connect(context.Context) (driver.Conn, error) {
	v.c.connections++
	return v.c, nil
}
func (timelineConnector) Driver() driver.Driver { return overviewDriver{} }

type timelineConn struct {
	*overviewConn
	extra []activities.Activity
}

func (c *timelineConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.HasPrefix(q, "SELECT id::text FROM owned_authority_accounts WHERE id::text="):
		return overviewValues(1, []driver.Value{overviewFirst}), nil
	case strings.HasPrefix(q, "WITH candidates AS("):
		if len(args) != 7 {
			return nil, errors.New("historical timeline acquired a source-session predicate")
		}
		page := [][]driver.Value{}
		for _, a := range append([]activities.Activity{c.activity}, c.extra...) {
			page = append(page, []driver.Value{int64(1 + len(c.extra)), a.ID, c.initial, int64(1)})
		}
		return overviewValues(4, page...), nil
	case strings.Contains(q, "FROM owned_presence_activities WHERE id=$1::uuid"):
		for _, a := range append([]activities.Activity{c.activity}, c.extra...) {
			if a.ID != args[0].Value.(string) {
				continue
			}
			data, _ := json.Marshal(a.Content)
			return overviewValues(17, []driver.Value{a.ID, overviewFirst, "private-manual", nil, data, int64(a.Visibility), overviewSession, int64(7), int64(3), int64(5), a.LeaseExpiresAt, nil, nil, c.initial, c.initial, nil, int64(1)}), nil
		}
		return nil, errors.New("unexpected activity id")
	}
	return c.overviewConn.QueryContext(ctx, q, args)
}
func newTimelineSynthetic(t *testing.T) (*timelineConn, *Repository, context.Context) {
	t.Helper()
	base, _, ctx := newOverviewSynthetic(t)
	base.ids = []string{overviewFirst}
	c := &timelineConn{overviewConn: base}
	db := sql.OpenDB(timelineConnector{c})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	g := &overviewGuard{c: base, db: db}
	return c, New(db, g), ctx
}
func TestTimelinePreservesPublicHistoryAcrossSourceLogout(t *testing.T) {
	c, r, ctx := newTimelineSynthetic(t)
	c.sourceOK = false
	c.activity.LeaseExpiresAt = c.now.Add(-time.Hour)
	rows, total, e := r.Timeline(ctx, overviewReader, overviewFirst, history.Filter{Take: 20})
	if e != nil || total != 1 || len(rows) != 1 || !c.committed {
		t.Fatal(rows, total, e)
	}
	if c.sourceChecks != 0 {
		t.Fatal("historical row treated as a live source admission")
	}
	if rows[0].Activity.ManualID != nil || string(rows[0].Activity.Meta) != "null" {
		t.Fatal("public historical metadata leaked", rows)
	}
}
func TestTimelineArtworkWaitRechecksCurrentRights(t *testing.T) {
	for _, change := range []string{"reader", "privacy", "source"} {
		t.Run(change, func(t *testing.T) {
			c, r, ctx := newTimelineSynthetic(t)
			hash := "sha256:" + strings.Repeat("a", 64)
			c.activity.LargeImage = &hash
			c.afterFile = func() {
				switch change {
				case "reader":
					c.proofError = authority.ErrInvalid
				case "privacy":
					c.hidden[overviewFirst] = true
				case "source":
					c.sourceOK = false
				}
			}
			rows, total, e := r.Timeline(ctx, overviewReader, overviewFirst, history.Filter{Take: 20})
			if change == "source" {
				if e != nil || total != 1 || len(rows) != 1 || !c.committed || c.sourceChecks != 0 {
					t.Fatal("logout during wait erased history", rows, total, e)
				}
			} else if e == nil || total != 0 || len(rows) != 0 || c.committed {
				t.Fatal("current rights withdrawal leaked history", change, rows, total, e)
			}
		})
	}
}
func TestTimelineLaterArtworkWaitRedactsExpiredReference(t *testing.T) {
	c, r, ctx := newTimelineSynthetic(t)
	first, last := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("z", 64)
	c.activity.LargeImage = &first
	second := c.activity
	second.ID = "66666666-6666-4666-8666-666666666666"
	second.LargeImage = &last
	c.extra = []activities.Activity{second}
	c.artworkExpiry = map[string]time.Time{first: c.now.Add(time.Second)}
	c.afterFileAt = 2
	c.afterFile = func() { c.now = c.now.Add(2 * time.Second) }
	rows, total, e := r.Timeline(ctx, overviewReader, overviewFirst, history.Filter{Take: 20})
	if e != nil || total != 2 || len(rows) != 2 || rows[0].Activity.LargeImage != nil || rows[1].Activity.LargeImage == nil {
		t.Fatal("stale artwork reference after later wait", rows, total, e)
	}
}
func TestTimelineLocksAllArtworkBeforeFiles(t *testing.T) {
	c, r, ctx := newTimelineSynthetic(t)
	first, last := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("z", 64)
	c.activity.LargeImage = &last
	second := c.activity
	second.ID = "66666666-6666-4666-8666-666666666666"
	second.LargeImage = &first
	c.extra = []activities.Activity{second}
	c.afterFile = func() {
		if len(c.hashes) < 2 || !reflect.DeepEqual(c.hashes[:2], []string{first, last}) {
			t.Error("file wait preceded ordered full associations", c.hashes)
		}
	}
	c.activity.LeaseExpiresAt = c.now.Add(-time.Hour) // History retains expired activity leases.
	rows, total, e := r.Timeline(ctx, overviewReader, overviewFirst, history.Filter{Take: 20})
	if e != nil || len(rows) != 2 || total != 2 || !c.waited {
		t.Fatal(rows, total, e)
	}
}
