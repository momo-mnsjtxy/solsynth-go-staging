// SPDX-License-Identifier: AGPL-3.0-only
package tickets

import (
	"encoding/json"
	"math"
	"regexp"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"unicode/utf8"
)

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func ValidKey(s string) bool { return keyPattern.MatchString(s) }
func Text(s string, min, max int) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0) && utf8.RuneCountInString(s) <= max && utf8.RuneCountInString(strings.TrimSpace(s)) >= min
}
func OptionalText(s *string, max int) bool { return s == nil || Text(*s, 0, max) }
func validConfig(c map[string]any) bool    { b, e := json.Marshal(c); return e == nil && len(b) <= 8192 }
func ValidIdempotency(s string) bool {
	if len(s) > 128 {
		return false
	}
	for _, c := range s {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}
func ValidQuery(q Query) bool {
	return q.Take >= 1 && q.Take <= 100 && q.Offset >= 0 && q.Offset <= 100000 && (q.CreatorID == "" || authority.ValidID(q.CreatorID)) && (q.AssigneeID == "" || authority.ValidID(q.AssigneeID))
}
func ValidResources(v []*string) bool {
	if len(v) > 32 {
		return false
	}
	for _, s := range v {
		if !OptionalText(s, 2048) {
			return false
		}
	}
	return true
}
func ValidateTicket(c TicketCreate) error {
	if len(c.FileIDs) > 0 {
		return ErrUnsupported
	}
	if !Text(c.Title, 3, 256) || !Text(c.Content, 1, 16384) || c.Type < 0 || c.Type > 3 || c.Priority < 0 || c.Priority > 3 || !ValidResources(c.Resources) {
		return ErrInvalid
	}
	return nil
}
func ValidateGroup(g GroupInput) error {
	if !ValidKey(g.Key) || !Text(g.Title, 1, 256) || !OptionalText(g.Description, 4096) || !validConfig(g.Config) {
		return ErrInvalid
	}
	return nil
}
func ValidateQuestion(q QuestionInput) error {
	if !ValidKey(q.QuestionGroupKey) || !Text(q.Content, 1, 8192) || !OptionalText(q.Category, 128) || q.Type < 0 || q.Type > 2 || q.GradingMode < 0 || q.GradingMode > 1 || q.Difficulty < 0 || q.Difficulty > 100 || math.IsNaN(q.Points) || math.IsInf(q.Points, 0) || q.Points < 0 || q.Points > 10000 || !validConfig(q.Config) || len(q.Choices) > MaxChoices {
		return ErrInvalid
	}
	if q.SortOrder != nil && (*q.SortOrder < 0 || *q.SortOrder > 100000) {
		return ErrInvalid
	}
	correct := 0
	seen := map[string]bool{}
	for _, c := range q.Choices {
		if !Text(c.Content, 1, 4096) || c.SortOrder < 0 || c.SortOrder > 100000 || !validConfig(c.Config) {
			return ErrInvalid
		}
		if c.ID != nil {
			if !authority.ValidID(*c.ID) || seen[*c.ID] {
				return ErrInvalid
			}
			seen[*c.ID] = true
		}
		if c.IsCorrect {
			correct++
		}
	}
	if q.Type == FreeText {
		if q.GradingMode == Auto || len(q.Choices) > 0 {
			return ErrInvalid
		}
	} else if q.GradingMode == Auto && (correct == 0 || q.Type == SingleChoice && correct != 1) {
		return ErrInvalid
	}
	return nil
}
func ValidateTest(t TestInput) error {
	if !ValidKey(t.Key) || !Text(t.Title, 1, 256) || !OptionalText(t.Description, 4096) || !validConfig(t.Config) || len(t.QuestionGroups) == 0 || len(t.QuestionGroups) > 16 || math.IsNaN(t.PassingScore) || math.IsInf(t.PassingScore, 0) || t.PassingScore < 0 || t.PassingScore > 100 || t.AttemptPeriodDays < 1 || t.AttemptPeriodDays > 3660 || t.SimpleQuestionPercentage < 0 || t.SimpleQuestionPercentage > 100 {
		return ErrInvalid
	}
	if t.MaxAttempts != nil && (*t.MaxAttempts < 1 || *t.MaxAttempts > 1000) || t.TimeLimitSeconds != nil && (*t.TimeLimitSeconds < 1 || *t.TimeLimitSeconds > 86400) || t.RandomQuestionCount != nil && (*t.RandomQuestionCount < 1 || *t.RandomQuestionCount > MaxQuestions) || t.RewardExperience != nil && (*t.RewardExperience < 1 || *t.RewardExperience > 1000000000) {
		return ErrInvalid
	}
	if t.GrantedPermissionGroupKey != nil && !ValidKey(*t.GrantedPermissionGroupKey) {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, g := range t.QuestionGroups {
		key := strings.ToLower(g.QuestionGroupKey)
		if !ValidKey(g.QuestionGroupKey) || seen[key] || g.SortOrder < 0 || g.SortOrder > 100000 {
			return ErrInvalid
		}
		seen[key] = true
	}
	return nil
}
func ValidateReview(v ReviewInput) error {
	if math.IsNaN(v.AwardedPoints) || math.IsInf(v.AwardedPoints, 0) || !OptionalText(v.Note, 4096) {
		return ErrInvalid
	}
	return nil
}
