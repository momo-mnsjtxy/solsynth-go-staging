// SPDX-License-Identifier: AGPL-3.0-only
package notificationpg

import (
	"context"
	"errors"
	"solsynth.local/go-migration/ports/notifications"
	"testing"
)

func TestLocalProjectionsPrecedeOtherNotificationWork(t *testing.T) {
	// The deliberate projection error stops before any database/provider access.
	// This exercises the actual Worker.Step hook used by the composed backend.
	expected := errors.New("synthetic local projection stopped")
	called := 0
	worker := Worker{Repository: New(nil, nil, notifications.DefaultApp), LocalProjections: []func(context.Context, int) (int, error){func(ctx context.Context, limit int) (int, error) {
		called++
		if limit != 32 {
			t.Fatal("unbounded runtime projection", limit)
		}
		return 3, expected
	}}}
	n, e := worker.Step(context.Background())
	if called != 1 || n != 3 || !errors.Is(e, expected) {
		t.Fatal(called, n, e)
	}
	repo := New(nil, nil, notifications.DefaultApp)
	if e = repo.ProjectLocalInTransaction(context.Background(), nil, nil, notifications.Command{External: true}); !errors.Is(e, notifications.ErrUnavailable) {
		t.Fatal("external/unbound transaction accepted", e)
	}
}
