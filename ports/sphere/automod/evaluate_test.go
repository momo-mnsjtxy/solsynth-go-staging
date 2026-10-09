// SPDX-License-Identifier: AGPL-3.0-only
package automod

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestEvaluateAllActionsPrecedenceExpiryAndFingerprint(t *testing.T) {
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	rules := []Rule{}
	for i, action := range []int{Flag, Derank, Hide, Derank} {
		r := basic()
		r.ID = string(rune('d' - i))
		r.DefaultAction = action
		r.DerankWeight = 7
		rules = append(rules, r)
	}
	d, e := Evaluate(context.Background(), rules, "SYNTHETIC TOKEN", now)
	if e != nil || !d.Hidden || !d.Flagged || d.Penalty != 114 || d.Outcome != "hidden" || len(d.Matches) != 4 || d.Matches[0].RuleID != "a" {
		t.Fatal(d, e)
	}
	rules[2].IsEnabled = false
	d2, e := Evaluate(context.Background(), rules, "SYNTHETIC TOKEN", now)
	if e != nil || d2.Hidden || d2.Penalty != 14 || d2.Outcome != "flagged" || d2.Fingerprint == d.Fingerprint {
		t.Fatal(d2, e)
	}
	rules[0].ExpiresAt = &now
	d, e = Evaluate(context.Background(), rules, "SYNTHETIC TOKEN", now)
	if e != nil || d.Flagged || d.Outcome != "deranked" {
		t.Fatal(d, e)
	}
	for i := range rules {
		rules[i].IsEnabled = false
	}
	d, e = Evaluate(context.Background(), rules, strings.Repeat("x", MaxContentBytes+1), now)
	if e != nil || d.Outcome != "clear" || len(d.Matches) != 0 {
		t.Fatal("no configured rules should not reject an existing large body", d, e)
	}
}
func TestEvaluateAggregateBudgetAndBoundedEvidence(t *testing.T) {
	r := basic()
	r.Pattern = "x"
	rules := make([]Rule, 129)
	for i := range rules {
		rules[i] = r
	}
	if _, e := Evaluate(context.Background(), rules, strings.Repeat("x", MaxContentBytes), time.Now()); e == nil {
		t.Fatal("ruleset aggregate work cap bypassed")
	}
	r.IsRegex = true
	r.Pattern = ".+"
	d, e := Evaluate(context.Background(), []Rule{r}, strings.Repeat("日", 200), time.Now())
	if e != nil || len(d.Matches) != 1 || len(d.Matches[0].MatchedText) > 256 || !utf8.ValidString(d.Matches[0].MatchedText) {
		t.Fatal(d, e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = Evaluate(ctx, []Rule{}, "", time.Now()); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
func TestAutomaticTextFieldsAndLiteralURLPolicy(t *testing.T) {
	title, description := "A title", "Description"
	if Content(&title, &description, "body") != "A title Description body" {
		t.Fatal("field join differs")
	}
	r := basic()
	r.Type = LinkBlocklist
	r.Pattern = "https://example.test/path"
	for _, tt := range []struct {
		text  string
		match bool
	}{
		{"https://EXAMPLE.TEST/PATH", true},
		{"https://example.test/%70ath", false},
		{"https://example.test:443/path", false},
		{"https://example.test/a/../path", false},
		{"https://example.test/path?query=1", true},
	} {
		d, e := Evaluate(context.Background(), []Rule{r}, tt.text, time.Now())
		if e != nil || (len(d.Matches) > 0) != tt.match {
			t.Fatal(tt, d, e)
		}
	}
	// Both authored types are text rules; no URI decoding, DNS, normalization,
	// provider call, or invented URL-equivalence policy is part of matching.
	r.IsRegex = true
	r.Pattern = `(?<=x)y`
	if _, e := Evaluate(context.Background(), []Rule{r}, "xy", time.Now()); e == nil {
		t.Fatal("unsupported regex admitted")
	}
}
