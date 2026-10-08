// SPDX-License-Identifier: AGPL-3.0-only
package developpg

import (
	"context"
	"errors"
	"testing"

	"solsynth.local/go-migration/ports/develop"
)

func TestDefaultAndZeroRepositoryFailClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, repo := range []*Repository{nil, {}, New(nil)} {
		for _, query := range []develop.Query{{Take: 20}, {Take: -1}} {
			for _, c := range []context.Context{context.Background(), ctx} {
				page, err := repo.Discover(c, query)
				if !errors.Is(err, ErrUnverifiedPolicy) || page.Apps != nil || page.Total != 0 {
					t.Fatal(page, err)
				}
			}
		}
	}
}

func TestSyntheticRepositoryInputGuards(t *testing.T) {
	repo := NewSyntheticCompatibilityRepository(nil, 42)
	for _, query := range []develop.Query{{Take: -1}, {Take: 101}, {Offset: -1}} {
		if _, err := repo.Discover(context.Background(), query); !errors.Is(err, develop.ErrInvalidQuery) {
			t.Fatal(query, err)
		}
	}
	for _, query := range []develop.Query{{Take: 0}, {Take: 100, Offset: 1}} {
		if _, err := repo.Discover(context.Background(), query); !errors.Is(err, ErrDatabaseRequired) {
			t.Fatal(query, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repo.Discover(ctx, develop.Query{Take: 20}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
