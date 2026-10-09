// SPDX-License-Identifier: AGPL-3.0-only
package automod

import (
	"context"
	"errors"
	c "solsynth.local/go-migration/ports/admin/content"
	"strings"
	"testing"
)

func basic() Rule {
	return Rule{ID: "00000000-0000-4000-8000-000000000001", Name: "Synthetic rule", Type: KeywordBlocklist, DefaultAction: Flag, Pattern: "synthetic token", DerankWeight: 10, IsEnabled: true}
}
func TestExplicitMatcherAndBounds(t *testing.T) {
	r := basic()
	v, e := Match(context.Background(), r, "A SYNTHETIC TOKEN appears")
	if e != nil || len(v) != 1 || v[0].MatchedText != r.Pattern || v[0].Action != Flag {
		t.Fatal(v, e)
	}
	r.Type = LinkBlocklist
	r.Pattern = "example.test"
	v, e = Match(context.Background(), r, "https://EXAMPLE.TEST/a")
	if e != nil || len(v) != 1 {
		t.Fatal(v, e)
	}
	r.IsRegex = true
	r.Pattern = `hello\s+world`
	v, e = Match(context.Background(), r, "HELLO World")
	if e != nil || len(v) != 1 || v[0].MatchedText != "HELLO World" {
		t.Fatal(v, e)
	}
	r.Pattern = "^$"
	v, e = Match(context.Background(), r, "")
	if e != nil || len(v) != 1 || v[0].MatchedText != "" {
		t.Fatal("empty match lost", v, e)
	}
	for _, pattern := range []string{`(?<=hello)world`, `(x)\1`, `[`, `x{1000}y{1000}`} {
		r.Pattern = pattern
		if Validate(r) == nil {
			t.Fatal("unsupported or unbounded regex accepted", pattern)
		}
	}
	r = basic()
	if _, e = Match(context.Background(), r, strings.Repeat("x", MaxContentBytes+1)); !errors.Is(e, c.ErrInvalid) {
		t.Fatal(e)
	}
	r.IsRegex = true
	r.Pattern = strings.Repeat("x", 256)
	if _, e = Match(context.Background(), r, strings.Repeat("x", MaxContentBytes)); !errors.Is(e, c.ErrInvalid) {
		t.Fatal("work cap", e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = Match(ctx, basic(), "token"); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
func TestRuleInputDoesNotInventActions(t *testing.T) {
	p := Patch{}
	if _, e := p.Apply(Rule{}, true); e == nil {
		t.Fatal("missing required enum fields")
	}
	r := basic()
	r.Type = 0
	if Validate(r) == nil {
		t.Fatal("unknown type accepted")
	}
	r = basic()
	r.DefaultAction = 4
	if Validate(r) == nil {
		t.Fatal("unknown action accepted")
	}
	r = basic()
	r.Pattern = ""
	if Validate(r) == nil {
		t.Fatal("empty universal literal accepted")
	}
	r = basic()
	r.Description = new(string)
	*r.Description = strings.Repeat("😀", 513)
	if Validate(r) == nil {
		t.Fatal("UTF-16 description cap bypass")
	}
}
