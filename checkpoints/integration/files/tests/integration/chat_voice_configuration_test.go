// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/application"
)

func TestPGBackendDisabledVoiceRequiresNoVoiceSchemaOrWorker(t *testing.T) {
	f := newPostMediaFixture(t)
	ctx := context.Background()
	query(t, f.db, `DROP TABLE owned_chat_voice_clips`)
	cfg := publishingAppConfig(f)
	app, err := application.NewPostgres(f.db, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = app.Ready(ctx); err != nil {
		t.Fatal("ordinary media must not require disabled voice schema", err)
	}
	if _, err = app.CleanupMedia(ctx, 10); err != nil {
		t.Fatal("ordinary cleanup must not run disabled voice maintenance", err)
	}
	// A configured feature still checks its schema; disabling voice must not
	// weaken readiness when the operator explicitly enables it.
	cfg.ChatVoiceRetention = time.Hour
	app, err = application.NewPostgres(f.db, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = app.Ready(ctx); err == nil {
		t.Fatal("enabled voice accepted a missing schema")
	}
}
