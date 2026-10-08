// SPDX-License-Identifier: AGPL-3.0-only
package ticketmediapg

import (
	"context"
	"errors"
	"testing"

	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/tickets"
)

func TestTicketMediaUnconfiguredFailsClosed(t *testing.T) {
	for _, r := range []*Repository{nil, {}} {
		if e := r.Ready(context.Background()); !errors.Is(e, tickets.ErrUnavailable) {
			t.Fatal(e)
		}
		if e := r.Apply(context.Background(), nil, "", "", nil); !errors.Is(e, tickets.ErrUnavailable) {
			t.Fatal(e)
		}
		if allowed, e := r.CanReadInTransaction(context.Background(), nil, "", ""); allowed || !errors.Is(e, media.ErrUnavailable) {
			t.Fatal(allowed, e)
		}
	}
}
