// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/boardpg"
	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/internal/taxonomypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/board"
	"solsynth.local/go-migration/ports/sphere/taxonomy"
)

// These cases require the existing marked disposable PostgreSQL runner. They
// never start listeners, discover production databases, or use real identities.
func TestPGDomainTransactionAuthority(t *testing.T) {
	for _, domain := range []string{"account", "board", "chat", "taxonomy"} {
		for _, change := range []string{"revocation", "grant removal"} {
			t.Run(domain+"/"+change, func(t *testing.T) {
				f := newDeveloperFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				proof, e := f.v.Authenticate(ctx, f.token)
				if e != nil {
					t.Fatal(e)
				}
				bound := f.guard.Bind(ctx, proof)
				id := authorityUUID(t)
				var write func(context.Context, string) error
				switch domain {
				case "account":
					write = func(ctx context.Context, actor string) error {
						_, e := accountpg.NewWithAuthority(f.db, f.guard).UpdateProfile(ctx, actor, accounts.ProfilePatch{Bio: sp("must not commit")})
						return e
					}
				case "board":
					write = func(ctx context.Context, actor string) error {
						return boardpg.NewWithAuthority(f.db, f.guard, nil).Replace(ctx, actor, []board.Item{})
					}
				case "chat":
					r, e := chatpg.NewWithAuthority(f.db, chatPolicy, f.guard)
					if e != nil {
						t.Fatal(e)
					}
					write = func(ctx context.Context, actor string) error {
						_, e := r.Create(ctx, actor, rooms.RoomPatch{Name: sp("must not commit")})
						return e
					}
				case "taxonomy":
					write = func(ctx context.Context, actor string) error {
						_, e := taxonomypg.NewWithAuthority(f.db, 0, f.guard).Create(ctx, taxonomy.Record{ID: id, Kind: taxonomy.Category, AuditActorID: actor, AuditAdmin: true, Slug: "guard-" + id, CreatedAt: time.Now(), UpdatedAt: time.Now()})
						return e
					}
				}
				if e = write(bound, f.other); e == nil {
					t.Fatal("actor substitution accepted")
				}
				blocker, e := f.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
				if e != nil {
					t.Fatal(e)
				}
				defer blocker.Rollback()
				var pid int
				if e = blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
					t.Fatal(e)
				}
				if _, e = blocker.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, f.actor); e != nil {
					t.Fatal(e)
				}
				if change == "revocation" {
					_, e = blocker.ExecContext(ctx, `UPDATE owned_authority_sessions SET revoked_at=statement_timestamp(),epoch=epoch+1 WHERE id=$1`, proof.Session.ID)
				} else {
					_, e = blocker.ExecContext(ctx, `DELETE FROM owned_authority_grants WHERE account_id=$1`, f.actor)
				}
				if e != nil {
					t.Fatal(e)
				}
				done := make(chan error, 1)
				go func() { done <- write(bound, f.actor) }()
				waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: ctx}, pid, "owned_authority_accounts")
				if e = blocker.Commit(); e != nil {
					t.Fatal(e)
				}
				e = <-done
				if domain == "board" {
					want := board.ErrForbidden
					if change == "revocation" {
						want = board.ErrUnauthenticated
					}
					if !errors.Is(e, want) {
						t.Fatalf("guard outcome: %v", e)
					}
				} else if domain == "taxonomy" {
					want := taxonomy.ErrForbidden
					if change == "revocation" {
						want = taxonomy.ErrUnauthenticated
					}
					if !errors.Is(e, want) {
						t.Fatalf("guard outcome: %v", e)
					}
				} else {
					want := authority.ErrForbidden
					if change == "revocation" {
						want = authority.ErrRevoked
					}
					if !errors.Is(e, want) {
						t.Fatalf("guard outcome: %v", e)
					}
				}
				var count int
				queries := map[string]string{"account": `SELECT count(*) FROM owned_accounts_profiles WHERE account_id=$1`, "board": `SELECT count(*) FROM account_board_items WHERE account_id=$1`, "chat": `SELECT count(*) FROM chat_rooms WHERE owned_owner_id=$1`, "taxonomy": `SELECT count(*) FROM owned_taxonomy.actions WHERE actor_id=$1`}
				if e = f.db.QueryRowContext(ctx, queries[domain], f.actor).Scan(&count); e != nil || count != 0 {
					t.Fatalf("denied mutation persisted: count=%d err=%v", count, e)
				}
			})
		}
	}
}

func TestPGTaxonomyPublisherMembershipGuard(t *testing.T) {
	f := newDeveloperFixture(t)
	ctx := context.Background()
	var publisher string
	if e := f.db.QueryRow(`SELECT id::text FROM owned_publishers WHERE name=$1`, f.dev).Scan(&publisher); e != nil {
		t.Fatal(e)
	}
	id := authorityUUID(t)
	slug := "membership-" + strings.ReplaceAll(id, "-", "")
	query(t, f.db, `INSERT INTO owned_taxonomy.post_tags(id,slug,owner_publisher_id,is_protected,is_event,created_at,updated_at)VALUES($1,$2,$3,false,false,statement_timestamp(),statement_timestamp())`, id, slug, publisher)
	proof, e := f.v.Authenticate(ctx, f.otherToken)
	if e != nil {
		t.Fatal(e)
	}
	r := taxonomypg.NewWithAuthority(f.db, 0, f.guard)
	_, e = r.Mutate(f.guard.Bind(ctx, proof), taxonomy.Tag, slug, taxonomy.Update, taxonomy.Change{}, func(row taxonomy.Record, _ taxonomy.MutationState) (taxonomy.Record, error) {
		row.AuditActorID = f.other
		row.Name = sp("must not commit")
		return row, nil
	})
	if !errors.Is(e, taxonomy.ErrForbidden) {
		t.Fatalf("foreign publisher update: %v", e)
	}
	var name sql.NullString
	if e = f.db.QueryRow(`SELECT name FROM owned_taxonomy.post_tags WHERE id=$1`, id).Scan(&name); e != nil || name.Valid {
		t.Fatalf("foreign update persisted: %v %v", name, e)
	}
	// Membership revocation wins after the publisher-row wait, and the waiting
	// taxonomy writer has not inverted the post path's publisher/table order.
	query(t, f.db, `INSERT INTO owned_publisher_members(id,publisher_id,account_id,role,joined_at,created_at,updated_at)VALUES($1,$2,$3,100,statement_timestamp(),statement_timestamp(),statement_timestamp())`, authorityUUID(t), publisher, f.other)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	blocker, e := f.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		t.Fatal(e)
	}
	defer blocker.Rollback()
	var blockerPID int
	if e = blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); e != nil {
		t.Fatal(e)
	}
	if _, e = blocker.ExecContext(ctx, `SELECT id FROM owned_publishers WHERE id=$1 FOR UPDATE`, publisher); e != nil {
		t.Fatal(e)
	}
	if _, e = blocker.ExecContext(ctx, `UPDATE owned_publisher_members SET revoked_at=statement_timestamp() WHERE publisher_id=$1 AND account_id=$2`, publisher, f.other); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, err := r.Mutate(f.guard.Bind(ctx, proof), taxonomy.Tag, slug, taxonomy.Update, taxonomy.Change{}, func(row taxonomy.Record, _ taxonomy.MutationState) (taxonomy.Record, error) {
			row.AuditActorID, row.Name = f.other, sp("must not commit after revocation")
			return row, nil
		})
		done <- err
	}()
	waiterPID := waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: ctx}, blockerPID, "owned_publishers")
	var tableLocks int
	if e = f.db.QueryRowContext(ctx, `SELECT count(*) FROM pg_locks WHERE pid=$1 AND relation='owned_taxonomy.post_tags'::regclass AND mode='ShareRowExclusiveLock'`, waiterPID).Scan(&tableLocks); e != nil || tableLocks != 0 {
		t.Fatalf("publisher/table lock order inverted: %d %v", tableLocks, e)
	}
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, taxonomy.ErrForbidden) {
		t.Fatalf("revoked publisher membership accepted: %v", e)
	}
	if e = f.db.QueryRow(`SELECT name FROM owned_taxonomy.post_tags WHERE id=$1`, id).Scan(&name); e != nil || name.Valid {
		t.Fatalf("revoked update persisted: %v %v", name, e)
	}
}
