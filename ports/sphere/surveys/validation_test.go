// SPDX-License-Identifier: AGPL-3.0-only
package surveys

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQuestionAndAnswerValidation(t *testing.T) {
	q := Question{ID: "10000000-0000-4000-8000-000000000001", Type: MultipleChoice, Title: "Pick", Required: true, Options: []Option{{ID: "10000000-0000-4000-8000-000000000002", Label: "A"}, {ID: "10000000-0000-4000-8000-000000000003", Label: "B"}}}
	s := Survey{Questions: []Question{q}}
	if e := Validate(s); e != nil {
		t.Fatal(e)
	}
	for _, raw := range []string{`["10000000-0000-4000-8000-000000000002","10000000-0000-4000-8000-000000000002"]`, `[true]`, `[]`, `null`, `"10000000-0000-4000-8000-000000000002"`} {
		if e := ValidateAnswer(s, map[string]json.RawMessage{q.ID: json.RawMessage(raw)}); e == nil {
			t.Fatal("accepted", raw)
		}
	}
	a := map[string]json.RawMessage{q.ID: json.RawMessage(`["10000000-0000-4000-8000-000000000002"]`)}
	if e := ValidateAnswer(s, a); e != nil {
		t.Fatal(e)
	}
	a["unknown"] = json.RawMessage(`true`)
	if e := ValidateAnswer(s, a); e == nil {
		t.Fatal("unknown question accepted")
	}
	s.Questions[0].Options[1].ID = q.Options[0].ID
	if e := Validate(s); e == nil {
		t.Fatal("duplicate option IDs")
	}
}
func TestJSONRejectsDuplicateKeysAndNestedUnknowns(t *testing.T) {
	for _, body := range []string{`{"questions":[],"questions":[]}`, `{"questions":[{"title":"a","title":"b"}]}`, `{"questions":[{"made_up":true}]}`, `{} {}`, `null`} {
		r := httptest.NewRequest("POST", "/api/surveys", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		var p Patch
		if e := decode(httptest.NewRecorder(), r, &p); e == nil {
			t.Fatal("accepted", body)
		}
	}
}
func TestDocumentedNumericEnums(t *testing.T) {
	if Draft != 0 || Published != 1 || Archived != 2 || SingleChoice != 0 || MultipleChoice != 1 || YesNo != 2 || Rating != 3 || FreeText != 4 {
		t.Fatal("numeric contract drift")
	}
}
