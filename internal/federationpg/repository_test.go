// SPDX-License-Identifier: AGPL-3.0-only
package federationpg

import (
	"solsynth.local/go-migration/ports/federation"
	"testing"
	"time"
)

func TestConstructorAndRetryBounds(t *testing.T) {
	if _, e := New(nil, federation.Config{}, nil, nil); e == nil {
		t.Fatal("nil dependency accepted")
	}
	for _, n := range []int{-1, 0, 1, 2, 10, 99} {
		d := retryDelay(n)
		if d < time.Minute || d > 6*time.Hour {
			t.Fatal(n, d)
		}
	}
	if retryDelay(2) != 2*time.Minute {
		t.Fatal("retry backoff")
	}
}
