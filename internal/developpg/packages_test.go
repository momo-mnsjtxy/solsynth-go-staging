// SPDX-License-Identifier: AGPL-3.0-only
package developpg

import (
	"context"
	"errors"
	"testing"

	"solsynth.local/go-migration/ports/develop"
)

func TestPackageRepositoryPolicyAndGuards(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, repo := range []*PackageRepository{nil, {}, NewPackageRepository(nil)} {
		for _, c := range []context.Context{context.Background(), ctx} {
			if _, err := repo.FindPublishedPackage(c, "example"); !errors.Is(err, develop.ErrPackagePolicyUnverified) {
				t.Fatal(err)
			}
			if err := repo.IncrementPackageDownloads(c, "id"); !errors.Is(err, develop.ErrPackagePolicyUnverified) {
				t.Fatal(err)
			}
		}
	}
	repo := NewSyntheticCompatibilityPackageRepository(nil, 71)
	if _, err := repo.FindPublishedPackage(ctx, "example"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := repo.IncrementPackageDownloads(ctx, "id"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := repo.FindPublishedPackage(context.Background(), ""); !errors.Is(err, develop.ErrPackageInvalidSlug) {
		t.Fatal(err)
	}
	if _, err := repo.FindPublishedPackage(context.Background(), "example"); !errors.Is(err, ErrDatabaseRequired) {
		t.Fatal(err)
	}
	if err := repo.IncrementPackageDownloads(context.Background(), "id"); !errors.Is(err, ErrDatabaseRequired) {
		t.Fatal(err)
	}
}
