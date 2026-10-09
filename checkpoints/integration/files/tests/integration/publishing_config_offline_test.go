// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"os"
	"testing"

	"solsynth.local/go-migration/internal/mediastore"
)

func TestPublishingFixtureMediaConfiguration(t *testing.T) {
	if cfg := publishingAppConfig(postMediaFixture{}); cfg.MediaStore != nil {
		t.Fatal("absent adapter became a configured interface")
	}
	var typedNil *mediastore.Filesystem
	if cfg := publishingAppConfig(postMediaFixture{mediaFixture: mediaFixture{store: typedNil}}); cfg.MediaStore != nil {
		t.Fatal("typed nil adapter became a configured interface")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := mediastore.NewFilesystem(root, 65536)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := publishingAppConfig(postMediaFixture{mediaFixture: mediaFixture{store: store}})
	if cfg.MediaStore != store || cfg.MediaStore.Ready(context.Background()) != nil {
		t.Fatal("configured valid adapter was lost")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if cfg.MediaStore == nil || cfg.MediaStore.Ready(context.Background()) == nil {
		t.Fatal("configured unavailable adapter bypassed readiness")
	}
}
