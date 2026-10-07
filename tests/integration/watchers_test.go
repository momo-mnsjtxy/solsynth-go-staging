package integration_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"solsynth.local/go-migration/internal/watchpg"
	"solsynth.local/go-migration/ports/sphere"
)

func watcherID(group, n int) string { return fmt.Sprintf("%08x-0000-0000-0000-%012x", group, n) }
func sameIDSet(t *testing.T, got []string, want ...string) {
	t.Helper()
	a, b := append([]string{}, got...), append([]string{}, want...)
	sort.Strings(a)
	sort.Strings(b)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("IDs = %v, want %v", a, b)
	}
}

func TestPGWatcherRecipientsAndPreferences(t *testing.T) {
	db := database(t)
	r := watchpg.NewSoftDeleteOnlyCompatibilityTestCandidate(db)
	s := service(db)
	ctx := context.Background()
	id := func(n int) string { return watcherID(0x71000000, n) }
	target, other, a, b, c, publisher := id(1), id(2), id(3), id(4), id(5), id(6)
	// Target intentionally need not exist: no target-post join is in the source query.
	query(t, db, `INSERT INTO post_bookmarks(id,post_id,account_id) VALUES ($1,$2,$3),($4,$2,$3),($5,$6,$7)`, id(10), target, a, id(11), id(12), other, b)
	query(t, db, `INSERT INTO post_reactions(id,post_id,account_id) VALUES ($1,$2,$3),($4,$2,$3),($5,$2,$6),($7,$2,NULL),($8,$9,$10)`, id(20), target, a, id(21), id(22), b, id(23), id(24), other, c)
	query(t, db, `INSERT INTO publishers(id,account_id) VALUES ($1,$2)`, publisher, c)
	query(t, db, `INSERT INTO posts(id,publisher_id,replied_post_id) VALUES ($1,$2,$3),($4,$2,$3),($5,$2,$6)`, id(30), publisher, target, id(31), id(32), other)
	accounts, err := r.WatchAccounts(ctx, []string{target, target})
	if err != nil {
		t.Fatal(err)
	}
	sameIDSet(t, accounts.Bookmarkers, a)
	sameIDSet(t, accounts.Reactors, a, b)
	sameIDSet(t, accounts.Repliers, c)
	resolve := func(event sphere.WatchEvent) []string {
		t.Helper()
		got, e := s.ResolveWatcherAccountIDs(ctx, r, []string{target, target}, event)
		if e != nil {
			t.Fatal(e)
		}
		return got
	}
	sameIDSet(t, resolve(sphere.WatchReactions), a)
	sameIDSet(t, resolve(sphere.WatchChains), a, b, c)
	// Any eligible source suffices: disable a's bookmark flag but enable reaction.
	_, err = s.UpdatePreferences(ctx, a, []sphere.WatchPreferenceUpdate{{Source: sphere.WatchBookmark, NotifyReactions: boolean(false)}, {Source: sphere.WatchReaction, NotifyReactions: boolean(true)}})
	if err != nil {
		t.Fatal(err)
	}
	sameIDSet(t, resolve(sphere.WatchReactions), a)
	_, err = s.UpdatePreferences(ctx, a, []sphere.WatchPreferenceUpdate{{Source: sphere.WatchReaction, NotifyReactions: boolean(false)}})
	if err != nil {
		t.Fatal(err)
	}
	sameIDSet(t, resolve(sphere.WatchReactions))
	sameIDSet(t, resolve(sphere.WatchEvent("unknown")))
}

func TestPGWatcherSoftDeleteAssumptions(t *testing.T) {
	db := database(t)
	r := watchpg.NewSoftDeleteOnlyCompatibilityTestCandidate(db)
	ctx := context.Background()
	id := func(n int) string { return watcherID(0x72000000, n) }
	target, live, dead, pubLive, pubDeleted, pubNull := id(1), id(2), id(3), id(4), id(5), id(6)
	query(t, db, `INSERT INTO post_bookmarks(id,post_id,account_id,deleted_at) VALUES ($1,$2,$3,NULL),($4,$2,$5,now())`, id(10), target, live, id(11), dead)
	query(t, db, `INSERT INTO post_reactions(id,post_id,account_id,deleted_at) VALUES ($1,$2,$3,NULL),($4,$2,$5,now())`, id(20), target, live, id(21), dead)
	query(t, db, `INSERT INTO publishers(id,account_id,deleted_at) VALUES ($1,$2,NULL),($3,$4,now()),($5,NULL,NULL)`, pubLive, live, pubDeleted, dead, pubNull)
	query(t, db, `INSERT INTO posts(id,publisher_id,replied_post_id,deleted_at) VALUES ($1,$2,$3,NULL),($4,$5,$3,NULL),($6,$7,$3,NULL),($8,$2,$3,now()),($9,$10,$3,NULL)`, id(30), pubLive, target, id(31), pubDeleted, id(32), pubNull, id(33), id(34), id(999))
	accounts, err := r.WatchAccounts(ctx, []string{target})
	if err != nil {
		t.Fatal(err)
	}
	sameIDSet(t, accounts.Bookmarkers, live)
	sameIDSet(t, accounts.Reactors, live)
	sameIDSet(t, accounts.Repliers, live)
	// Explicit per-entity filtering is the documented candidate assumption;
	// this is not a proof of the uninspected Shared global filter's behavior.
	query(t, db, "UPDATE publishers SET deleted_at=now() WHERE id=$1", pubLive)
	accounts, err = r.WatchAccounts(ctx, []string{target})
	if err != nil {
		t.Fatal(err)
	}
	sameIDSet(t, accounts.Repliers)
}

func TestPGWatcherOneHopChains(t *testing.T) {
	db := database(t)
	r := watchpg.NewSoftDeleteOnlyCompatibilityTestCandidate(db)
	ctx := context.Background()
	id := func(n int) string { return watcherID(0x73000000, n) }
	head, child, grandchild, deletedChild, missingHead, orphan, pub := id(1), id(2), id(3), id(4), id(5), id(6), id(7)
	query(t, db, `INSERT INTO posts(id,publisher_id,chained_post_id,deleted_at) VALUES ($1,$2,NULL,NULL),($3,$2,$1,NULL),($4,$2,$3,NULL),($5,$2,$1,now()),($6,$2,$7,NULL)`, head, pub, child, grandchild, deletedChild, orphan, missingHead)
	check := func(h string, want ...string) {
		t.Helper()
		got, err := r.ChainPostIDs(ctx, h)
		if err != nil {
			t.Fatal(err)
		}
		sameIDSet(t, got, want...)
	}
	check(head, head, child)
	check(missingHead, orphan)
	check(id(999))
	query(t, db, "UPDATE posts SET deleted_at=now() WHERE id=$1", head)
	check(head, child)
}

func TestPGWatcherInputErrors(t *testing.T) {
	db := database(t)
	r := watchpg.NewSoftDeleteOnlyCompatibilityTestCandidate(db)
	ctx := context.Background()
	valid := watcherID(0x74000000, 1)
	defaultReader := watchpg.New(db)
	for _, ids := range [][]string{nil, {valid}, {"invalid"}} {
		if _, err := defaultReader.WatchAccounts(ctx, ids); !errors.Is(err, watchpg.ErrUnverifiedWatcherPolicy) {
			t.Fatalf("default watcher did not fail closed: %v", err)
		}
	}
	if _, err := defaultReader.ChainPostIDs(ctx, valid); !errors.Is(err, watchpg.ErrUnverifiedWatcherPolicy) {
		t.Fatalf("default chain did not fail closed: %v", err)
	}
	empty, err := r.WatchAccounts(ctx, nil)
	if err != nil || empty.Bookmarkers == nil || empty.Reactors == nil || empty.Repliers == nil {
		t.Fatalf("empty input: %+v %v", empty, err)
	}
	for _, bad := range []string{"", "not-a-uuid", "7400000A-0000-0000-0000-000000000001"} {
		if _, err := r.WatchAccounts(ctx, []string{valid, bad}); !errors.Is(err, watchpg.ErrInvalidPostID) {
			t.Fatalf("watch invalid %q: %v", bad, err)
		}
		if _, err := r.ChainPostIDs(ctx, bad); !errors.Is(err, watchpg.ErrInvalidPostID) {
			t.Fatalf("chain invalid %q: %v", bad, err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := r.WatchAccounts(cancelled, []string{valid}); !errors.Is(err, context.Canceled) {
		t.Fatalf("watch cancellation: %v", err)
	}
	if _, err := r.ChainPostIDs(cancelled, valid); !errors.Is(err, context.Canceled) {
		t.Fatalf("chain cancellation: %v", err)
	}
	broken := watchpg.NewSoftDeleteOnlyCompatibilityTestCandidate(nil)
	if _, err := broken.WatchAccounts(ctx, []string{valid}); err == nil {
		t.Fatal("missing DB accepted")
	}
	if _, err := broken.ChainPostIDs(ctx, valid); err == nil {
		t.Fatal("missing chain DB accepted")
	}
}
