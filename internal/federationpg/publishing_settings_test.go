// SPDX-License-Identifier: AGPL-3.0-only
package federationpg

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	f "solsynth.local/go-migration/ports/federation"
)

// This read-only fixture exercises the actual adapter and canonical identity
// validator without a database or key provider. Real SQL predicates and locks
// remain covered by the registered PostgreSQL integration case.
type publishingConnector struct{ conn *publishingConn }

func (c publishingConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (publishingConnector) Driver() driver.Driver                          { return admissionLedgerDriver{} }

type publishingConn struct {
	t       *testing.T
	p       publisher
	queries int
	fail    error
	absent  bool
}

func (*publishingConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("publishing settings must only read")
}
func (*publishingConn) Close() error              { return nil }
func (*publishingConn) Begin() (driver.Tx, error) { return admissionLedgerTx{}, nil }
func (c *publishingConn) QueryContext(_ context.Context, q string, a []driver.NamedValue) (driver.Rows, error) {
	c.queries++
	if len(a) != 1 || a[0].Value != c.p.id {
		c.t.Fatal("unexpected publisher selection", a)
	}
	if c.queries == 1 {
		if q != `SELECT publisher_id::text FROM owned_federation_publishers WHERE publisher_id=$1 FOR SHARE` {
			c.t.Fatal("missing federation-row lock", q)
		}
		if c.fail != nil {
			return nil, c.fail
		}
		return &publishingRows{values: []driver.Value{c.p.id}}, nil
	}
	if c.queries != 2 || !strings.Contains(q, "p.type=0 AND p.deleted_at IS NULL AND p.shadowbanned_at IS NULL AND NOT a.disabled") {
		c.t.Fatal("not using canonical local actor eligibility", q)
	}
	if c.absent {
		return &publishingRows{count: 12}, nil
	}
	p := c.p
	return &publishingRows{values: []driver.Value{p.id, p.name, p.nick, p.bio, p.owner, p.created, p.enabled, p.manual, p.identityHandle, p.actorURI, p.keyID, p.registered}}, nil
}

type publishingRows struct {
	values []driver.Value
	count  int
}

func (r *publishingRows) Columns() []string {
	n := r.count
	if n == 0 {
		n = len(r.values)
	}
	return make([]string, n)
}
func (*publishingRows) Close() error { return nil }
func (r *publishingRows) Next(dst []driver.Value) error {
	if r.values == nil {
		return io.EOF
	}
	copy(dst, r.values)
	r.values = nil
	return nil
}

func TestPublishingDefaultRequiresCanonicalEnabledLocalActor(t *testing.T) {
	for _, mode := range []string{"valid", "display-rename", "disabled", "other-origin", "other-anchor", "other-key", "unregistered", "unavailable-actor", "storage-error"} {
		t.Run(mode, func(t *testing.T) {
			p := publisher{id: "00000000-0000-4000-8000-000000000001", owner: "00000000-0000-4000-8000-000000000002", name: "original", identityHandle: "original", created: time.Unix(1, 0), enabled: true, registered: true, actorURI: "https://local.example.test/activitypub/actors/original", keyID: "https://local.example.test/activitypub/actors/original#main-key"}
			conn := &publishingConn{t: t, p: p}
			switch mode {
			case "display-rename":
				conn.p.name = "renamed"
			case "disabled":
				conn.p.enabled = false
			case "other-origin":
				conn.p.actorURI = "https://other.example.test/activitypub/actors/original"
				conn.p.keyID = conn.p.actorURI + "#main-key"
			case "other-anchor":
				conn.p.identityHandle = "different"
			case "other-key":
				conn.p.keyID = conn.p.actorURI + "#different-key"
			case "unregistered":
				conn.p.registered = false
			case "unavailable-actor":
				conn.absent = true
			case "storage-error":
				conn.fail = errors.New("synthetic unavailable ledger")
			}
			db := sql.OpenDB(publishingConnector{conn})
			defer db.Close()
			tx, e := db.Begin()
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback()
			r := &Repository{db: db, config: f.Config{Origin: "https://local.example.test"}}
			got, e := r.PublishingActorEnabled(context.Background(), tx, p.id)
			want := mode == "valid" || mode == "display-rename"
			if got != want || !errors.Is(e, conn.fail) {
				t.Fatal(got, e)
			}
		})
	}
}
