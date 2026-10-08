// SPDX-License-Identifier: AGPL-3.0-only
package nodeinfopg

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"solsynth.local/go-migration/ports/sphere/nodeinfo"
)

func TestDefaultAndZeroRepositoryFailClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, repo := range []*Repository{nil, {}, New(nil), New(&sql.DB{})} {
		for _, ctx := range []context.Context{context.Background(), ctx} {
			got, err := repo.ReadStats(ctx)
			if !errors.Is(err, ErrUnverifiedPolicy) || got != (nodeinfo.Stats{}) {
				t.Fatal(got, err)
			}
		}
	}
}

func TestSyntheticRepositoryRequiresExplicitPolicy(t *testing.T) {
	for _, policy := range []SoftDeletePolicy{"", "guess", "include-deleted"} {
		if repo, err := NewSyntheticCompatibilityRepository(nil, 42, policy); repo != nil || !errors.Is(err, ErrCandidatePolicyRequired) {
			t.Fatal(repo, err)
		}
	}
	for _, enum := range []int32{0, 42, -2147483648, 2147483647} {
		repo, err := NewSyntheticCompatibilityRepository(nil, enum, CandidateExcludeDeletedRows)
		if err != nil || repo.fediverseType != enum {
			t.Fatal(repo, err)
		}
		if got, err := repo.ReadStats(context.Background()); got != (nodeinfo.Stats{}) || !errors.Is(err, ErrDatabaseRequired) {
			t.Fatal(got, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if got, err := repo.ReadStats(ctx); got != (nodeinfo.Stats{}) || !errors.Is(err, context.Canceled) {
			t.Fatal(got, err)
		}
	}
}
