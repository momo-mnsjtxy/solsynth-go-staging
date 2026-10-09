// SPDX-License-Identifier: AGPL-3.0-only
package posts

import "time"

// These owned review fields are available only to current publisher editors or
// through guarded canonical administration. They never imply classifier approval.
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
