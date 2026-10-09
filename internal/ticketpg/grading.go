// SPDX-License-Identifier: AGPL-3.0-only
package ticketpg

import (
	"crypto/rand"
	"math"
	"math/big"
	"sort"
	"strings"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/tickets"
)

func shuffle[T any](v []T) error {
	for i := len(v) - 1; i > 0; i-- {
		n, e := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if e != nil {
			return e
		}
		j := int(n.Int64())
		v[i], v[j] = v[j], v[i]
	}
	return nil
}
func balanced(source []tickets.Question, count int) (out []tickets.Question, e error) {
	buckets := map[string][]tickets.Question{}
	for _, q := range source {
		key := ""
		if q.Category != nil {
			key = strings.ToLower(strings.TrimSpace(*q.Category))
		}
		buckets[key] = append(buckets[key], q)
	}
	keys := []string{}
	for key, v := range buckets {
		if e = shuffle(v); e != nil {
			return nil, e
		}
		buckets[key] = v
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out = []tickets.Question{}
	for len(out) < count && len(keys) > 0 {
		if e = shuffle(keys); e != nil {
			return nil, e
		}
		next := []string{}
		for _, key := range keys {
			v := buckets[key]
			if len(out) < count && len(v) > 0 {
				out = append(out, v[0])
				v = v[1:]
				buckets[key] = v
			}
			if len(v) > 0 {
				next = append(next, key)
			}
		}
		keys = next
	}
	return out, nil
}
func pick(source []tickets.Question, count, simplePercentage int) ([]tickets.Question, error) {
	simple, hard := []tickets.Question{}, []tickets.Question{}
	for _, q := range source {
		if q.Difficulty <= 2 {
			simple = append(simple, q)
		} else {
			hard = append(hard, q)
		}
	}
	a, e := balanced(simple, int(math.Floor(float64(count)*float64(simplePercentage)/100+0.5)))
	if e != nil {
		return nil, e
	}
	b, e := balanced(hard, count-len(a))
	if e != nil {
		return nil, e
	}
	a = append(a, b...)
	if len(a) < count {
		seen := map[string]bool{}
		for _, q := range a {
			seen[q.ID] = true
		}
		remaining := []tickets.Question{}
		for _, q := range source {
			if !seen[q.ID] {
				remaining = append(remaining, q)
			}
		}
		b, e = balanced(remaining, count-len(a))
		if e != nil {
			return nil, e
		}
		a = append(a, b...)
	}
	return a, nil
}
func selectSnapshot(t tickets.Test, previous map[string]bool, selected []string, trial bool) (snapshot tickets.Snapshot, e error) {
	if len(selected) > 5 {
		return snapshot, tickets.ErrInvalid
	}
	cats := map[string]bool{}
	available := map[string]bool{}
	for _, s := range categories(t) {
		available[strings.ToLower(s)] = true
	}
	if t.AllowCategorySelection {
		for _, s := range selected {
			s = strings.ToLower(strings.TrimSpace(s))
			if s == "" || !available[s] {
				return snapshot, tickets.ErrInvalid
			}
			cats[s] = true
		}
		if trial && len(cats) == 0 {
			cats = available
		} else if len(cats) < 3 || len(cats) > 5 {
			return snapshot, tickets.ErrInvalid
		}
	} else if len(selected) > 0 {
		return snapshot, tickets.ErrUnsupported
	}
	snapshot = tickets.Snapshot{Key: t.Key, Title: t.Title, PassingScore: t.PassingScore, RewardExperience: t.RewardExperience, GrantedPermissionGroupKey: t.GrantedPermissionGroupKey, SelectedCategories: []string{}, Questions: []tickets.Question{}}
	for key := range cats {
		snapshot.SelectedCategories = append(snapshot.SelectedCategories, key)
	}
	sort.Strings(snapshot.SelectedCategories)
	for _, g := range t.Groups {
		for _, q := range g.Questions {
			if len(cats) > 0 && (q.Category == nil || !cats[strings.ToLower(strings.TrimSpace(*q.Category))]) {
				continue
			}
			snapshot.Questions = append(snapshot.Questions, q)
		}
	}
	if len(snapshot.Questions) == 0 || len(snapshot.Questions) > tickets.MaxQuestions {
		return snapshot, tickets.ErrInvalid
	}
	if t.ShuffleQuestions {
		count := len(snapshot.Questions)
		if t.RandomQuestionCount != nil {
			count = min(count, *t.RandomQuestionCount)
		}
		if count < len(snapshot.Questions) {
			unseen := []tickets.Question{}
			for _, q := range snapshot.Questions {
				if !previous[q.ID] {
					unseen = append(unseen, q)
				}
			}
			chosen, e := pick(unseen, min(count, len(unseen)), t.SimpleQuestionPercentage)
			if e != nil {
				return snapshot, e
			}
			if len(chosen) < count {
				seen := map[string]bool{}
				for _, q := range chosen {
					seen[q.ID] = true
				}
				remaining := []tickets.Question{}
				for _, q := range snapshot.Questions {
					if !seen[q.ID] {
						remaining = append(remaining, q)
					}
				}
				rest, e := pick(remaining, count-len(chosen), t.SimpleQuestionPercentage)
				if e != nil {
					return snapshot, e
				}
				chosen = append(chosen, rest...)
			}
			snapshot.Questions = chosen
		}
		if e = shuffle(snapshot.Questions); e != nil {
			return snapshot, e
		}
	}
	for i := range snapshot.Questions {
		snapshot.Questions[i].Config = map[string]any{}
		snapshot.Questions[i].Choices = append([]tickets.Choice{}, snapshot.Questions[i].Choices...)
		for j := range snapshot.Questions[i].Choices {
			snapshot.Questions[i].Choices[j].Config = map[string]any{}
		}
		if e = shuffle(snapshot.Questions[i].Choices); e != nil {
			return snapshot, e
		}
	}
	return snapshot, nil
}
func normalizeAnswers(snapshot tickets.Snapshot, inputs []tickets.AnswerInput) ([]tickets.AnswerInput, error) {
	if len(inputs) > tickets.MaxQuestions {
		return nil, tickets.ErrInvalid
	}
	questions := map[string]tickets.Question{}
	for _, q := range snapshot.Questions {
		questions[q.ID] = q
	}
	seen := map[string]bool{}
	out := make([]tickets.AnswerInput, 0, len(inputs))
	for _, v := range inputs {
		q, ok := questions[v.QuestionID]
		if !ok || seen[v.QuestionID] || !authority.ValidID(v.QuestionID) || len(v.ChoiceIDs) > tickets.MaxChoices || !tickets.OptionalText(v.Text, 16384) {
			return nil, tickets.ErrInvalid
		}
		seen[v.QuestionID] = true
		choices := map[string]bool{}
		for _, ch := range q.Choices {
			choices[ch.ID] = true
		}
		selected := map[string]bool{}
		v.ChoiceIDs = append([]string{}, v.ChoiceIDs...)
		for _, id := range v.ChoiceIDs {
			if !choices[id] || selected[id] {
				return nil, tickets.ErrInvalid
			}
			selected[id] = true
		}
		if q.Type == tickets.FreeText && len(v.ChoiceIDs) > 0 {
			return nil, tickets.ErrInvalid
		}
		sort.Strings(v.ChoiceIDs)
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].QuestionID < out[j].QuestionID })
	return out, nil
}
func grade(snapshot tickets.Snapshot, inputs []tickets.AnswerInput) ([]tickets.Answer, int, *float64, error) {
	normalized, e := normalizeAnswers(snapshot, inputs)
	if e != nil {
		return nil, 0, nil, e
	}
	byID := map[string]tickets.AnswerInput{}
	for _, v := range normalized {
		byID[v.QuestionID] = v
	}
	answers := []tickets.Answer{}
	pending := false
	for _, q := range snapshot.Questions {
		input := byID[q.ID]
		if input.ChoiceIDs == nil {
			input.ChoiceIDs = []string{}
		}
		answer := tickets.Answer{QuestionID: q.ID, Value: tickets.AnswerValue{ChoiceIDs: input.ChoiceIDs, Text: input.Text}}
		answered := len(input.ChoiceIDs) > 0 || input.Text != nil && strings.TrimSpace(*input.Text) != ""
		if !answered {
			zero := float64(0)
			answer.AwardedPoints = &zero
		} else if q.GradingMode == tickets.Auto {
			correct := []string{}
			for _, c := range q.Choices {
				if c.IsCorrect {
					correct = append(correct, c.ID)
				}
			}
			sort.Strings(correct)
			ok := len(correct) == len(input.ChoiceIDs)
			if ok {
				for i, id := range correct {
					if id != input.ChoiceIDs[i] {
						ok = false
						break
					}
				}
			}
			points := -q.Points
			if ok {
				points = q.Points
			}
			answer.IsCorrect = &ok
			answer.AwardedPoints = &points
		} else {
			pending = true
		}
		answers = append(answers, answer)
	}
	if pending {
		return answers, tickets.PendingReview, nil, nil
	}
	score := score(snapshot, answers)
	status := tickets.Failed
	if score >= snapshot.PassingScore {
		status = tickets.Passed
	}
	return answers, status, &score, nil
}
func score(snapshot tickets.Snapshot, answers []tickets.Answer) float64 {
	awarded := map[string]float64{}
	for _, a := range answers {
		if a.AwardedPoints != nil {
			awarded[a.QuestionID] = *a.AwardedPoints
		}
	}
	possible, total := float64(0), float64(0)
	for _, q := range snapshot.Questions {
		possible += q.Points
		total += awarded[q.ID]
	}
	if possible == 0 {
		return 100
	}
	return math.Min(100, total/possible*100)
}
func gradeTrial(t tickets.Test, inputs []tickets.AnswerInput) (tickets.Result, error) {
	var out tickets.Result
	questions := map[string]tickets.Question{}
	for _, g := range t.Groups {
		for _, q := range g.Questions {
			questions[q.ID] = q
		}
	}
	snapshot := tickets.Snapshot{PassingScore: t.PassingScore, Questions: []tickets.Question{}}
	for _, v := range inputs {
		q, ok := questions[v.QuestionID]
		if !ok {
			return out, tickets.ErrInvalid
		}
		snapshot.Questions = append(snapshot.Questions, q)
	}
	answers, _, _, e := grade(snapshot, inputs)
	if e != nil {
		return out, e
	}
	possible := float64(0)
	total := float64(0)
	for _, q := range snapshot.Questions {
		if q.GradingMode == tickets.Auto {
			possible += q.Points
		}
	}
	for i, a := range answers {
		q := questions[a.QuestionID]
		if q.GradingMode == tickets.Manual {
			answers[i].AwardedPoints = nil
			answers[i].IsCorrect = nil
		} else if a.AwardedPoints != nil {
			total += *a.AwardedPoints
		}
	}
	var score *float64
	passed := false
	if possible != 0 {
		x := total / possible * 100
		score = &x
		passed = x >= t.PassingScore
	}
	out.Value = map[string]any{"score": score, "passed": passed, "answers": answers}
	return out, nil
}
