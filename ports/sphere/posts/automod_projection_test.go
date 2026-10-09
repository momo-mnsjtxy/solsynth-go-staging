// SPDX-License-Identifier: AGPL-3.0-only
package posts

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestEditorAutomodProjectionOmitsGlobalEvidenceRecursively(t *testing.T) {
	at := time.Unix(1234, 0).UTC()
	full := &AutomodDecision{Stale: true, EvaluationWork: 123, Revision: 7, Generation: 42, Outcome: "hidden", Hidden: true, Penalty: 100, Flagged: true, RulesFingerprint: "private-rule-fingerprint", ContentHash: "private-content-hash", Matches: []AutomodMatch{{RuleID: "private-rule-id", RuleName: "private-rule-name", MatchedText: "private-match"}}, EvaluatedAt: at, Classifier: "unavailable", Review: &AutomodReviewRecord{ActorID: "private-reviewer", Reason: "private-review-reason"}}
	before, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	child := &Post{Automod: full}
	p := Post{Automod: full, RepliedPost: child, ForwardedPost: child, ChainedPost: &Post{Automod: full}, ChainedPosts: []Post{{Automod: full, RepliedPost: &Post{Automod: full}}}}
	RedactAutomodForEditor(&p)
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-rule", "private-content", "private-match", "private-review"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("global evidence escaped ordinary projection")
		}
	}
	want := &AutomodDecision{Stale: true, Revision: 7, Outcome: "hidden", Hidden: true, Penalty: 100, Flagged: true, Matches: []AutomodMatch{}, EvaluatedAt: at, Classifier: "unavailable"}
	for _, node := range []*Post{&p, p.RepliedPost, p.ForwardedPost, p.ChainedPost, &p.ChainedPosts[0], p.ChainedPosts[0].RepliedPost} {
		if node.Automod == full || !reflect.DeepEqual(node.Automod, want) {
			t.Fatal("summary changed public outcome or retained private evidence")
		}
	}
	after, err := json.Marshal(full)
	if err != nil || string(before) != string(after) {
		t.Fatal("projection mutated canonical decision", err)
	}
	p.Automod.Hidden = false
	p.Automod.Matches = append(p.Automod.Matches, AutomodMatch{RuleID: "new"})
	if !full.Hidden || len(full.Matches) != 1 || full.Matches[0].RuleID != "private-rule-id" {
		t.Fatal("summary retained mutable aliases to canonical decision")
	}
}

func TestEditorAutomodProjectionHandlesCyclesAndAbsentEvidence(t *testing.T) {
	RedactAutomodForEditor(nil)
	if EditorAutomodSummary(nil) != nil {
		t.Fatal("missing evidence became a successful decision")
	}
	full := &AutomodDecision{Outcome: "flagged", Flagged: true, Matches: []AutomodMatch{{RuleID: "private"}}}
	p := &Post{Automod: full}
	p.RepliedPost = p
	p.ChainedPosts = []Post{{Automod: full, ForwardedPost: p}, {}}
	RedactAutomodForEditor(p)
	RedactAutomodForEditor(p)
	if p.RepliedPost != p || p.ChainedPosts[0].ForwardedPost != p || p.ChainedPosts[1].Automod != nil || !p.Automod.Flagged || p.Automod.Hidden || p.Automod.Penalty != 0 || len(p.Automod.Matches) != 0 || len(full.Matches) != 1 {
		t.Fatal("recursive projection changed the graph, outcome or canonical evidence")
	}
}
