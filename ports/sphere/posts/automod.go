// SPDX-License-Identifier: AGPL-3.0-only
package posts

import "time"

// Full automatic-rule evidence is reserved for canonical global administrators
// with the current named post and rule-management permissions. Publisher editors
// receive only EditorAutomodSummary. Neither projection implies classifier approval.
type AutomodMatch struct {
	RuleID       string `json:"rule_id"`
	RuleName     string `json:"rule_name"`
	Type         int    `json:"type"`
	Action       int    `json:"action"`
	DerankWeight int    `json:"derank_weight"`
	MatchedText  string `json:"matched_text"`
}
type AutomodDecision struct {
	Stale            bool                 `json:"stale"`
	EvaluationWork   int                  `json:"-"`
	Revision         int64                `json:"revision"`
	Generation       int64                `json:"generation"`
	RulesFingerprint string               `json:"rules_fingerprint"`
	ContentHash      string               `json:"content_hash"`
	Outcome          string               `json:"outcome"`
	Hidden           bool                 `json:"hidden"`
	Penalty          int                  `json:"penalty"`
	Flagged          bool                 `json:"flagged"`
	Matches          []AutomodMatch       `json:"matches"`
	EvaluatedAt      time.Time            `json:"evaluated_at"`
	Classifier       string               `json:"classifier"`
	Review           *AutomodReviewRecord `json:"review,omitempty"`
}
type AutomodReviewRecord struct {
	BaseRevision int64     `json:"base_revision"`
	ActorID      string    `json:"actor_id"`
	Reason       string    `json:"reason"`
	At           time.Time `json:"at"`
}

// EditorAutomodSummary copies only a post owner's necessary outcome. Global
// rule identities, matching excerpts, policy hashes and reviewer data remain
// in canonical administrative evidence, never in ordinary post projections.
func EditorAutomodSummary(d *AutomodDecision) *AutomodDecision {
	if d == nil {
		return nil
	}
	return &AutomodDecision{Stale: d.Stale, Revision: d.Revision, Outcome: d.Outcome, Hidden: d.Hidden, Penalty: d.Penalty, Flagged: d.Flagged, EvaluatedAt: d.EvaluatedAt, Classifier: d.Classifier, Matches: []AutomodMatch{}}
}
func RedactAutomodForEditor(p *Post) {
	seen := map[*Post]bool{}
	var visit func(*Post)
	visit = func(v *Post) {
		if v == nil || seen[v] {
			return
		}
		seen[v] = true
		v.Automod = EditorAutomodSummary(v.Automod)
		visit(v.RepliedPost)
		visit(v.ForwardedPost)
		visit(v.ChainedPost)
		for i := range v.ChainedPosts {
			visit(&v.ChainedPosts[i])
		}
	}
	visit(p)
}
