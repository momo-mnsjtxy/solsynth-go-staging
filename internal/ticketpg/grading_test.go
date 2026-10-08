// SPDX-License-Identifier: AGPL-3.0-only
package ticketpg

import (
	"encoding/json"
	"solsynth.local/go-migration/ports/passport/tickets"
	"strings"
	"testing"
)

const q1 = "10000000-0000-4000-8000-000000000001"
const q2 = "10000000-0000-4000-8000-000000000002"
const good = "20000000-0000-4000-8000-000000000001"
const bad = "20000000-0000-4000-8000-000000000002"

func gradingSnapshot() tickets.Snapshot {
	return tickets.Snapshot{PassingScore: 60, Questions: []tickets.Question{{ID: q1, Content: "Choice", Type: tickets.SingleChoice, GradingMode: tickets.Auto, Points: 2, Choices: []tickets.Choice{{ID: good, Content: "Good", IsCorrect: true}, {ID: bad, Content: "Bad"}}}, {ID: q2, Content: "Text", Type: tickets.FreeText, GradingMode: tickets.Manual, Points: 1}}}
}
func TestAuthoredPositiveNegativeAndManualGrading(t *testing.T) {
	s := gradingSnapshot()
	a, status, value, e := grade(s, []tickets.AnswerInput{{QuestionID: q1, ChoiceIDs: []string{good}}})
	if e != nil || status != tickets.Passed || value == nil || *value != float64(2)/3*100 || a[1].AwardedPoints == nil || *a[1].AwardedPoints != 0 {
		t.Fatal(a, status, value, e)
	}
	a, status, value, e = grade(s, []tickets.AnswerInput{{QuestionID: q1, ChoiceIDs: []string{bad}}})
	if e != nil || status != tickets.Failed || value == nil || *value != -float64(2)/3*100 || a[0].IsCorrect == nil || *a[0].IsCorrect {
		t.Fatal(a, status, value, e)
	}
	text := "answer"
	_, status, value, e = grade(s, []tickets.AnswerInput{{QuestionID: q1, ChoiceIDs: []string{good}}, {QuestionID: q2, Text: &text}})
	if e != nil || status != tickets.PendingReview || value != nil {
		t.Fatal(status, value, e)
	}
	for _, inputs := range [][]tickets.AnswerInput{{{QuestionID: q1}, {QuestionID: q1}}, {{QuestionID: q1, ChoiceIDs: []string{good, good}}}, {{QuestionID: q1, ChoiceIDs: []string{q2}}}, {{QuestionID: q2, ChoiceIDs: []string{good}}}} {
		if _, _, _, e := grade(s, inputs); e == nil {
			t.Fatal("invalid answer accepted", inputs)
		}
	}
}
func TestParticipantQuestionsNeverIncludeAnswerKeys(t *testing.T) {
	s := gradingSnapshot()
	s.Questions[0].Config = map[string]any{"answer": "secret"}
	s.Questions[0].Choices[0].Config = map[string]any{"correct": true}
	b, e := json.Marshal(participantQuestions(s.Questions))
	if e != nil {
		t.Fatal(e)
	}
	for _, secret := range []string{"is_correct", "grading_mode", "secret", "\"correct\""} {
		if strings.Contains(string(b), secret) {
			t.Fatal("participant key leak", string(b))
		}
	}
}
func TestSnapshotSelectionBalancesUnseenCategoriesAndIsImmutable(t *testing.T) {
	test := tickets.Test{TestInput: tickets.DefaultTestInput()}
	test.Key = "entry"
	test.Title = "Entry"
	test.AllowCategorySelection = true
	test.ShuffleQuestions = true
	n := 3
	test.RandomQuestionCount = &n
	test.Groups = []tickets.Group{{}}
	prior := map[string]bool{}
	for i, cat := range []string{"a", "b", "c", "a", "b", "c"} {
		id := "10000000-0000-4000-8000-00000000000" + string(rune('1'+i))
		q := tickets.Question{ID: id, Category: &cat, Difficulty: 1, Choices: []tickets.Choice{{ID: good, Content: "stable", IsCorrect: true}}}
		test.Groups[0].Questions = append(test.Groups[0].Questions, q)
		if i < 3 {
			prior[id] = true
		}
	}
	snapshot, e := selectSnapshot(test, prior, []string{"a", "b", "c"}, false)
	if e != nil || len(snapshot.Questions) != 3 {
		t.Fatal(snapshot, e)
	}
	categories := map[string]bool{}
	for _, q := range snapshot.Questions {
		if prior[q.ID] {
			t.Fatal("reused question with enough unseen candidates", q.ID)
		}
		categories[*q.Category] = true
	}
	if len(categories) != 3 {
		t.Fatal(categories)
	}
	snapshot.Questions[0].Choices[0].Content = "changed"
	for _, q := range test.Groups[0].Questions {
		if q.Choices[0].Content != "stable" {
			t.Fatal("snapshot changed bank")
		}
	}
	if _, e = selectSnapshot(test, nil, []string{"a"}, false); e == nil {
		t.Fatal("too few categories accepted")
	}
}
func TestZeroPointScoreMatchesSource(t *testing.T) {
	s := tickets.Snapshot{PassingScore: 100, Questions: []tickets.Question{{ID: q1, Points: 0, Type: tickets.SingleChoice, GradingMode: tickets.Auto, Choices: []tickets.Choice{{ID: good, IsCorrect: true}}}}}
	_, status, score, e := grade(s, nil)
	if e != nil || status != tickets.Passed || score == nil || *score != 100 {
		t.Fatal(status, score, e)
	}
}
