// SPDX-License-Identifier: AGPL-3.0-only
package transactionguard_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/boardpg"
	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/internal/taxonomypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/taxonomy"
)

const actor = "a1000000-0000-4000-8000-000000000001"
const target = "a1000000-0000-4000-8000-000000000002"

var blocked = errors.New("synthetic authority denied")

// This driver supports transaction lifecycle only. Any domain query, lock or
// mutation before the denied authority guard fails the test. It opens no socket.
type sentinel struct{ begins, rollbacks, commits, statements int }
type connector struct{ state *sentinel }

func (c connector) Connect(context.Context) (driver.Conn, error) { return connection{c.state}, nil }
func (c connector) Driver() driver.Driver                        { return c }
func (c connector) Open(string) (driver.Conn, error)             { return connection{c.state}, nil }

type connection struct{ state *sentinel }

func (c connection) Close() error { return nil }
func (c connection) Prepare(string) (driver.Stmt, error) {
	c.state.statements++
	return nil, errors.New("domain SQL preceded authority")
}
func (c connection) Begin() (driver.Tx, error) { c.state.begins++; return transaction{c.state}, nil }
func (c connection) BeginTx(_ context.Context, o driver.TxOptions) (driver.Tx, error) {
	if o.ReadOnly || o.Isolation != driver.IsolationLevel(sql.LevelReadCommitted) {
		return nil, errors.New("wrong mutation isolation")
	}
	return c.Begin()
}

type transaction struct{ state *sentinel }

func (t transaction) Commit() error   { t.state.commits++; return nil }
func (t transaction) Rollback() error { t.state.rollbacks++; return nil }

type denyGuard struct {
	t                 *testing.T
	db                *sql.DB
	actor, permission string
	calls             int
}

func (g *denyGuard) check(db *sql.DB, tx *sql.Tx, id string, permissions []string) (authority.Principal, error) {
	g.t.Helper()
	g.calls++
	if db != g.db || tx == nil || id != g.actor {
		g.t.Fatalf("wrong transaction or actor %q", id)
	}
	if g.permission == "" {
		if len(permissions) != 0 {
			g.t.Fatalf("authentication-only route got %v", permissions)
		}
	} else if len(permissions) != 1 || permissions[0] != g.permission {
		g.t.Fatalf("permissions=%v want=%s", permissions, g.permission)
	}
	return authority.Principal{}, blocked
}
func (g *denyGuard) Authenticate(_ context.Context, db *sql.DB, tx *sql.Tx, id string) (authority.Principal, error) {
	return g.check(db, tx, id, nil)
}
func (g *denyGuard) Authorize(_ context.Context, db *sql.DB, tx *sql.Tx, id string, p ...string) (authority.Principal, error) {
	return g.check(db, tx, id, p)
}
func harness(t *testing.T, id, permission string, run func(*sql.DB, *denyGuard) error) {
	t.Helper()
	state := &sentinel{}
	db := sql.OpenDB(connector{state})
	defer db.Close()
	guard := &denyGuard{t: t, db: db, actor: id, permission: permission}
	err := run(db, guard)
	if !errors.Is(err, blocked) {
		t.Fatalf("guard denial lost: %v", err)
	}
	if guard.calls != 1 || state.begins != 1 || state.rollbacks != 1 || state.commits != 0 || state.statements != 0 {
		t.Fatalf("guard=%d transaction=%+v", guard.calls, state)
	}
}
func ptr(s string) *string { return &s }

func TestAccountMutationsCheckOriginalActorBeforeDomainSQL(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name, permission string
		run              func(*accountpg.Repository) error
	}{
		{"profile", "accounts.manage", func(r *accountpg.Repository) error {
			_, e := r.UpdateProfile(ctx, actor, accounts.ProfilePatch{Bio: ptr("Synthetic")})
			return e
		}},
		{"create publisher", "publishers.create", func(r *accountpg.Repository) error {
			_, e := r.CreatePublisher(ctx, actor, accounts.PublisherPatch{Name: ptr("synthetic-publisher"), Nick: ptr("Synthetic")})
			return e
		}},
		{"update publisher", "publishers.update", func(r *accountpg.Repository) error {
			_, e := r.UpdatePublisher(ctx, actor, target, accounts.PublisherPatch{Nick: ptr("Synthetic")})
			return e
		}},
		{"delete publisher", "publishers.delete", func(r *accountpg.Repository) error { return r.DeletePublisher(ctx, actor, target) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			harness(t, actor, c.permission, func(db *sql.DB, g *denyGuard) error { return c.run(accountpg.NewWithAuthority(db, g)) })
		})
	}
	for _, c := range []struct {
		action     accounts.Mutation
		permission string
	}{{accounts.InviteMember, "publishers.invites.manage"}, {accounts.AcceptInvite, "publishers.invites.manage"}, {accounts.DeclineInvite, "publishers.invites.manage"}, {accounts.RemoveMember, "publishers.members.manage"}, {accounts.UpdateRole, "publishers.members.manage"}, {accounts.LeavePublisher, ""}} {
		t.Run(string(c.action), func(t *testing.T) {
			harness(t, actor, c.permission, func(db *sql.DB, g *denyGuard) error {
				_, e := accountpg.NewWithAuthority(db, g).ChangeMember(ctx, actor, target, accounts.MemberChange{Action: c.action, AccountID: target, Role: accounts.Editor})
				return e
			})
		})
	}
}
func TestBoardWritesCheckOriginalActorBeforeBoardLock(t *testing.T) {
	harness(t, actor, "accounts.profile.board", func(db *sql.DB, g *denyGuard) error {
		return boardpg.NewWithAuthority(db, g, nil).Replace(context.Background(), actor, nil)
	})
}

var policy = rooms.Policy{NoneEncryptionMode: 0, NotifyAll: 0, NotifyMentions: 1, NotifyNone: 2}

func TestChatMutationsCheckOriginalActorBeforeRoomLock(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name, permission string
		run              func(*chatpg.Repository) error
	}{
		{"create", "chat.create", func(r *chatpg.Repository) error {
			_, e := r.Create(ctx, actor, rooms.RoomPatch{Name: ptr("Synthetic")})
			return e
		}},
		{"update", "chat.update", func(r *chatpg.Repository) error {
			_, e := r.Update(ctx, actor, target, rooms.RoomPatch{Name: ptr("Synthetic")})
			return e
		}},
		{"delete", "chat.delete", func(r *chatpg.Repository) error { return r.Delete(ctx, actor, target) }},
		{"send", "chat.messages.create", func(r *chatpg.Repository) error {
			_, e := r.Send(ctx, actor, target, rooms.TextRequest{Content: "Synthetic"})
			return e
		}},
		{"edit", "chat.messages.update", func(r *chatpg.Repository) error {
			_, e := r.Edit(ctx, actor, target, target, rooms.TextRequest{Content: "Synthetic"})
			return e
		}},
		{"delete message", "chat.messages.delete", func(r *chatpg.Repository) error { return r.DeleteMessage(ctx, actor, target, target) }},
		{"notify", "", func(r *chatpg.Repository) error { _, e := r.SetNotify(ctx, actor, target, policy.NotifyAll); return e }},
		{"read", "", func(r *chatpg.Repository) error { _, e := r.MarkRead(ctx, actor, target, target); return e }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			harness(t, actor, c.permission, func(db *sql.DB, g *denyGuard) error {
				r, e := chatpg.NewWithAuthority(db, policy, g)
				if e != nil {
					return e
				}
				return c.run(r)
			})
		})
	}
	for _, c := range []struct {
		action     rooms.MemberAction
		permission string
	}{{rooms.Invite, "chat.invites.manage"}, {rooms.Remove, "chat.members.kick"}, {rooms.Join, ""}, {rooms.Accept, ""}, {rooms.Decline, ""}, {rooms.Leave, ""}} {
		t.Run(string(c.action), func(t *testing.T) {
			harness(t, actor, c.permission, func(db *sql.DB, g *denyGuard) error {
				r, e := chatpg.NewWithAuthority(db, policy, g)
				if e != nil {
					return e
				}
				_, e = r.ChangeMember(ctx, actor, target, c.action, target)
				return e
			})
		})
	}
}
func TestTaxonomyWritesCheckProofBeforeTableLock(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name       string
		k          taxonomy.Kind
		admin      bool
		permission string
	}{{"ordinary tag", taxonomy.Tag, false, ""}, {"admin tag", taxonomy.Tag, true, "posts.tags.create"}, {"category", taxonomy.Category, true, "post.categories.manage"}} {
		t.Run(c.name, func(t *testing.T) {
			harness(t, actor, c.permission, func(db *sql.DB, g *denyGuard) error {
				_, e := taxonomypg.NewWithAuthority(db, 0, g).Create(ctx, taxonomy.Record{Kind: c.k, ID: target, AuditActorID: actor, AuditAdmin: c.admin, Slug: "synthetic"})
				return e
			})
		})
	}
	t.Run("mutate", func(t *testing.T) {
		harness(t, "", "", func(db *sql.DB, g *denyGuard) error {
			_, e := taxonomypg.NewWithAuthority(db, 0, g).Mutate(ctx, taxonomy.Tag, "synthetic", taxonomy.Update, taxonomy.Change{}, func(taxonomy.Record, taxonomy.MutationState) (taxonomy.Record, error) {
				t.Fatal("callback ran before authority")
				return taxonomy.Record{}, nil
			})
			return e
		})
	})
	for _, action := range []string{"subscription", "subscribe", "unsubscribe"} {
		t.Run(action, func(t *testing.T) {
			permission := ""
			if action != "subscription" {
				permission = "post.categories.subscribe"
			}
			harness(t, actor, permission, func(db *sql.DB, g *denyGuard) error {
				_, _, e := taxonomypg.NewWithAuthority(db, 0, g).Subscription(ctx, taxonomy.Tag, "synthetic", actor, action, time.Now())
				return e
			})
		})
	}
}
func TestDefaultConstructorsDenyWithoutAuthority(t *testing.T) {
	state := &sentinel{}
	db := sql.OpenDB(connector{state})
	defer db.Close()
	ctx := context.Background()
	if _, e := accountpg.New(db).UpdateProfile(ctx, actor, accounts.ProfilePatch{}); e == nil {
		t.Fatal("unguarded profile")
	}
	if e := boardpg.NewWithIndependentPolicy(db, nil).Replace(ctx, actor, nil); e == nil {
		t.Fatal("unguarded board")
	}
	r, e := chatpg.New(db, policy)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.Create(ctx, actor, rooms.RoomPatch{Name: ptr("Synthetic")}); e == nil {
		t.Fatal("unguarded chat")
	}
	if _, e := taxonomypg.NewWithIndependentPolicy(db, 0).Create(ctx, taxonomy.Record{Kind: taxonomy.Tag, ID: target, AuditActorID: actor, Slug: "synthetic"}); e == nil {
		t.Fatal("unguarded taxonomy")
	}
	if state.statements != 0 || state.commits != 0 {
		t.Fatalf("unguarded SQL: %+v", state)
	}
}
