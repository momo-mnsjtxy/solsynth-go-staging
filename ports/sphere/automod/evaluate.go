// SPDX-License-Identifier: AGPL-3.0-only
package automod

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	c "solsynth.local/go-migration/ports/admin/content"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type Evaluation struct {
	Work        int
	Outcome     string
	Hidden      bool
	Penalty     int
	Flagged     bool
	Matches     []Result
	Fingerprint string
}

// RuleSnapshot provides a deterministic active-rule identity without matching.
func RuleSnapshot(rules []Rule, now time.Time) ([]Rule, string) {
	active := make([]Rule, 0, len(rules))
	for _, r := range rules {
		if r.IsEnabled && (r.ExpiresAt == nil || r.ExpiresAt.After(now)) {
			active = append(active, r)
		}
	}
	sort.Slice(active, func(i, j int) bool {
		if active[i].Priority != active[j].Priority {
			return active[i].Priority < active[j].Priority
		}
		return active[i].ID < active[j].ID
	})
	b, _ := json.Marshal(active)
	h := sha256.Sum256(b)
	return active, hex.EncodeToString(h[:])
}

// Evaluate is the owned, deterministic all-rule policy. Hide wins the outcome,
// then Flag, then Derank; every matching rule remains in the ordered reasons.
// Unlike individual rule tests, the work bound applies to the entire ruleset.
func Evaluate(ctx context.Context, rules []Rule, text string, now time.Time) (Evaluation, error) {
	out := Evaluation{Outcome: "clear", Matches: []Result{}}
	if len(rules) > MaxRules {
		return out, c.ErrUnavailable
	}
	active, fingerprint := RuleSnapshot(rules, now)
	out.Fingerprint = fingerprint
	work := 0
	for _, r := range active {
		if e := ctx.Err(); e != nil {
			return out, e
		}
		cost := 1
		if r.IsRegex {
			_, n, e := program(r.Pattern)
			if e != nil {
				return out, e
			}
			cost = n
		}
		// Division avoids overflow for callers with an oversized input string.
		if len(text) > MaxContentBytes || len(text)+1 > (MaxEvaluationWork-work)/cost {
			return out, c.ErrInvalid
		}
		work += (len(text) + 1) * cost
		matches, e := Match(ctx, r, text)
		if e != nil {
			return out, e
		}
		for _, m := range matches {
			// Persist only a bounded UTF-8 excerpt; absence of a full body is
			// deliberate and the exact rule/content hashes bind the decision.
			if len(m.MatchedText) > 256 {
				m.MatchedText = m.MatchedText[:256]
				for !utf8.ValidString(m.MatchedText) {
					m.MatchedText = m.MatchedText[:len(m.MatchedText)-1]
				}
			}
			out.Matches = append(out.Matches, m)
			switch m.Action {
			case Hide:
				out.Hidden = true
				out.Penalty += 100
			case Derank:
				out.Penalty += m.DerankWeight
			case Flag:
				out.Flagged = true
			}
		}
	}
	if out.Hidden {
		out.Outcome = "hidden"
	} else if out.Flagged {
		out.Outcome = "flagged"
	} else if len(out.Matches) > 0 {
		out.Outcome = "deranked"
	}
	out.Work = work
	return out, ctx.Err()
}

// Content uses the licensed service's space-joined text fields. Mention URLs
// are not a supported owned post field; no remote content is fetched.
func Content(title, description *string, body string) string {
	parts := []string{}
	for _, p := range []*string{title, description, &body} {
		if p != nil && strings.TrimSpace(*p) != "" {
			parts = append(parts, *p)
		}
	}
	return strings.Join(parts, " ")
}
