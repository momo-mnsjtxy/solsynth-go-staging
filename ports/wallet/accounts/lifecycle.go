// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import "time"

// Effect is applied once under transaction-row and deterministic wallet locks.
type Effect int

const (
	NoEffect Effect = iota
	Release
	Refund
)

func InitialStatus(freeze, confirm bool, now time.Time) (Status, *time.Time, *time.Time, *time.Time) {
	if !freeze && !confirm {
		return Confirmed, nil, nil, &now
	}
	expires := now.Add(24 * time.Hour)
	if freeze {
		return Frozen, &now, &expires, nil
	}
	return Pending, nil, &expires, nil
}

// Advance independently repairs contradictory legacy freeze documentation:
// early acceptance is recorded, but frozen funds cannot settle before deadline.
func Advance(t Transaction, action Transition, now time.Time) (Transaction, Effect, error) {
	if t.Status != Pending && t.Status != Frozen {
		if (action == Confirm && t.Status == Confirmed) || (action == Reject && t.Status == Refunded) {
			return t, NoEffect, nil
		}
		return t, NoEffect, ErrConflict
	}
	if t.ExpiresAt == nil {
		return t, NoEffect, ErrConflict
	}
	if !now.Before(*t.ExpiresAt) {
		return t, NoEffect, ErrConflict
	}
	if action == Reject {
		t.Status = Refunded
		t.UpdatedAt = now
		return t, Refund, nil
	}
	if action != Confirm {
		return t, NoEffect, ErrInvalid
	}
	if t.ConfirmationReceivedAt == nil {
		t.ConfirmationReceivedAt = &now
		t.UpdatedAt = now
	}
	if t.IsFrozen {
		return t, NoEffect, nil
	}
	t.Status = Confirmed
	t.ConfirmedAt = &now
	t.UpdatedAt = now
	return t, Release, nil
}
func Expire(t Transaction, now time.Time) (Transaction, Effect, error) {
	if t.Status != Pending && t.Status != Frozen {
		return t, NoEffect, nil
	}
	if t.ExpiresAt == nil {
		return t, NoEffect, ErrConflict
	}
	if now.Before(*t.ExpiresAt) {
		return t, NoEffect, nil
	}
	t.UpdatedAt = now
	if t.RequireConfirmation && t.ConfirmationReceivedAt == nil {
		t.Status = Refunded
		return t, Refund, nil
	}
	t.Status = Confirmed
	t.ConfirmedAt = &now
	return t, Release, nil
}
