// SPDX-License-Identifier: AGPL-3.0-only
package federationpg

import (
	"context"
	"crypto"
	"crypto/rsa"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"math/big"
	f "solsynth.local/go-migration/ports/federation"
	"strings"
	"testing"
	"time"
)

// This driver exercises provider fallback decisions only. Catalogs, constraints,
// concurrency and the reservation predicates require the registered PG cases.
type admissionLedgerConnector struct{ conn *admissionLedgerConn }

func (c admissionLedgerConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c admissionLedgerConnector) Driver() driver.Driver                        { return admissionLedgerDriver{} }

type admissionLedgerDriver struct{}

func (admissionLedgerDriver) Open(string) (driver.Conn, error) { return nil, errors.New("no sockets") }

type admissionLedgerConn struct {
	t            *testing.T
	queries      int
	reserved     bool
	failure      error
	key          f.RemoteKey
	probe        bool
	ledger       bool
	probeFailure error
}

func (*admissionLedgerConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (*admissionLedgerConn) Close() error { return nil }
func (*admissionLedgerConn) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected transaction")
}
func (c *admissionLedgerConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.queries++
	step := c.queries
	if c.probe {
		if step == 1 {
			if query != `SELECT to_regclass('owned_federation_public_keys') IS NOT NULL` || len(args) != 0 {
				c.t.Fatal("expected read-only optional-schema probe", query, args)
			}
			if c.probeFailure != nil {
				return nil, c.probeFailure
			}
			return &admissionLedgerRows{values: []driver.Value{c.ledger}}, nil
		}
		step--
	}
	if step == 1 {
		if !strings.Contains(query, "FROM owned_federation_public_keys WHERE ") || len(args) != 1 {
			c.t.Fatal("expected initial durable lookup", query, args)
		}
		return &admissionLedgerRows{}, nil
	}
	fingerprint, _ := f.KeyFingerprint(c.key.PublicKey)
	if step != 2 || !strings.Contains(query, "actor_uri=$1 OR key_id=$2 OR fingerprint=$3") || len(args) != 3 || args[0].Value != c.key.Actor || args[1].Value != c.key.KeyID || args[2].Value != fingerprint {
		c.t.Fatal("fallback did not check all durable reservations", query, args)
	}
	if c.failure != nil {
		return nil, c.failure
	}
	return &admissionLedgerRows{values: []driver.Value{c.reserved}}, nil
}

type admissionLedgerRows struct{ values []driver.Value }

func (*admissionLedgerRows) Columns() []string { return []string{"reserved"} }
func (*admissionLedgerRows) Close() error      { return nil }
func (r *admissionLedgerRows) Next(dest []driver.Value) error {
	if r.values == nil {
		return io.EOF
	}
	copy(dest, r.values)
	r.values = nil
	return nil
}

type admissionPinnedKeys struct{ key f.RemoteKey }

func (admissionPinnedKeys) LocalKey(context.Context, string) (crypto.Signer, error) {
	return nil, f.ErrUnavailable
}
func (p admissionPinnedKeys) RemoteKey(context.Context, string) (f.RemoteKey, error) {
	return p.key, nil
}
func (p admissionPinnedKeys) RemoteActor(context.Context, string) (f.RemoteKey, error) {
	return p.key, nil
}

func TestPublicKeyFallbackHonorsAllDurableReservations(t *testing.T) {
	for _, byKey := range []bool{false, true} {
		for _, disabled := range []bool{false, true} {
			for _, scenario := range []string{"unreserved", "reserved", "ledger-unavailable"} {
				t.Run(scenario+map[bool]string{false: "/actor", true: "/key"}[byKey]+map[bool]string{false: "/enabled", true: "/disabled"}[disabled], func(t *testing.T) {
					key := f.RemoteKey{Actor: "https://remote.example.test/alias", Inbox: "https://remote.example.test/inbox", KeyID: "https://remote.example.test/alias#replacement", PublicKey: &rsa.PublicKey{N: new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 2047), big.NewInt(31337)), E: 65537}}
					conn := &admissionLedgerConn{t: t, key: key, reserved: scenario == "reserved", probe: disabled, ledger: true}
					if scenario == "ledger-unavailable" {
						conn.failure = errors.New("synthetic ledger unavailable")
					}
					db := sql.OpenDB(admissionLedgerConnector{conn})
					defer db.Close()
					r := &Repository{db: db, config: f.Config{Origin: "https://local.example.test", AllowedOrigins: []string{"https://remote.example.test"}, MaxAttempts: 3, DeliveryTimeout: time.Second}, pinnedKeys: admissionPinnedKeys{key}}
					if !disabled {
						r.publicPolicies = map[string]f.PublicAdmissionPolicy{"https://remote.example.test": {Origin: "https://remote.example.test"}}
					}
					id := key.Actor
					if byKey {
						id = key.KeyID
					}
					got, e := (publicKeys{r}).lookup(context.Background(), id, byKey)
					if scenario == "unreserved" {
						if e != nil || got.Actor != key.Actor {
							t.Fatal(got, e)
						}
						got.PublicKey.N.SetInt64(1)
						if key.PublicKey.N.BitLen() != 2048 {
							t.Fatal("fallback exposed mutable provider key")
						}
					} else if e == nil || got.Actor != "" || got.PublicKey != nil {
						t.Fatal("durable failure granted fallback authority", got, e)
					}
					want := 2
					if disabled {
						want++
					}
					if conn.queries != want {
						t.Fatal("missing reservation read", conn.queries)
					}
				})
			}
		}
	}
}

func TestDisabledAdmissionSchemaAbsenceIsNotCachedOrConfusedWithFailure(t *testing.T) {
	key := f.RemoteKey{Actor: "https://remote.example.test/actor", Inbox: "https://remote.example.test/inbox", KeyID: "https://remote.example.test/actor#key", PublicKey: &rsa.PublicKey{N: new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 2047), big.NewInt(31337)), E: 65537}}
	conn := &admissionLedgerConn{t: t, key: key, probe: true}
	db := sql.OpenDB(admissionLedgerConnector{conn})
	defer db.Close()
	r, e := New(db, f.Config{Origin: "https://local.example.test", AllowedOrigins: []string{"https://remote.example.test"}, MaxAttempts: 3, DeliveryTimeout: time.Second}, admissionPinnedKeys{key}, nil)
	if e != nil || conn.queries != 0 {
		t.Fatal("constructor performed I/O", e, conn.queries)
	}
	if got, e := r.Keys().RemoteKey(context.Background(), key.KeyID); e != nil || got.KeyID != key.KeyID || conn.queries != 1 {
		t.Fatal("legacy static lookup without optional schema", got, e, conn.queries)
	}
	conn.queries = 0
	conn.ledger = true
	conn.reserved = true
	if _, e := r.remote(context.Background(), key.Actor); !errors.Is(e, f.ErrForbidden) || conn.queries != 3 {
		t.Fatal("cached absence bypassed newly present ledger", e, conn.queries)
	}
	conn.queries = 0
	conn.probeFailure = errors.New("synthetic schema visibility failure")
	if _, e := r.Keys().RemoteKey(context.Background(), key.KeyID); !errors.Is(e, conn.probeFailure) || conn.queries != 1 {
		t.Fatal("schema query error treated as absence", e, conn.queries)
	}
}
