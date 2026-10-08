// SPDX-License-Identifier: AGPL-3.0-only
package ticketpg

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/tickets"
)

type Export struct {
	Data []byte
	Name string
}

func loadGroup(ctx context.Context, tx *sql.Tx, key string, lock bool) (g tickets.Group, e error) {
	var b []byte
	q := `SELECT id::text,data FROM owned_exam_groups WHERE key=$1`
	if lock {
		q += ` FOR UPDATE`
	}
	e = tx.QueryRowContext(ctx, q, key).Scan(&g.ID, &b)
	if e != nil {
		return g, mapped(e)
	}
	if e = json.Unmarshal(b, &g.GroupInput); e != nil {
		return g, e
	}
	return g, nil
}
func groupQuestions(ctx context.Context, tx *sql.Tx, g *tickets.Group) error {
	rows, e := tx.QueryContext(ctx, `SELECT data FROM owned_exam_questions WHERE group_id=$1 ORDER BY sort_order,id LIMIT 200`, g.ID)
	if e != nil {
		return e
	}
	defer rows.Close()
	g.Questions = []tickets.Question{}
	for rows.Next() {
		var b []byte
		var q tickets.Question
		if e = rows.Scan(&b); e != nil {
			return e
		}
		if e = json.Unmarshal(b, &q); e != nil {
			return e
		}
		g.Questions = append(g.Questions, q)
	}
	g.QuestionCount = len(g.Questions)
	return rows.Err()
}
func loadTest(ctx context.Context, tx *sql.Tx, key string, lock bool) (t tickets.Test, e error) {
	var b []byte
	q := `SELECT id::text,data,is_published,is_listed,is_archived,created_at,updated_at FROM owned_exams WHERE key=$1`
	if lock {
		q += ` FOR UPDATE`
	}
	var published, listed bool
	e = tx.QueryRowContext(ctx, q, key).Scan(&t.ID, &b, &published, &listed, &t.IsArchived, &t.CreatedAt, &t.UpdatedAt)
	if e != nil {
		return t, mapped(e)
	}
	if e = json.Unmarshal(b, &t.TestInput); e != nil {
		return t, e
	}
	t.IsPublished = published
	t.IsListed = listed
	rows, e := tx.QueryContext(ctx, `SELECT g.id::text,g.data,l.sort_order FROM owned_exam_group_links l JOIN owned_exam_groups g ON g.id=l.group_id WHERE l.test_id=$1 ORDER BY l.sort_order,g.id LIMIT 16`, t.ID)
	if e != nil {
		return t, e
	}
	t.Groups = []tickets.Group{}
	t.QuestionGroups = []tickets.Assignment{}
	for rows.Next() {
		var g tickets.Group
		var data []byte
		var order int
		if e = rows.Scan(&g.ID, &data, &order); e != nil {
			rows.Close()
			return t, e
		}
		if e = json.Unmarshal(data, &g.GroupInput); e != nil {
			rows.Close()
			return t, e
		}
		t.Groups = append(t.Groups, g)
		t.QuestionGroups = append(t.QuestionGroups, tickets.Assignment{QuestionGroupKey: g.Key, SortOrder: order})
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return t, e
	}
	for i := range t.Groups {
		if e = groupQuestions(ctx, tx, &t.Groups[i]); e != nil {
			return t, e
		}
	}
	return t, nil
}
func categories(t tickets.Test) []string {
	seen := map[string]string{}
	for _, g := range t.Groups {
		for _, q := range g.Questions {
			if q.Category != nil {
				v := strings.TrimSpace(*q.Category)
				if v != "" {
					seen[strings.ToLower(v)] = v
				}
			}
		}
	}
	out := []string{}
	for _, v := range seen {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}
func participantQuestions(qs []tickets.Question) []tickets.ParticipantQuestion {
	out := []tickets.ParticipantQuestion{}
	for _, q := range qs {
		p := tickets.ParticipantQuestion{ID: q.ID, Content: q.Content, Category: q.Category, Type: q.Type, Difficulty: q.Difficulty, Points: q.Points, Config: map[string]any{}, Choices: []tickets.ParticipantChoice{}}
		for _, c := range q.Choices {
			p.Choices = append(p.Choices, tickets.ParticipantChoice{ID: c.ID, Content: c.Content, Config: map[string]any{}})
		}
		out = append(out, p)
	}
	return out
}
func participantTest(t tickets.Test, questions []tickets.Question) tickets.ParticipantTest {
	return tickets.ParticipantTest{Key: t.Key, Title: t.Title, Description: t.Description, TimeLimitSeconds: t.TimeLimitSeconds, RewardExperience: t.RewardExperience, MaxAttempts: t.MaxAttempts, AllowCategorySelection: t.AllowCategorySelection, AvailableCategories: categories(t), Questions: participantQuestions(questions)}
}
func (r *Repository) bank(ctx context.Context, tx *sql.Tx, actor string, c tickets.Command, now time.Time) (out tickets.Result, e error) {
	switch c.Operation {
	case "group.list":
		if !tickets.ValidQuery(c.Query) {
			return out, tickets.ErrInvalid
		}
		rows, e := tx.QueryContext(ctx, `SELECT id::text,data FROM owned_exam_groups ORDER BY key LIMIT $1 OFFSET $2`, c.Query.Take, c.Query.Offset)
		if e != nil {
			return out, e
		}
		v := []tickets.Group{}
		for rows.Next() {
			var g tickets.Group
			var b []byte
			if e = rows.Scan(&g.ID, &b); e != nil {
				rows.Close()
				return out, e
			}
			if e = json.Unmarshal(b, &g.GroupInput); e != nil {
				rows.Close()
				return out, e
			}
			v = append(v, g)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return out, e
		}
		for i := range v {
			if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_exam_questions WHERE group_id=$1`, v[i].ID).Scan(&v[i].QuestionCount); e != nil {
				return out, e
			}
		}
		out.Value = v
		return out, nil
	case "group.save":
		if c.Group == nil {
			return out, tickets.ErrInvalid
		}
		g := tickets.Group{GroupInput: *c.Group}
		if g.Config == nil {
			g.Config = map[string]any{}
		}
		if e = tickets.ValidateGroup(g.GroupInput); e != nil {
			return out, e
		}
		if c.Key != "" {
			prior, e := loadGroup(ctx, tx, c.Key, true)
			if e != nil {
				return out, e
			}
			g.ID = prior.ID
		} else {
			g.ID, e = uuid()
			if e != nil {
				return out, e
			}
		}
		b, e := marshal(g.GroupInput)
		if e != nil {
			return out, e
		}
		if c.Key == "" {
			_, e = tx.ExecContext(ctx, `INSERT INTO owned_exam_groups(id,key,data)VALUES($1,$2,$3)`, g.ID, g.Key, b)
		} else {
			_, e = tx.ExecContext(ctx, `UPDATE owned_exam_groups SET key=$2,data=$3 WHERE id=$1`, g.ID, g.Key, b)
		}
		if e != nil {
			return out, e
		}
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_exam_questions WHERE group_id=$1`, g.ID).Scan(&g.QuestionCount); e != nil {
			return out, e
		}
		out.Value = g
		out.Changed = true
		out.ResourceID = g.ID
		return out, nil
	case "group.delete":
		g, e := loadGroup(ctx, tx, c.Key, true)
		if e != nil {
			return out, e
		}
		var linked bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_exam_group_links WHERE group_id=$1)`, g.ID).Scan(&linked); e != nil {
			return out, e
		}
		if linked {
			return out, tickets.ErrConflict
		}
		if _, e = tx.ExecContext(ctx, `DELETE FROM owned_exam_questions WHERE group_id=$1`, g.ID); e != nil {
			return out, e
		}
		_, e = tx.ExecContext(ctx, `DELETE FROM owned_exam_groups WHERE id=$1`, g.ID)
		out.Changed = true
		out.ResourceID = g.ID
		return out, e
	case "question.list":
		if !tickets.ValidQuery(c.Query) || !tickets.ValidKey(c.Query.GroupKey) {
			return out, tickets.ErrInvalid
		}
		g, e := loadGroup(ctx, tx, c.Query.GroupKey, false)
		if e != nil {
			return out, e
		}
		if e = groupQuestions(ctx, tx, &g); e != nil {
			return out, e
		}
		end := min(len(g.Questions), c.Query.Offset+c.Query.Take)
		start := min(c.Query.Offset, end)
		out.Value = map[string]any{"total_count": len(g.Questions), "items": g.Questions[start:end]}
		return out, nil
	case "question.save":
		if c.Question == nil {
			return out, tickets.ErrInvalid
		}
		q, e := saveQuestion(ctx, tx, c.ID, *c.Question)
		out.Value = q
		out.Changed = e == nil
		out.ResourceID = q.ID
		return out, e
	case "question.import":
		if !tickets.ValidKey(c.Query.GroupKey) || len(c.Questions) == 0 || len(c.Questions) > 200 {
			return out, tickets.ErrInvalid
		}
		for _, q := range c.Questions {
			q.QuestionGroupKey = c.Query.GroupKey
			if _, e = saveQuestion(ctx, tx, "", q); e != nil {
				return out, e
			}
		}
		out.Value = map[string]int{"imported_count": len(c.Questions)}
		out.Changed = true
		return out, nil
	case "question.delete":
		if !authority.ValidID(c.ID) {
			return out, tickets.ErrNotFound
		}
		var groupID string
		if e = tx.QueryRowContext(ctx, `SELECT group_id::text FROM owned_exam_questions WHERE id=$1`, c.ID).Scan(&groupID); e != nil {
			return out, e
		}
		if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_exam_groups WHERE id=$1 FOR UPDATE`, groupID); e != nil {
			return out, e
		}
		result, e := tx.ExecContext(ctx, `DELETE FROM owned_exam_questions WHERE id=$1 AND group_id=$2`, c.ID, groupID)
		if e != nil {
			return out, e
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			return out, tickets.ErrConflict
		}
		out.Changed = true
		out.ResourceID = c.ID
		return out, nil
	case "question.prune":
		if c.Toggle == nil || !*c.Toggle || !tickets.ValidKey(c.Query.GroupKey) {
			return out, tickets.ErrInvalid
		}
		g, e := loadGroup(ctx, tx, c.Query.GroupKey, true)
		if e != nil {
			return out, e
		}
		result, e := tx.ExecContext(ctx, `DELETE FROM owned_exam_questions WHERE group_id=$1`, g.ID)
		if e != nil {
			return out, e
		}
		n, _ := result.RowsAffected()
		out.Value = map[string]int64{"removed_count": n}
		out.Changed = n > 0
		out.ResourceID = g.ID
		return out, nil
	case "question.export":
		if !tickets.ValidKey(c.Query.GroupKey) {
			return out, tickets.ErrInvalid
		}
		g, e := loadGroup(ctx, tx, c.Query.GroupKey, false)
		if e != nil {
			return out, e
		}
		if e = groupQuestions(ctx, tx, &g); e != nil {
			return out, e
		}
		var b strings.Builder
		w := csv.NewWriter(&b)
		w.Write([]string{"question_group_key", "content", "category", "type", "grading_mode", "difficulty", "points", "choices", "correct_choices"})
		for _, q := range g.Questions {
			choiceTexts, correct := []string{}, []string{}
			for i, ch := range q.Choices {
				choiceTexts = append(choiceTexts, ch.Content)
				if ch.IsCorrect {
					correct = append(correct, fmt.Sprint(i))
				}
			}
			category := ""
			if q.Category != nil {
				category = *q.Category
			}
			w.Write([]string{g.Key, q.Content, category, []string{"single_choice", "multiple_choice", "free_text"}[q.Type], []string{"auto", "manual"}[q.GradingMode], fmt.Sprint(q.Difficulty), fmt.Sprint(q.Points), strings.Join(choiceTexts, "|"), strings.Join(correct, "|")})
		}
		w.Flush()
		if e = w.Error(); e != nil {
			return out, e
		}
		out.Value = Export{Data: []byte(b.String()), Name: "test-question-bank-" + g.Key + ".csv"}
		return out, nil
	case "test.save":
		if c.Test == nil {
			return out, tickets.ErrInvalid
		}
		t, e := r.saveTest(ctx, tx, c.Key, *c.Test, now)
		out.Value = t
		out.Changed = e == nil
		out.ResourceID = t.ID
		return out, e
	case "test.public.list", "test.admin.list":
		if !tickets.ValidQuery(c.Query) {
			return out, tickets.ErrInvalid
		}
		where := ""
		if c.Operation == "test.public.list" {
			where = ` WHERE is_published AND is_listed AND NOT is_archived`
		}
		rows, e := tx.QueryContext(ctx, `SELECT key FROM owned_exams`+where+` ORDER BY key LIMIT $1 OFFSET $2`, c.Query.Take, c.Query.Offset)
		if e != nil {
			return out, e
		}
		keys := []string{}
		for rows.Next() {
			var key string
			if e = rows.Scan(&key); e != nil {
				rows.Close()
				return out, e
			}
			keys = append(keys, key)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return out, e
		}
		if c.Operation == "test.public.list" {
			v := []tickets.ParticipantTest{}
			for _, key := range keys {
				t, e := loadTest(ctx, tx, key, false)
				if e != nil {
					return out, e
				}
				if t.IsPublished && t.IsListed && !t.IsArchived {
					v = append(v, participantTest(t, nil))
				}
			}
			out.Value = v
		} else {
			v := []tickets.Test{}
			for _, key := range keys {
				t, e := loadTest(ctx, tx, key, false)
				if e != nil {
					return out, e
				}
				v = append(v, t)
			}
			out.Value = v
		}
		return out, nil
	case "test.get", "test.trial.preview", "test.publish", "test.archive", "test.trial.create", "test.trial.grade":
		t, e := loadTest(ctx, tx, c.Key, c.Operation != "test.get")
		if e != nil {
			return out, e
		}
		if c.Operation == "test.publish" || c.Operation == "test.archive" {
			if c.Toggle == nil {
				return out, tickets.ErrInvalid
			}
			if c.Operation == "test.publish" {
				if t.IsArchived && *c.Toggle {
					return out, tickets.ErrConflict
				}
				t.IsPublished = *c.Toggle
			} else {
				t.IsArchived = *c.Toggle
				if t.IsArchived {
					t.IsPublished = false
				}
			}
			_, e = tx.ExecContext(ctx, `UPDATE owned_exams SET is_published=$2,is_archived=$3,updated_at=$4 WHERE id=$1`, t.ID, t.IsPublished, t.IsArchived, now)
			t.UpdatedAt = now
			out.Value = t
			out.Changed = true
			out.ResourceID = t.ID
			return out, e
		}
		if t.IsArchived || c.Operation == "test.get" && !t.IsPublished {
			return out, tickets.ErrNotFound
		}
		if c.Operation == "test.get" {
			out.Value = participantTest(t, nil)
			return out, nil
		}
		if c.Operation == "test.trial.preview" {
			snap, e := selectSnapshot(t, nil, nil, true)
			if e != nil {
				return out, e
			}
			out.Value = participantTest(t, snap.Questions)
			return out, nil
		}
		if c.Operation == "test.trial.grade" {
			return gradeTrial(t, c.Answers)
		}
		trial := tickets.TrialInput{Key: t.Key + "-trial", Title: t.Title + " trial", IsPublished: true, TestKey: t.Key}
		existing, e := loadTrial(ctx, tx, trial.Key, false)
		if e == nil {
			out.Value = existing
			return out, nil
		}
		if !errors.Is(e, tickets.ErrNotFound) {
			return out, e
		}
		v, e := saveTrial(ctx, tx, "", trial)
		out.Value = v
		out.ResourceID = v.ID
		out.Changed = e == nil
		return out, e
	case "trial.list":
		if !tickets.ValidQuery(c.Query) {
			return out, tickets.ErrInvalid
		}
		rows, e := tx.QueryContext(ctx, `SELECT key FROM owned_exam_trials ORDER BY key LIMIT $1 OFFSET $2`, c.Query.Take, c.Query.Offset)
		if e != nil {
			return out, e
		}
		keys := []string{}
		for rows.Next() {
			var key string
			if e = rows.Scan(&key); e != nil {
				rows.Close()
				return out, e
			}
			keys = append(keys, key)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return out, e
		}
		v := []tickets.Trial{}
		for _, key := range keys {
			trial, e := loadTrial(ctx, tx, key, false)
			if e != nil {
				return out, e
			}
			v = append(v, trial)
		}
		out.Value = v
		return out, nil
	case "trial.save":
		if c.Trial == nil {
			return out, tickets.ErrInvalid
		}
		v, e := saveTrial(ctx, tx, c.Key, *c.Trial)
		out.Value = v
		out.ResourceID = v.ID
		out.Changed = e == nil
		return out, e
	case "trial.get":
		v, e := loadTrial(ctx, tx, c.Key, false)
		if e != nil {
			return out, e
		}
		if !v.IsPublished {
			return out, tickets.ErrNotFound
		}
		t, e := loadTest(ctx, tx, v.TestKey, false)
		if e != nil {
			return out, e
		}
		if t.IsArchived {
			return out, tickets.ErrNotFound
		}
		out.Value = participantTest(t, nil)
		return out, nil
	default:
		return out, tickets.ErrUnsupported
	}
}
func saveQuestion(ctx context.Context, tx *sql.Tx, id string, v tickets.QuestionInput) (q tickets.Question, e error) {
	if e = tickets.ValidateQuestion(v); e != nil {
		return q, e
	}
	g, e := loadGroup(ctx, tx, v.QuestionGroupKey, true)
	if e != nil {
		return q, e
	}
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_exam_questions WHERE group_id=$1`, g.ID).Scan(&count); e != nil {
		return q, e
	}
	existing := map[string]tickets.Choice{}
	if id != "" {
		if !authority.ValidID(id) {
			return q, tickets.ErrNotFound
		}
		var b []byte
		var oldGroup string
		if e = tx.QueryRowContext(ctx, `SELECT group_id::text,data FROM owned_exam_questions WHERE id=$1 FOR UPDATE`, id).Scan(&oldGroup, &b); e != nil {
			return q, e
		}
		if oldGroup != g.ID {
			return q, tickets.ErrUnsupported
		}
		if e = json.Unmarshal(b, &q); e != nil {
			return q, e
		}
		for _, ch := range q.Choices {
			existing[ch.ID] = ch
		}
	} else {
		if count >= tickets.MaxQuestions {
			return q, tickets.ErrLimit
		}
		var full bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_exam_group_links l WHERE l.group_id=$1 AND (SELECT count(*) FROM owned_exam_group_links x JOIN owned_exam_questions q ON q.group_id=x.group_id WHERE x.test_id=l.test_id)>=200)`, g.ID).Scan(&full); e != nil {
			return q, e
		}
		if full {
			return q, tickets.ErrLimit
		}
		q.ID, e = uuid()
		if e != nil {
			return q, e
		}
		q.SortOrder = count
	}
	q.GroupID = g.ID
	if v.SortOrder != nil {
		q.SortOrder = *v.SortOrder
	}
	q.Content = v.Content
	q.Category = v.Category
	if q.Category != nil {
		x := strings.TrimSpace(*q.Category)
		if x == "" {
			q.Category = nil
		} else {
			q.Category = &x
		}
	}
	q.Type = v.Type
	q.GradingMode = v.GradingMode
	q.Difficulty = v.Difficulty
	q.Points = v.Points
	q.Config = v.Config
	if q.Config == nil {
		q.Config = map[string]any{}
	}
	q.Choices = []tickets.Choice{}
	for _, ch := range v.Choices {
		x := tickets.Choice{SortOrder: ch.SortOrder, Content: ch.Content, IsCorrect: ch.IsCorrect, Config: ch.Config}
		if x.Config == nil {
			x.Config = map[string]any{}
		}
		if ch.ID != nil {
			if old, ok := existing[*ch.ID]; ok {
				x.ID = old.ID
			}
		}
		if x.ID == "" {
			x.ID, e = uuid()
			if e != nil {
				return q, e
			}
		}
		q.Choices = append(q.Choices, x)
	}
	sort.SliceStable(q.Choices, func(i, j int) bool { return q.Choices[i].SortOrder < q.Choices[j].SortOrder })
	b, e := marshal(q)
	if e != nil {
		return q, e
	}
	if id == "" {
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_exam_questions(id,group_id,sort_order,data)VALUES($1,$2,$3,$4)`, q.ID, g.ID, q.SortOrder, b)
	} else {
		_, e = tx.ExecContext(ctx, `UPDATE owned_exam_questions SET sort_order=$2,data=$3 WHERE id=$1`, q.ID, q.SortOrder, b)
	}
	return q, e
}
func (r *Repository) saveTest(ctx context.Context, tx *sql.Tx, key string, v tickets.TestInput, now time.Time) (t tickets.Test, e error) {
	if e = tickets.ValidateTest(v); e != nil {
		return t, e
	}
	if v.GrantedPermissionGroupKey != nil {
		groupID, ok := r.cfg.GrantedGroups[*v.GrantedPermissionGroupKey]
		if !ok {
			return t, tickets.ErrUnsupported
		}
		var exists bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_authority_groups WHERE id=$1)`, groupID).Scan(&exists); e != nil {
			return t, e
		}
		if !exists {
			return t, tickets.ErrUnavailable
		}
	}
	if key != "" {
		t, e = loadTest(ctx, tx, key, true)
		if e != nil {
			return t, e
		}
	} else {
		t.ID, e = uuid()
		if e != nil {
			return t, e
		}
		t.CreatedAt = now
	}
	t.TestInput = v
	if t.Config == nil {
		t.Config = map[string]any{}
	}
	t.UpdatedAt = now
	assignments := append([]tickets.Assignment{}, v.QuestionGroups...)
	sort.Slice(assignments, func(i, j int) bool { return assignments[i].QuestionGroupKey < assignments[j].QuestionGroupKey })
	groupIDs := map[string]string{}
	t.Groups = []tickets.Group{}
	// Resolve names first, then acquire every shared group lock in UUID order.
	// Starting attempts uses this same order; bank writers take one group lock.
	holdIDs := []string{}
	for _, a := range assignments {
		g, e := loadGroup(ctx, tx, a.QuestionGroupKey, false)
		if e != nil {
			return t, e
		}
		holdIDs = append(holdIDs, g.ID)
	}
	sort.Strings(holdIDs)
	for _, id := range holdIDs {
		if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_exam_groups WHERE id=$1 FOR SHARE`, id); e != nil {
			return t, e
		}
	}
	count := 0
	for _, a := range assignments {
		g, e := loadGroup(ctx, tx, a.QuestionGroupKey, false)
		if e != nil {
			return t, e
		}
		if e = groupQuestions(ctx, tx, &g); e != nil {
			return t, e
		}
		groupIDs[a.QuestionGroupKey] = g.ID
		t.Groups = append(t.Groups, g)
		count += len(g.Questions)
	}
	if count < 1 || count > tickets.MaxQuestions {
		return t, tickets.ErrInvalid
	}
	bankBytes, err := marshal(t.Groups)
	if err != nil {
		return t, err
	}
	if len(bankBytes) > 2<<20 {
		return t, tickets.ErrLimit
	}
	if v.AllowCategorySelection && len(categories(t)) < 3 {
		return t, tickets.ErrInvalid
	}
	if !v.ShuffleQuestions {
		t.RandomQuestionCount = nil
	}
	b, e := marshal(t.TestInput)
	if e != nil {
		return t, e
	}
	if key == "" {
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_exams(id,key,data,is_published,is_listed,is_archived,created_at,updated_at)VALUES($1,$2,$3,$4,$5,false,$6,$6)`, t.ID, t.Key, b, t.IsPublished, t.IsListed, now)
	} else {
		_, e = tx.ExecContext(ctx, `UPDATE owned_exams SET key=$2,data=$3,is_published=$4,is_listed=$5,updated_at=$6 WHERE id=$1`, t.ID, t.Key, b, t.IsPublished, t.IsListed, now)
	}
	if e != nil {
		return t, e
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM owned_exam_group_links WHERE test_id=$1`, t.ID); e != nil {
		return t, e
	}
	for _, a := range v.QuestionGroups {
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_exam_group_links(test_id,group_id,sort_order)VALUES($1,$2,$3)`, t.ID, groupIDs[a.QuestionGroupKey], a.SortOrder); e != nil {
			return t, e
		}
	}
	return loadTest(ctx, tx, t.Key, false)
}
func loadTrial(ctx context.Context, tx *sql.Tx, key string, lock bool) (v tickets.Trial, e error) {
	q := `SELECT r.id::text,r.data,r.is_published,t.key,t.data->>'title' FROM owned_exam_trials r JOIN owned_exams t ON t.id=r.test_id WHERE r.key=$1`
	if lock {
		q += ` FOR UPDATE OF r`
	}
	var b []byte
	var published bool
	var testKey string
	var title sql.NullString
	e = tx.QueryRowContext(ctx, q, key).Scan(&v.ID, &b, &published, &testKey, &title)
	if e != nil {
		return v, mapped(e)
	}
	if !title.Valid {
		return v, tickets.ErrUnavailable
	}
	v.TestTitle = title.String
	e = json.Unmarshal(b, &v.TrialInput)
	v.TestKey = testKey
	v.IsPublished = published
	return v, e
}
func saveTrial(ctx context.Context, tx *sql.Tx, key string, v tickets.TrialInput) (out tickets.Trial, e error) {
	if !tickets.ValidKey(v.Key) || !tickets.ValidKey(v.TestKey) || !tickets.Text(v.Title, 1, 256) || !tickets.OptionalText(v.Description, 4096) {
		return out, tickets.ErrInvalid
	}
	t, e := loadTest(ctx, tx, v.TestKey, true)
	if e != nil {
		return out, e
	}
	if t.IsArchived {
		return out, tickets.ErrNotFound
	}
	out = tickets.Trial{TrialInput: v, TestTitle: t.Title}
	if key == "" {
		out.ID, e = uuid()
		if e != nil {
			return out, e
		}
	} else {
		prior, e := loadTrial(ctx, tx, key, true)
		if e != nil {
			return out, e
		}
		out.ID = prior.ID
	}
	b, e := marshal(v)
	if e != nil {
		return out, e
	}
	if key == "" {
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_exam_trials(id,key,test_id,data,is_published)VALUES($1,$2,$3,$4,$5)`, out.ID, v.Key, t.ID, b, v.IsPublished)
	} else {
		_, e = tx.ExecContext(ctx, `UPDATE owned_exam_trials SET key=$2,test_id=$3,data=$4,is_published=$5 WHERE id=$1`, out.ID, v.Key, t.ID, b, v.IsPublished)
	}
	return out, e
}

func (e Export) ExportBytes() ([]byte, string) { return e.Data, e.Name }
