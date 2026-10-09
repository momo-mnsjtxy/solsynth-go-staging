// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"errors"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"testing"
	"time"
)

func TestActivationPlanConservesRemainingTimeAndOptOut(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	a := c.Subscription{ID: "a", Status: c.SubscriptionActive, IsActive: true, BegunAt: now.Add(-5 * day), EndedAt: now.Add(5 * day)}
	a.RenewalAt = &a.EndedAt
	b := c.Subscription{ID: "b", Status: c.SubscriptionActive, IsActive: true, BegunAt: a.EndedAt, EndedAt: a.EndedAt.Add(30 * day)}
	d := c.Subscription{ID: "c", Status: c.SubscriptionActive, IsActive: true, BegunAt: b.EndedAt, EndedAt: b.EndedAt.Add(7 * day)}
	plan, changed, e := planActivation([]c.Subscription{d, a, b}, "b", now)
	if e != nil || !changed || len(plan) != 3 || plan[0].ID != "b" || plan[1].ID != "a" || !plan[2].EndedAt.Equal(d.EndedAt) {
		t.Fatal(plan, changed, e)
	}
	if plan[0].RenewalAt != nil || plan[1].RenewalAt == nil || !plan[1].RenewalAt.Equal(plan[1].EndedAt) {
		t.Fatal("renewal opt-in changed", plan)
	}
	if plan[1].EndedAt.Sub(plan[1].BegunAt) != 5*day {
		t.Fatal("consumed time was regranted")
	}
	if _, changed, e = planActivation(plan, "b", now.Add(time.Hour)); e != nil || changed {
		t.Fatal("current target was not idempotent", changed, e)
	}
	reversed, changed, e := planActivation(plan, "a", now.Add(time.Hour))
	if e != nil || !changed || !reversed[2].EndedAt.Equal(d.EndedAt) {
		t.Fatal("repeated switch minted time", reversed, e)
	}
	if !a.BegunAt.Equal(now.Add(-5*day)) || !b.BegunAt.Equal(a.EndedAt) {
		t.Fatal("planner mutated input")
	}
}
func TestActivationPlanRejectsInvalidOrOverlappingTime(t *testing.T) {
	now := time.Now().UTC()
	a := c.Subscription{ID: "a", Status: c.SubscriptionActive, IsActive: true, BegunAt: now, EndedAt: now.Add(time.Hour)}
	if _, _, e := planActivation([]c.Subscription{a}, "missing", now); !errors.Is(e, c.ErrNotFound) {
		t.Fatal(e)
	}
	for _, change := range []func(*c.Subscription){func(s *c.Subscription) { s.Status = c.SubscriptionUnpaid }, func(s *c.Subscription) { s.IsTesting = true }, func(s *c.Subscription) { s.EndedAt = s.BegunAt }, func(s *c.Subscription) { s.IsActive = false }} {
		s := a
		change(&s)
		if _, _, e := planActivation([]c.Subscription{s}, s.ID, now); e == nil {
			t.Fatal("invalid grant activated", s)
		}
	}
	b := a
	b.ID = "b"
	b.BegunAt = now.Add(time.Minute)
	b.EndedAt = now.Add(2 * time.Hour)
	if _, _, e := planActivation([]c.Subscription{a, b}, b.ID, now); !errors.Is(e, c.ErrConflict) {
		t.Fatal("overlap silently repaired", e)
	}
}
