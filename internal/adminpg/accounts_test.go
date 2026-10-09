// SPDX-License-Identifier: AGPL-3.0-only
package adminpg

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	a "solsynth.local/go-migration/ports/admin/accounts"
	c "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
)

const adminActor = "b1000000-0000-4000-8000-000000000001"
const adminTarget = "a1000000-0000-4000-8000-000000000002"
const adminSession = "c1000000-0000-4000-8000-000000000003"
const adminParent = "c1000000-0000-4000-8000-000000000004"

// This socket-free SQL boundary fixture uses the real signature verifier,
// transaction guard and administrative repository. It proves query ordering,
// redaction and proof freshness; it does not simulate PostgreSQL locks or DDL.
type adminBoundary struct {
	now                          time.Time
	superuser, disabled          bool
	parent, client               string
	sessionType                  int64
	epoch                        int64
	scopes                       []string
	grants                       []authority.Rule
	revoked                      *time.Time
	locks                        []string
	observations, reads, commits int
	target                       string
	afterLocks, afterRead        func()
}
type adminConnector struct{ state *adminBoundary }

func (c adminConnector) Connect(context.Context) (driver.Conn, error) {
	return &adminConn{state: c.state}, nil
}
func (adminConnector) Driver() driver.Driver { return adminDriver{} }

type adminDriver struct{}

func (adminDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("socket access forbidden")
}

type adminConn struct{ state *adminBoundary }

func (*adminConn) Prepare(string) (driver.Stmt, error)                            { return nil, errors.New("unexpected prepare") }
func (*adminConn) Close() error                                                   { return nil }
func (c *adminConn) Begin() (driver.Tx, error)                                    { return adminTx{c.state}, nil }
func (c *adminConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) { return c.Begin() }

type adminTx struct{ state *adminBoundary }

func (t adminTx) Commit() error { t.state.commits++; return nil }
func (adminTx) Rollback() error { return nil }

type adminRows struct {
	columns int
	rows    [][]driver.Value
}

func (r *adminRows) Columns() []string { return make([]string, r.columns) }
func (*adminRows) Close() error        { return nil }
func (r *adminRows) Next(dest []driver.Value) error {
	if len(r.rows) == 0 {
		return io.EOF
	}
	copy(dest, r.rows[0])
	r.rows = r.rows[1:]
	return nil
}
func adminRow(v ...driver.Value) driver.Rows {
	return &adminRows{columns: len(v), rows: [][]driver.Value{v}}
}
func (c *adminConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.state
	switch {
	case q == `SHOW transaction_isolation`:
		return adminRow("read committed"), nil
	case strings.HasPrefix(q, `SELECT id::text,name,nick,language,region,is_superuser`):
		if len(args) != 1 || args[0].Value != adminActor {
			return nil, fmt.Errorf("unexpected authority account")
		}
		return adminRow(adminActor, "synthetic_actor", "Synthetic", "", "", s.superuser, s.disabled, int64(1), s.now.Add(-time.Hour)), nil
	case strings.HasPrefix(q, `SELECT id::text,account_id::text,COALESCE(parent_id::text`):
		if len(args) != 2 || args[0].Value != adminActor || args[1].Value != adminSession {
			return nil, fmt.Errorf("unexpected authority session")
		}
		scopes, _ := json.Marshal(s.scopes)
		var revoked driver.Value
		if s.revoked != nil {
			revoked = *s.revoked
		}
		return adminRow(adminSession, adminActor, s.parent, "synthetic", s.sessionType, s.client, s.now.Add(-time.Hour), s.now.Add(-time.Hour), "device", false, s.epoch, int64(1), int64(1), s.now.Add(time.Hour), revoked, scopes, nil, nil, nil, nil), nil
	case strings.HasPrefix(q, `SELECT kind,pattern,starts_at,expires_at FROM owned_authority_punishments`):
		return &adminRows{columns: 4}, nil
	case strings.HasPrefix(q, `SELECT pattern,deny,starts_at,expires_at FROM owned_authority_grants`):
		rows := &adminRows{columns: 4}
		for _, g := range s.grants {
			var start, end driver.Value
			if g.StartsAt != nil {
				start = *g.StartsAt
			}
			if g.ExpiresAt != nil {
				end = *g.ExpiresAt
			}
			rows.rows = append(rows.rows, []driver.Value{g.Pattern, g.Deny, start, end})
		}
		return rows, nil
	case q == `SELECT id::text,id::text,'' FROM owned_authority_accounts WHERE lower(name)=lower($1::text)`:
		s.observations++
		if args[0].Value == "missing" {
			return &adminRows{columns: 3}, nil
		}
		return adminRow(s.target, s.target, ""), nil
	case q == `SELECT id::text FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`:
		s.locks = append(s.locks, args[0].Value.(string))
		if len(s.locks) == 2 && s.afterLocks != nil {
			s.afterLocks()
		}
		return adminRow(args[0].Value), nil
	case q == `SELECT id::text,type,secret IS NOT NULL AND octet_length(secret)>0,created_at,enabled_at FROM owned_authority_factors WHERE account_id=$1 ORDER BY type,id`:
		if len(args) != 1 || args[0].Value != adminTarget {
			return nil, fmt.Errorf("foreign factor query")
		}
		s.reads++
		if s.afterRead != nil {
			s.afterRead()
		}
		return &adminRows{columns: 5, rows: [][]driver.Value{{adminTarget, int64(0), true, s.now, s.now}, {adminParent, int64(5), true, s.now, nil}}}, nil
	default:
		// Selecting raw secret/config columns is intentionally unsupported.
		return nil, fmt.Errorf("unexpected administrative SQL: %s", q)
	}
}

var adminKeyOnce sync.Once
var adminKey *rsa.PrivateKey

func newAdminBoundary(t *testing.T, prepare func(*adminBoundary)) (*Repository, *adminBoundary, context.Context) {
	t.Helper()
	adminKeyOnce.Do(func() {
		var e error
		adminKey, e = rsa.GenerateKey(rand.Reader, 2048)
		if e != nil {
			panic(e)
		}
	})
	s := &adminBoundary{now: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC), superuser: true, epoch: 1, scopes: []string{"*"}, grants: []authority.Rule{{Pattern: "*"}}, target: adminTarget}
	if prepare != nil {
		prepare(s)
	}
	db := sql.OpenDB(adminConnector{s})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	store := authoritypg.New(db)
	verifier, e := authority.NewVerifier(store, authority.Config{Issuer: "https://synthetic.invalid", Audience: "synthetic-admin", TrustedKeys: map[string]*rsa.PublicKey{"synthetic": &adminKey.PublicKey}, Now: func() time.Time { return s.now }})
	if e != nil {
		t.Fatal(e)
	}
	header, _ := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT", "kid": "synthetic"})
	claims, _ := json.Marshal(map[string]any{"iss": "https://synthetic.invalid", "aud": "synthetic-admin", "sub": adminActor, "jti": adminSession, "sid": adminSession, "token_use": "user", "ver": 1, "epoch": 1, "exp": s.now.Add(10 * time.Minute).Unix(), "iat": s.now.Unix(), "nbf": s.now.Unix()})
	raw := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(raw))
	signature, e := rsa.SignPKCS1v15(rand.Reader, adminKey, crypto.SHA256, sum[:])
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	p, e := verifier.Authenticate(ctx, raw+"."+base64.RawURLEncoding.EncodeToString(signature))
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(store, verifier)
	repo, e := New(db, guard, func() time.Time { return s.now })
	if e != nil {
		t.Fatal(e)
	}
	s.commits = 0
	return repo, s, guard.Bind(ctx, p)
}

func TestAccountAdminRootBoundaryPrecedesTargetObservation(t *testing.T) {
	for _, kind := range []string{"child", "nonglobal", "grantless", "denied", "scope"} {
		for _, ref := range []string{"synthetic_target", "missing"} {
			t.Run(kind+"/"+ref, func(t *testing.T) {
				repo, s, ctx := newAdminBoundary(t, func(s *adminBoundary) {
					switch kind {
					case "child":
						s.parent = adminParent
					case "nonglobal":
						s.superuser = false
					case "grantless":
						s.grants = nil
					case "denied":
						s.grants = append(s.grants, authority.Rule{Pattern: a.ViewPermission, Deny: true})
					case "scope":
						s.scopes = []string{a.BadgePermission}
					}
				})
				_, e := repo.AccountFactors(ctx, adminActor, ref)
				if !errors.Is(e, c.ErrForbidden) && !errors.Is(e, authority.ErrForbidden) {
					t.Fatalf("unauthorized read: %v", e)
				}
				if s.observations != 0 || len(s.locks) != 0 || s.reads != 0 || s.commits != 0 {
					t.Fatalf("private target observed: %+v", s)
				}
			})
		}
	}
}
func TestAccountAdminFreshProofAfterSortedAccountLocks(t *testing.T) {
	for _, kind := range []string{"child", "revoked", "grant", "expired-grant", "scope-aba", "global", "target-reassigned"} {
		t.Run(kind, func(t *testing.T) {
			repo, s, ctx := newAdminBoundary(t, nil)
			s.afterLocks = func() {
				switch kind {
				case "child":
					s.parent = adminParent
				case "revoked":
					at := s.now
					s.revoked = &at
				case "grant":
					s.grants = nil
				case "expired-grant":
					at := s.now
					s.grants = []authority.Rule{{Pattern: "*", ExpiresAt: &at}}
				case "scope-aba":
					s.epoch += 2 // canonical narrowing/restoration invalidates both previous tokens
				case "global":
					s.superuser = false
				case "target-reassigned":
					s.target = adminParent
				}
			}
			if _, e := repo.AccountFactors(ctx, adminActor, "synthetic_target"); e == nil {
				t.Fatal("stale authority/target accepted")
			}
			if strings.Join(s.locks, ",") != adminTarget+","+adminActor {
				t.Fatal("account locks not globally sorted", s.locks)
			}
			if s.reads != 0 || s.commits != 0 {
				t.Fatal("read or commit escaped failed recheck")
			}
		})
	}
}
func TestAccountAdminFactorProjectionSingleConnectionAndFinalProof(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(fmt.Sprint(expire), func(t *testing.T) {
			repo, s, ctx := newAdminBoundary(t, nil)
			if expire {
				s.afterRead = func() { s.now = s.now.Add(11 * time.Minute) }
			}
			got, e := repo.AccountFactors(ctx, adminActor, "synthetic_target")
			if expire {
				if !errors.Is(e, authority.ErrExpired) || got != nil || s.commits != 0 {
					t.Fatal("expired proof released private data", got, e)
				}
				return
			}
			if e != nil || len(got) != 2 || !got[0].HasSecret || got[1].Type != "unspecified" || s.commits != 1 {
				t.Fatal(got, e)
			}
			raw, e := json.Marshal(got)
			if e != nil {
				t.Fatal(e)
			}
			for _, bad := range []string{`"secret":`, `"last_counter":`, `"revision":`, `"pending_until":`} {
				if strings.Contains(string(raw), bad) {
					t.Fatal("factor material exposed", string(raw))
				}
			}
			for _, f := range got {
				if f.Config != nil || f.UpdatedAt != nil || f.ExpiredAt != nil {
					t.Fatal("invented sensitive metadata", f)
				}
			}
		})
	}
}
