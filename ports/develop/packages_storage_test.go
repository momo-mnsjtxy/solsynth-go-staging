// SPDX-License-Identifier: AGPL-3.0-only
package develop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPackageLocalStorageContainment(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "file.zip"), []byte("fixture bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "outside.zip"), []byte("outside bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	storage, err := NewSyntheticLocalPackageStorage(root)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	data, err := storage.ReadPackage(context.Background(), "nested/file.zip")
	if err != nil || string(data) != "fixture bytes" {
		t.Fatal(string(data), err)
	}
	for _, key := range []string{"", "/absolute.zip", "../outside.zip", "nested/../file.zip", "nested\\file.zip", "nested//file.zip", "nested/./file.zip", "bad\x00.zip"} {
		if _, err := storage.ReadPackage(context.Background(), key); !errors.Is(err, ErrPackageInvalidKey) {
			t.Fatal(key, err)
		}
	}
	if err := os.Symlink(filepath.Join(outside, "outside.zip"), filepath.Join(root, "outside-link.zip")); err != nil {
		t.Fatal(err)
	}
	if data, err := storage.ReadPackage(context.Background(), "outside-link.zip"); err == nil || data != nil {
		t.Fatal("outside root read", data, err)
	}
	if err := os.Symlink("nested/file.zip", filepath.Join(root, "inside-link.zip")); err != nil {
		t.Fatal(err)
	}
	if data, err := storage.ReadPackage(context.Background(), "inside-link.zip"); err != nil || string(data) != "fixture bytes" {
		t.Fatal(data, err)
	}
	for _, key := range []string{"absent.zip", "nested"} {
		if _, err := storage.ReadPackage(context.Background(), key); err == nil {
			t.Fatal(key)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := storage.ReadPackage(ctx, "nested/file.zip"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestPackageLocalStorageLimitsAndLifecycle(t *testing.T) {
	root := t.TempDir()
	file, err := os.Create(filepath.Join(root, "large.zip"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(MaxSyntheticPackageBytes + 1); err != nil {
		t.Fatal(err)
	}
	file.Close()
	storage, err := NewSyntheticLocalPackageStorage(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.ReadPackage(context.Background(), "large.zip"); !errors.Is(err, ErrPackageUnavailable) {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.ReadPackage(context.Background(), "large.zip"); err == nil {
		t.Fatal("closed root allowed read")
	}
	if _, err := NewSyntheticLocalPackageStorage(filepath.Join(root, "missing")); err == nil {
		t.Fatal("missing root")
	}
	for _, storage := range []*LocalPackageStorage{nil, {}} {
		if _, err := storage.ReadPackage(context.Background(), "file.zip"); !errors.Is(err, ErrPackageUnavailable) {
			t.Fatal(err)
		}
	}
}
