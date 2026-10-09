// SPDX-License-Identifier: AGPL-3.0-only
package develop

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type packageFixtureRepository struct {
	app                     PackageRecord
	lookupErr, incrementErr error
	calls                   []string
}

func (r *packageFixtureRepository) FindPublishedPackage(ctx context.Context, slug string) (PackageRecord, error) {
	r.calls = append(r.calls, "lookup:"+slug)
	return r.app, r.lookupErr
}
func (r *packageFixtureRepository) IncrementPackageDownloads(ctx context.Context, id string) error {
	r.calls = append(r.calls, "increment:"+id)
	return r.incrementErr
}

type packageFixtureStorage struct {
	calls []string
	data  []byte
	err   error
}

func (s *packageFixtureStorage) ReadPackage(ctx context.Context, key string) ([]byte, error) {
	s.calls = append(s.calls, key)
	return s.data, s.err
}
func packageString(s string) *string { return &s }

func TestPackageServiceDefaultPolicy(t *testing.T) {
	repo := &packageFixtureRepository{}
	for _, svc := range []*PackageService{nil, {}, NewPackageService(repo, nil)} {
		_, err := svc.Download(context.Background(), "example")
		if !errors.Is(err, ErrPackagePolicyUnverified) || len(repo.calls) != 0 {
			t.Fatal(err, repo.calls)
		}
	}
}

func TestPackageServiceDownloadOrdering(t *testing.T) {
	storageError := errors.New("fixture storage error")
	for _, tc := range []struct {
		name                                         string
		key, url                                     *string
		lookupErr, incrementErr, storageErr, wantErr error
		wantCalls, wantStorage                       []string
		redirect                                     string
	}{
		{name: "missing app", lookupErr: ErrPackageNotFound, wantErr: ErrPackageNotFound, wantCalls: []string{"lookup:example"}},
		{name: "nil key", wantErr: ErrPackageMissing, wantCalls: []string{"lookup:example"}},
		{name: "blank key despite URL", key: packageString(" \t"), url: packageString("https://example.invalid/file.zip"), wantErr: ErrPackageMissing, wantCalls: []string{"lookup:example"}},
		{name: "increment failed", key: packageString("file.zip"), incrementErr: storageError, wantErr: storageError, wantCalls: []string{"lookup:example", "increment:id"}},
		{name: "storage failure retains increment", key: packageString("file.zip"), storageErr: storageError, wantErr: storageError, wantCalls: []string{"lookup:example", "increment:id"}, wantStorage: []string{"file.zip"}},
		{name: "local bytes", key: packageString("file.zip"), url: packageString(" \t"), wantCalls: []string{"lookup:example", "increment:id"}, wantStorage: []string{"file.zip"}},
		{name: "redirect skips storage", key: packageString("file.zip"), url: packageString("https://example.invalid/file.zip?v=1"), wantCalls: []string{"lookup:example", "increment:id"}, redirect: "https://example.invalid/file.zip?v=1"},
		{name: "unsupported URL retains increment", key: packageString("file.zip"), url: packageString("mailto:example@example.invalid"), wantErr: ErrPackageUnavailable, wantCalls: []string{"lookup:example", "increment:id"}},
		{name: "relative URL restriction", key: packageString("file.zip"), url: packageString("/file.zip"), wantErr: ErrPackageUnavailable, wantCalls: []string{"lookup:example", "increment:id"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &packageFixtureRepository{app: PackageRecord{ID: "id", Slug: "example", StorageKey: tc.key, URL: tc.url}, lookupErr: tc.lookupErr, incrementErr: tc.incrementErr}
			storage := &packageFixtureStorage{data: []byte("fixture"), err: tc.storageErr}
			got, err := NewSyntheticCompatibilityPackageService(repo, storage).Download(context.Background(), "example")
			if !errors.Is(err, tc.wantErr) || !reflect.DeepEqual(repo.calls, tc.wantCalls) || !reflect.DeepEqual(storage.calls, tc.wantStorage) {
				t.Fatal(got, err, repo.calls, storage.calls)
			}
			if err == nil && (got.Slug != "example" || got.RedirectURL != tc.redirect || tc.redirect == "" && string(got.Data) != "fixture") {
				t.Fatal(got)
			}
		})
	}
}

func TestPackageServiceInputAndCancellation(t *testing.T) {
	repo := &packageFixtureRepository{}
	svc := NewSyntheticCompatibilityPackageService(repo, nil)
	for _, slug := range []string{"", "a/b", "a\\b", "a\x00b"} {
		if _, err := svc.Download(context.Background(), slug); !errors.Is(err, ErrPackageInvalidSlug) {
			t.Fatal(slug, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.Download(ctx, "example"); !errors.Is(err, context.Canceled) || len(repo.calls) != 0 {
		t.Fatal(err, repo.calls)
	}
	if _, err := NewSyntheticCompatibilityPackageService(nil, nil).Download(context.Background(), "example"); !errors.Is(err, ErrPackageUnavailable) {
		t.Fatal(err)
	}
	repo.app = PackageRecord{ID: "id", Slug: "example", StorageKey: packageString("file.zip")}
	if _, err := svc.Download(context.Background(), "example"); !errors.Is(err, ErrPackageUnavailable) || len(repo.calls) != 2 {
		t.Fatal(err, repo.calls)
	}
}
