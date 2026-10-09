// SPDX-License-Identifier: AGPL-3.0-only
package callpg

import (
	"context"
	"errors"
	"solsynth.local/go-migration/ports/messager/calls"
	"testing"
)

func TestMissingProviderAndGuardFailClosed(t *testing.T) {
	if _, e := New(nil, nil, nil, Config{}); !errors.Is(e, calls.ErrInvalid) {
		t.Fatal(e)
	}
	r := &Repository{}
	if _, e := r.Start(context.Background(), "", " "); !errors.Is(e, calls.ErrUnconfigured) {
		t.Fatal(e)
	}
	if _, e := r.ProcessDue(context.Background(), 1); !errors.Is(e, calls.ErrUnconfigured) {
		t.Fatal(e)
	}
}
