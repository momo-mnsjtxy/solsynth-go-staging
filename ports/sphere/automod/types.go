// SPDX-License-Identifier: AGPL-3.0-only
package automod

import (
	"context"
	"net/http"
	"regexp"
	"regexp/syntax"
	c "solsynth.local/go-migration/ports/admin/content"
	"strings"
	"time"
	"unicode/utf8"
)

// Values are explicit in the licensed authored Sphere Models/AutomodRule.cs.
const (
	LinkBlocklist        = 1
	KeywordBlocklist     = 2
	Derank               = 1
	Hide                 = 2
	Flag                 = 3
	MaxRules             = 1000
	MaxBulk              = 100
	MaxContentBytes      = 65536
	MaxRegexInstructions = 1024
	MaxEvaluationWork    = 8 << 20
)

var ErrNotFound = &c.Failure{Code: "AUTOMOD_RULE_NOT_FOUND", Message: "Rule not found.", Status: 404}

type Rule struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Description   *string    `json:"description"`
	Type          int        `json:"type"`
	DefaultAction int        `json:"default_action"`
	Pattern       string     `json:"pattern"`
	IsRegex       bool       `json:"is_regex"`
	DerankWeight  int        `json:"derank_weight"`
	IsEnabled     bool       `json:"is_enabled"`
	Priority      int        `json:"priority"`
	ExpiresAt     *time.Time `json:"expires_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}
type Patch struct {
	Name          *string `json:"name"`
	Description   *string `json:"description"`
	Type          *int    `json:"type"`
	DefaultAction *int    `json:"default_action"`
	Pattern       *string `json:"pattern"`
	IsRegex       *bool   `json:"is_regex"`
	DerankWeight  *int    `json:"derank_weight"`
	IsEnabled     *bool   `json:"is_enabled"`
	Priority      *int    `json:"priority"`
}

func (p Patch) Apply(r Rule, create bool) (Rule, error) {
	if create {
		r.IsEnabled = true
		r.DerankWeight = 10
		if p.Name == nil || p.Type == nil || p.DefaultAction == nil || p.Pattern == nil {
			return r, c.ErrInvalid
		}
	}
	if p.Name != nil {
		r.Name = *p.Name
	}
	if p.Description != nil {
		r.Description = p.Description
	}
	if p.Type != nil {
		r.Type = *p.Type
	}
	if p.DefaultAction != nil {
		r.DefaultAction = *p.DefaultAction
	}
	if p.Pattern != nil {
		r.Pattern = *p.Pattern
	}
	if p.IsRegex != nil {
		r.IsRegex = *p.IsRegex
	}
	if p.DerankWeight != nil {
		r.DerankWeight = *p.DerankWeight
	}
	if p.IsEnabled != nil {
		r.IsEnabled = *p.IsEnabled
	}
	if p.Priority != nil {
		r.Priority = *p.Priority
	}
	return r, Validate(r)
}
func validText(v string, n int) bool {
	if !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
		return false
	}
	length := 0
	for _, r := range v {
		length++
		if r > 0xffff {
			length++
		}
	}
	return length <= n
}

// Reject repeat expansion before Simplify/Compile allocates its program.
func regexCost(r *syntax.Regexp, depth int) int {
	if depth > 128 {
		return MaxRegexInstructions + 1
	}
	cost := 1
	for _, child := range r.Sub {
		n := regexCost(child, depth+1)
		if n > MaxRegexInstructions {
			return n
		}
		cost += n
		if cost > MaxRegexInstructions {
			return cost
		}
	}
	switch r.Op {
	case syntax.OpLiteral:
		cost += len(r.Rune)
	case syntax.OpCapture:
		cost += 2
	case syntax.OpStar, syntax.OpPlus, syntax.OpQuest:
		cost += 2
	case syntax.OpRepeat:
		copies := r.Max
		if copies < 0 {
			copies = r.Min + 1
		}
		cost = cost*(copies+1) + 2
	}
	return cost
}

func program(pattern string) (*regexp.Regexp, int, error) {
	tree, e := syntax.Parse("(?i:"+pattern+")", syntax.Perl)
	if e != nil {
		return nil, 0, c.ErrInvalid
	}
	if regexCost(tree, 0) > MaxRegexInstructions {
		return nil, 0, c.ErrInvalid
	}
	prog, e := syntax.Compile(tree.Simplify())
	if e != nil || len(prog.Inst) > MaxRegexInstructions {
		return nil, 0, c.ErrInvalid
	}
	r, e := regexp.Compile("(?i:" + pattern + ")")
	if e != nil {
		return nil, 0, c.ErrInvalid
	}
	return r, len(prog.Inst), nil
}
func Validate(r Rule) error {
	if strings.TrimSpace(r.Name) == "" || !validText(r.Name, 256) || r.Pattern == "" || !validText(r.Pattern, 4096) || r.Description != nil && !validText(*r.Description, 1024) || r.Type < 1 || r.Type > 2 || r.DefaultAction < 1 || r.DefaultAction > 3 || r.DerankWeight < 0 || r.DerankWeight > 10000 || r.Priority < -1000000 || r.Priority > 1000000 {
		return c.ErrInvalid
	}
	if r.IsRegex {
		_, _, e := program(r.Pattern)
		return e
	}
	return nil
}

type Result struct {
	RuleID       string `json:"rule_id"`
	RuleName     string `json:"rule_name"`
	Type         int    `json:"type"`
	Action       int    `json:"action"`
	DerankWeight int    `json:"derank_weight"`
	MatchedText  string `json:"matched_text"`
}

// Match evaluates only explicitly supplied text. It never alters posts, applies
// an action, fetches a link or invokes a classifier. RE2 has no backtracking;
// a compiled-instruction/input work bound complements the byte/rule limits.
func Match(ctx context.Context, r Rule, content string) ([]Result, error) {
	out := []Result{}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if len(content) > MaxContentBytes || !utf8.ValidString(content) || Validate(r) != nil {
		return nil, c.ErrInvalid
	}
	matched := ""
	yes := false
	if r.IsRegex {
		rx, n, e := program(r.Pattern)
		if e != nil {
			return nil, e
		}
		if (len(content)+1)*n > MaxEvaluationWork {
			return nil, c.ErrInvalid
		}
		loc := rx.FindStringIndex(content)
		if loc != nil {
			yes = true
			matched = content[loc[0]:loc[1]]
		}
	} else if strings.Contains(strings.ToLower(content), strings.ToLower(r.Pattern)) {
		yes = true
		matched = r.Pattern
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if yes {
		out = append(out, Result{r.ID, r.Name, r.Type, r.DefaultAction, r.DerankWeight, matched})
	}
	return out, nil
}

type Repository interface {
	Rules(context.Context, string, *bool) ([]Rule, error)
	Rule(context.Context, string, string) (Rule, error)
	CreateRule(context.Context, string, Patch) (Rule, error)
	UpdateRule(context.Context, string, string, Patch) (Rule, error)
	DeleteRule(context.Context, string, string) error
	BulkRules(context.Context, string, []Patch) ([]Rule, error)
	TestRule(context.Context, string, string, string) ([]Result, error)
}
type Auth interface {
	Authenticate(*http.Request) (string, error)
}
