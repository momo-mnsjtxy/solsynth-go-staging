// SPDX-License-Identifier: AGPL-3.0-only
package watchpg

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestWatchPostArgsCanonicalBoundAndDeduplicated(t *testing.T) {
	a := "11111111-1111-4111-8111-111111111111"
	b := "22222222-2222-4222-8222-222222222222"
	marks, args, err := watchPostArgs([]string{a, b, a})
	if err != nil || marks != "$1::uuid,$2::uuid" || !reflect.DeepEqual(args, []any{a, b}) {
		t.Fatal(marks, args, err)
	}
	for _, id := range []string{"", "not-a-uuid", "11111111-1111-4111-8111-11111111111A", "11111111-1111-4111-8111-111111111111'", "{11111111-1111-4111-8111-111111111111}"} {
		if _, _, err := watchPostArgs([]string{a, id}); !errors.Is(err, ErrInvalidPostID) {
			t.Fatalf("%q: %v", id, err)
		}
	}
}

func TestWatchAccountsEmptyNeedsNoDatabase(t *testing.T) {
	got, err := NewSoftDeleteOnlyCompatibilityTestCandidate(nil).WatchAccounts(context.Background(), nil)
	if err != nil || got.Bookmarkers == nil || got.Reactors == nil || got.Repliers == nil || len(got.Bookmarkers)+len(got.Reactors)+len(got.Repliers) != 0 {
		t.Fatal(got, err)
	}
}

func TestWatchReaderInvalidIDsAndCancellation(t *testing.T) {
	repo := NewSoftDeleteOnlyCompatibilityTestCandidate(nil)
	ctx := context.Background()
	if _, err := repo.WatchAccounts(ctx, []string{"bad"}); !errors.Is(err, ErrInvalidPostID) {
		t.Fatal(err)
	}
	if _, err := repo.ChainPostIDs(ctx, "bad"); !errors.Is(err, ErrInvalidPostID) {
		t.Fatal(err)
	}
	if _, err := repo.WatchAccounts(ctx, []string{testAccount}); err == nil {
		t.Fatal("missing database accepted")
	}
	if _, err := repo.ChainPostIDs(ctx, testAccount); err == nil {
		t.Fatal("missing database accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := repo.WatchAccounts(canceled, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestOrdinaryRepositoryWatcherPolicyFailsClosed(t *testing.T) {
	repo := New(nil)
	for _, ids := range [][]string{nil, {testAccount}, {"bad"}} {
		if _, err := repo.WatchAccounts(context.Background(), ids); !errors.Is(err, ErrUnverifiedWatcherPolicy) {
			t.Fatal(ids, err)
		}
	}
	if _, err := repo.ChainPostIDs(context.Background(), testAccount); !errors.Is(err, ErrUnverifiedWatcherPolicy) {
		t.Fatal(err)
	}
	var absent *Repository
	if _, err := absent.WatchAccounts(context.Background(), nil); !errors.Is(err, ErrUnverifiedWatcherPolicy) {
		t.Fatal(err)
	}
	if _, err := absent.ChainPostIDs(context.Background(), testAccount); !errors.Is(err, ErrUnverifiedWatcherPolicy) {
		t.Fatal(err)
	}
}
