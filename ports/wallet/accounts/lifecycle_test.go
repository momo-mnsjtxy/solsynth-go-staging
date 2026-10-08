package accounts

import (
	"testing"
	"time"
)

func lifecycleFixture(freeze, confirm bool, now time.Time) Transaction {
	s, f, e, c := InitialStatus(freeze, confirm, now)
	return Transaction{Status: s, IsFrozen: freeze, RequireConfirmation: confirm, FrozenAt: f, ExpiresAt: e, ConfirmedAt: c, CreatedAt: now, UpdatedAt: now}
}
func TestLifecycleMatrixAndConservationEffects(t *testing.T) {
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	for _, freeze := range []bool{false, true} {
		for _, confirm := range []bool{false, true} {
			tr := lifecycleFixture(freeze, confirm, now)
			if !freeze && !confirm {
				if tr.Status != Confirmed || tr.ExpiresAt != nil || tr.ConfirmedAt == nil {
					t.Fatal(tr)
				}
				continue
			}
			if tr.ExpiresAt == nil || !tr.ExpiresAt.Equal(now.Add(24*time.Hour)) {
				t.Fatal(tr)
			}
			early, effect, e := Expire(tr, now.Add(23*time.Hour))
			if e != nil || effect != NoEffect || early.Status != tr.Status {
				t.Fatal(early, effect, e)
			}
			due, effect, e := Expire(tr, now.Add(24*time.Hour))
			want := Release
			if confirm {
				want = Refund
			}
			if e != nil || effect != want {
				t.Fatal(due, effect, e)
			}
			again, effect, e := Expire(due, now.Add(25*time.Hour))
			if e != nil || effect != NoEffect || again.Status != due.Status {
				t.Fatal(again, effect, e)
			}
			accepted, effect, e := Advance(tr, Confirm, now.Add(time.Hour))
			if e != nil {
				t.Fatal(e)
			}
			if freeze {
				if effect != NoEffect || accepted.Status != Frozen || accepted.ConfirmationReceivedAt == nil {
					t.Fatal(accepted, effect)
				}
				accepted, effect, e = Expire(accepted, now.Add(24*time.Hour))
			}
			if e != nil || effect != Release || accepted.Status != Confirmed {
				t.Fatal(accepted, effect, e)
			}
		}
	}
}
func TestLifecycleDuplicateConflictAndDeadline(t *testing.T) {
	now := time.Now().UTC()
	tr := lifecycleFixture(false, true, now)
	confirmed, effect, e := Advance(tr, Confirm, now)
	if e != nil || effect != Release {
		t.Fatal(e)
	}
	if _, effect, e = Advance(confirmed, Confirm, now); e != nil || effect != NoEffect {
		t.Fatal(e)
	}
	if _, _, e = Advance(confirmed, Reject, now); e != ErrConflict {
		t.Fatal(e)
	}
	rejected, effect, e := Advance(tr, Reject, now)
	if e != nil || effect != Refund {
		t.Fatal(e)
	}
	if _, effect, e = Advance(rejected, Reject, now); e != nil || effect != NoEffect {
		t.Fatal(e)
	}
	if _, _, e = Advance(tr, Confirm, *tr.ExpiresAt); e != ErrConflict {
		t.Fatal("deadline boundary", e)
	}
}
