// SPDX-License-Identifier: AGPL-3.0-only
package ticketpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/tickets"
)

type attemptRecord struct {
	tickets.Attempt
	TestID         string
	TrialID        *string
	Snapshot       tickets.Snapshot
	SubmissionHash *string
}

func loadAttempt(ctx context.Context, tx *sql.Tx, id string, lock bool) (a attemptRecord, e error) {
	if !authority.ValidID(id) {
		return a, tickets.ErrNotFound
	}
	q := `SELECT id::text,test_id::text,account_id::text,is_trial,trial_id::text,status,started_at,deadline_at,submitted_at,reviewed_at,score,snapshot,submission_hash FROM owned_exam_attempts WHERE id=$1`
	if lock {
		q += ` FOR UPDATE`
	}
	var b []byte
	e = tx.QueryRowContext(ctx, q, id).Scan(&a.ID, &a.TestID, &a.AccountID, &a.IsTrial, &a.TrialID, &a.Status, &a.StartedAt, &a.DeadlineAt, &a.SubmittedAt, &a.ReviewedAt, &a.Score, &b, &a.SubmissionHash)
	if e != nil {
		return a, mapped(e)
	}
	if e = json.Unmarshal(b, &a.Snapshot); e != nil {
		return a, e
	}
	a.Key = a.Snapshot.Key
	a.Title = a.Snapshot.Title
	a.Answers = []tickets.Answer{}
	rows, e := tx.QueryContext(ctx, `SELECT id::text,question_id::text,value,is_correct,awarded_points,review_note,reviewed_at,reviewed_by_id::text FROM owned_exam_answers WHERE attempt_id=$1 ORDER BY id LIMIT 200`, id)
	if e != nil {
		return a, e
	}
	defer rows.Close()
	for rows.Next() {
		var answer tickets.Answer
		var value []byte
		if e = rows.Scan(&answer.ID, &answer.QuestionID, &value, &answer.IsCorrect, &answer.AwardedPoints, &answer.ReviewNote, &answer.ReviewedAt, &answer.ReviewedByID); e != nil {
			return a, e
		}
		if e = json.Unmarshal(value, &answer.Value); e != nil {
			return a, e
		}
		a.Answers = append(a.Answers, answer)
	}
	return a, rows.Err()
}
func projectAttempt(ctx context.Context, tx *sql.Tx, a attemptRecord, admin bool) (tickets.Attempt, error) {
	out := a.Attempt
	out.Questions = participantQuestions(a.Snapshot.Questions)
	out.PermissionClaim = "none"
	if !admin {
		out.AccountID = ""
		out.Snapshot = nil
		for i := range out.Answers {
			out.Answers[i].ReviewedByID = nil
			if a.Status == tickets.InProgress {
				out.Answers[i].IsCorrect = nil
				out.Answers[i].AwardedPoints = nil
				out.Answers[i].ReviewNote = nil
			}
		}
	} else {
		snapshot := a.Snapshot
		out.Snapshot = &snapshot
	}
	if a.Snapshot.GrantedPermissionGroupKey != nil {
		e := tx.QueryRowContext(ctx, `SELECT state FROM owned_exam_claims WHERE account_id=$1 AND test_id=$2 AND group_key=$3`, a.AccountID, a.TestID, *a.Snapshot.GrantedPermissionGroupKey).Scan(&out.PermissionClaim)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return out, e
		}
	}
	if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_exam_rewards WHERE account_id=$1 AND test_id=$2 AND state='pending'),EXISTS(SELECT 1 FROM owned_exam_activation_requests WHERE account_id=$1 AND state='pending')`, a.AccountID, a.TestID).Scan(&out.RewardPending, &out.ActivationPending); e != nil {
		return out, e
	}
	return out, nil
}
func (r *Repository) start(ctx context.Context, tx *sql.Tx, actor string, c tickets.Command, now time.Time) (out tickets.Result, e error) {
	if len(c.Categories) > 5 {
		return out, tickets.ErrInvalid
	}
	for _, category := range c.Categories {
		if !tickets.Text(category, 1, 128) {
			return out, tickets.ErrInvalid
		}
	}
	trial := c.Operation != "attempt.start"
	key := c.Key
	var trialID *string
	if c.Operation == "trial.start" {
		v, e := loadTrial(ctx, tx, key, false)
		if e != nil {
			return out, e
		}
		if !v.IsPublished {
			return out, tickets.ErrNotFound
		}
		key = v.TestKey
		trialID = &v.ID
	}
	test, e := loadTest(ctx, tx, key, true)
	if e != nil {
		return out, e
	}
	if test.IsArchived || !trial && !test.IsPublished {
		return out, tickets.ErrNotFound
	}
	ids := []string{}
	for _, g := range test.Groups {
		ids = append(ids, g.ID)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_exam_groups WHERE id=$1 FOR SHARE`, id); e != nil {
			return out, e
		}
	}
	test, e = loadTest(ctx, tx, key, false)
	if e != nil {
		return out, e
	}
	if c.Operation == "trial.start" {
		v, e := loadTrial(ctx, tx, c.Key, false)
		if e != nil {
			return out, e
		}
		if !v.IsPublished || v.TestKey != test.Key {
			return out, tickets.ErrConflict
		}
	}
	normalizedCategories := []string{}
	for _, s := range c.Categories {
		normalizedCategories = append(normalizedCategories, strings.ToLower(strings.TrimSpace(s)))
	}
	sort.Strings(normalizedCategories)
	hash, e := digest(struct {
		TestID     string
		Trial      bool
		TrialID    *string
		Categories []string
	}{test.ID, trial, trialID, normalizedCategories})
	if e != nil {
		return out, e
	}
	if c.IdempotencyKey != "" {
		var id string
		var prior sql.NullString
		e = tx.QueryRowContext(ctx, `SELECT id::text,request_hash FROM owned_exam_attempts WHERE account_id=$1 AND request_key=$2`, actor, c.IdempotencyKey).Scan(&id, &prior)
		if e == nil {
			if !prior.Valid || prior.String != hash {
				return out, tickets.ErrConflict
			}
			a, e := loadAttempt(ctx, tx, id, false)
			if e != nil {
				return out, e
			}
			out.Value, e = projectAttempt(ctx, tx, a, false)
			return out, e
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return out, e
		}
	}
	var activeID string
	e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_exam_attempts WHERE account_id=$1 AND test_id=$2 AND is_trial=$3 AND trial_id IS NOT DISTINCT FROM $4::uuid AND status=0 FOR UPDATE`, actor, test.ID, trial, trialID).Scan(&activeID)
	expired := false
	if e == nil {
		a, e := loadAttempt(ctx, tx, activeID, false)
		if e != nil {
			return out, e
		}
		if a.DeadlineAt == nil || !now.After(a.DeadlineAt.Add(10*time.Second)) {
			if c.IdempotencyKey != "" {
				var existingKey, existingHash *string
				if e = tx.QueryRowContext(ctx, `SELECT request_key,request_hash FROM owned_exam_attempts WHERE id=$1`, a.ID).Scan(&existingKey, &existingHash); e != nil {
					return out, e
				}
				if existingKey != nil && *existingKey != c.IdempotencyKey || existingHash != nil && *existingHash != hash {
					return out, tickets.ErrConflict
				}
				if existingKey == nil {
					if _, e = tx.ExecContext(ctx, `UPDATE owned_exam_attempts SET request_key=$2,request_hash=$3 WHERE id=$1`, a.ID, c.IdempotencyKey, hash); e != nil {
						return out, e
					}
				}
			}
			out.Value, e = projectAttempt(ctx, tx, a, false)
			return out, e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_exam_attempts SET status=4 WHERE id=$1`, activeID); e != nil {
			return out, e
		}
		expired = true
	} else if !errors.Is(e, sql.ErrNoRows) {
		return out, e
	}
	var used int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_exam_attempts WHERE account_id=$1 AND test_id=$2 AND NOT is_trial AND started_at>=$3 AND status<>0`, actor, test.ID, now.Add(-time.Duration(test.AttemptPeriodDays)*24*time.Hour)).Scan(&used); e != nil {
		return out, e
	}
	if test.MaxAttempts != nil && used >= *test.MaxAttempts {
		if expired {
			out.Changed = true
			out.ResourceID = activeID
			out.AfterCommit = tickets.ErrLimit
			return out, nil
		}
		return out, tickets.ErrLimit
	}
	rows, e := tx.QueryContext(ctx, `SELECT COALESCE((SELECT jsonb_agg(q.value->>'id') FROM jsonb_array_elements(snapshot->'questions') AS q(value)),'[]'::jsonb) FROM owned_exam_attempts WHERE account_id=$1 AND test_id=$2 AND is_trial=$3 AND trial_id IS NOT DISTINCT FROM $4::uuid AND status<>0 ORDER BY started_at DESC LIMIT 1000`, actor, test.ID, trial, trialID)
	if e != nil {
		return out, e
	}
	previous := map[string]bool{}
	for rows.Next() {
		var b []byte
		var questionIDs []string
		if e = rows.Scan(&b); e != nil {
			rows.Close()
			return out, e
		}
		if e = json.Unmarshal(b, &questionIDs); e != nil {
			rows.Close()
			return out, e
		}
		for _, id := range questionIDs {
			previous[id] = true
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	snapshot, e := selectSnapshot(test, previous, c.Categories, trial)
	if e != nil {
		return out, e
	}
	id, e := uuid()
	if e != nil {
		return out, e
	}
	var deadline *time.Time
	if test.TimeLimitSeconds != nil {
		v := now.Add(time.Duration(*test.TimeLimitSeconds) * time.Second)
		deadline = &v
	}
	b, e := marshal(snapshot)
	if e != nil {
		return out, e
	}
	if len(b) > 2<<20 {
		return out, tickets.ErrLimit
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_exam_attempts(id,test_id,account_id,is_trial,trial_id,status,started_at,deadline_at,snapshot,request_key,request_hash)VALUES($1,$2,$3,$4,$5,0,$6,$7,$8,$9,$10)`, id, test.ID, actor, trial, trialID, now, deadline, b, nullable(c.IdempotencyKey), hash)
	if e != nil {
		return out, e
	}
	a, e := loadAttempt(ctx, tx, id, false)
	if e != nil {
		return out, e
	}
	out.Value, e = projectAttempt(ctx, tx, a, false)
	out.Changed = true
	out.ResourceID = id
	return out, e
}
func (r *Repository) attempts(ctx context.Context, tx *sql.Tx, actor string, c tickets.Command, now time.Time) (out tickets.Result, e error) {
	if c.Operation == "attempt.list" {
		if !tickets.ValidQuery(c.Query) {
			return out, tickets.ErrInvalid
		}
		test, e := loadTest(ctx, tx, c.Key, false)
		if e != nil {
			return out, e
		}
		if c.Query.Status != nil && (*c.Query.Status < 0 || *c.Query.Status > 4) {
			return out, tickets.ErrInvalid
		}
		rows, e := tx.QueryContext(ctx, `SELECT id::text FROM owned_exam_attempts WHERE test_id=$1 AND ($2::integer IS NULL OR status=$2) ORDER BY started_at DESC,id LIMIT $3 OFFSET $4`, test.ID, c.Query.Status, c.Query.Take, c.Query.Offset)
		if e != nil {
			return out, e
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return out, e
			}
			ids = append(ids, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return out, e
		}
		items := []tickets.Attempt{}
		for _, id := range ids {
			a, e := loadAttempt(ctx, tx, id, false)
			if e != nil {
				return out, e
			}
			p, e := projectAttempt(ctx, tx, a, true)
			if e != nil {
				return out, e
			}
			items = append(items, p)
		}
		out.Value = items
		return out, nil
	}
	if c.Operation == "answer.review" {
		return r.review(ctx, tx, actor, c, now)
	}
	a, e := loadAttempt(ctx, tx, c.ID, c.Operation == "attempt.submit")
	if e != nil {
		return out, e
	}
	if a.AccountID != actor {
		return out, tickets.ErrNotFound
	}
	if c.Operation == "attempt.get" {
		out.Value, e = projectAttempt(ctx, tx, a, false)
		return out, e
	}
	normalized, e := normalizeAnswers(a.Snapshot, c.Answers)
	if e != nil {
		return out, e
	}
	hash, e := digest(normalized)
	if e != nil {
		return out, e
	}
	if a.Status != tickets.InProgress {
		if a.SubmissionHash != nil && *a.SubmissionHash == hash {
			out.Value, e = projectAttempt(ctx, tx, a, false)
			return out, e
		}
		return out, tickets.ErrConflict
	}
	if a.DeadlineAt != nil && now.After(a.DeadlineAt.Add(10*time.Second)) {
		_, e = tx.ExecContext(ctx, `UPDATE owned_exam_attempts SET status=4,submission_hash=$2 WHERE id=$1`, a.ID, hash)
		if e != nil {
			return out, e
		}
		a.Status = tickets.Expired
		out.Value, e = projectAttempt(ctx, tx, a, false)
		out.Changed = true
		out.ResourceID = a.ID
		return out, e
	}
	answers, status, score, e := grade(a.Snapshot, normalized)
	if e != nil {
		return out, e
	}
	for i := range answers {
		id, e := uuid()
		if e != nil {
			return out, e
		}
		answers[i].ID = id
		b, e := marshal(answers[i].Value)
		if e != nil {
			return out, e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_exam_answers(id,attempt_id,question_id,value,is_correct,awarded_points)VALUES($1,$2,$3,$4,$5,$6)`, id, a.ID, answers[i].QuestionID, b, answers[i].IsCorrect, answers[i].AwardedPoints); e != nil {
			return out, e
		}
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_exam_attempts SET status=$2,score=$3,submitted_at=$4,submission_hash=$5 WHERE id=$1`, a.ID, status, score, now, hash)
	if e != nil {
		return out, e
	}
	a.Status = status
	a.Score = score
	a.SubmittedAt = &now
	a.Answers = answers
	if e = r.effects(ctx, tx, a, now); e != nil {
		return out, e
	}
	out.Value, e = projectAttempt(ctx, tx, a, false)
	out.Changed = true
	out.ResourceID = a.ID
	return out, e
}
func (r *Repository) review(ctx context.Context, tx *sql.Tx, actor string, c tickets.Command, now time.Time) (out tickets.Result, e error) {
	if !authority.ValidID(c.ID) || c.Review == nil {
		return out, tickets.ErrInvalid
	}
	if e = tickets.ValidateReview(*c.Review); e != nil {
		return out, e
	}
	var attemptID, owner string
	e = tx.QueryRowContext(ctx, `SELECT a.attempt_id::text,t.account_id::text FROM owned_exam_answers a JOIN owned_exam_attempts t ON t.id=a.attempt_id WHERE a.id=$1`, c.ID).Scan(&attemptID, &owner)
	if e != nil {
		return out, e
	}
	// The reviewer's canonical lock is already held. NOWAIT on another account
	// prevents inverted two-account waits; callers can retry a conflict safely.
	if owner != actor {
		var active bool
		e = tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1 FOR UPDATE NOWAIT`, owner).Scan(&active)
		if e != nil {
			return out, e
		}
		if !active {
			return out, tickets.ErrNotFound
		}
	}
	a, e := loadAttempt(ctx, tx, attemptID, true)
	if e != nil {
		return out, e
	}
	var index = -1
	var question tickets.Question
	for i, v := range a.Answers {
		if v.ID == c.ID {
			index = i
			for _, q := range a.Snapshot.Questions {
				if q.ID == v.QuestionID {
					question = q
					break
				}
			}
			break
		}
	}
	if index < 0 || question.ID == "" || question.GradingMode != tickets.Manual {
		return out, tickets.ErrInvalid
	}
	value := *c.Review
	value.AwardedPoints = math.Max(0, math.Min(question.Points, value.AwardedPoints))
	answer := a.Answers[index]
	same := answer.IsCorrect != nil && *answer.IsCorrect == value.IsCorrect && answer.AwardedPoints != nil && *answer.AwardedPoints == value.AwardedPoints && stringEqual(answer.ReviewNote, value.Note)
	if a.Status != tickets.PendingReview {
		if same {
			out.Value, e = projectAttempt(ctx, tx, a, true)
			return out, e
		}
		return out, tickets.ErrConflict
	}
	if same {
		out.Value, e = projectAttempt(ctx, tx, a, true)
		return out, e
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_exam_answers SET is_correct=$2,awarded_points=$3,review_note=$4,reviewed_at=$5,reviewed_by_id=$6 WHERE id=$1`, answer.ID, value.IsCorrect, value.AwardedPoints, value.Note, now, actor)
	if e != nil {
		return out, e
	}
	a.Answers[index].IsCorrect = &value.IsCorrect
	a.Answers[index].AwardedPoints = &value.AwardedPoints
	a.Answers[index].ReviewNote = value.Note
	a.Answers[index].ReviewedAt = &now
	a.Answers[index].ReviewedByID = &actor
	pending := false
	for _, v := range a.Answers {
		if v.AwardedPoints == nil {
			pending = true
			break
		}
	}
	if !pending {
		v := score(a.Snapshot, a.Answers)
		a.Score = &v
		a.Status = tickets.Failed
		if v >= a.Snapshot.PassingScore {
			a.Status = tickets.Passed
		}
		a.ReviewedAt = &now
		if _, e = tx.ExecContext(ctx, `UPDATE owned_exam_attempts SET status=$2,score=$3,reviewed_at=$4 WHERE id=$1`, a.ID, a.Status, v, now); e != nil {
			return out, e
		}
		if e = r.effects(ctx, tx, a, now); e != nil {
			return out, e
		}
	}
	out.Value, e = projectAttempt(ctx, tx, a, true)
	out.Changed = true
	out.ResourceID = a.ID
	return out, e
}
func stringEqual(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
func (r *Repository) effects(ctx context.Context, tx *sql.Tx, a attemptRecord, now time.Time) error {
	if a.IsTrial || a.Status != tickets.Passed && a.Status != tickets.Failed {
		return nil
	}
	if a.Status == tickets.Passed && a.Snapshot.GrantedPermissionGroupKey != nil {
		key := *a.Snapshot.GrantedPermissionGroupKey
		groupID, allowed := r.cfg.GrantedGroups[key]
		state := "pending"
		var group any
		if allowed {
			var held string
			e := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_groups WHERE id=$1 FOR KEY SHARE`, groupID).Scan(&held)
			if e == nil {
				if _, e = tx.ExecContext(ctx, `INSERT INTO owned_authority_group_members(account_id,group_id)VALUES($1,$2)ON CONFLICT DO NOTHING`, a.AccountID, held); e != nil {
					return e
				}
				state = "granted"
				group = held
			} else if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
		}
		if _, e := tx.ExecContext(ctx, `INSERT INTO owned_exam_claims(account_id,test_id,attempt_id,group_key,group_id,state,created_at)VALUES($1,$2,$3,$4,$5,$6,$7)ON CONFLICT(account_id,test_id,group_key)DO UPDATE SET attempt_id=excluded.attempt_id,group_id=excluded.group_id,state=excluded.state`, a.AccountID, a.TestID, a.ID, key, group, state, now); e != nil {
			return e
		}
	}
	if a.Snapshot.RewardExperience != nil && *a.Snapshot.RewardExperience > 0 {
		if _, e := tx.ExecContext(ctx, `INSERT INTO owned_exam_rewards(account_id,test_id,attempt_id,amount,created_at)VALUES($1,$2,$3,$4,$5)ON CONFLICT DO NOTHING`, a.AccountID, a.TestID, a.ID, *a.Snapshot.RewardExperience, now); e != nil {
			return e
		}
		if r.cfg.Rewards != nil {
			var state, attemptID string
			var amount int64
			if e := tx.QueryRowContext(ctx, `SELECT state,attempt_id::text,amount FROM owned_exam_rewards WHERE account_id=$1 AND test_id=$2 FOR UPDATE`, a.AccountID, a.TestID).Scan(&state, &attemptID, &amount); e != nil {
				return e
			}
			if state == "pending" {
				if _, e := tx.ExecContext(ctx, `SAVEPOINT exam_reward_sink`); e != nil {
					return e
				}
				e := r.cfg.Rewards.GrantExamRewardInTransaction(ctx, tx, a.AccountID, a.TestID, attemptID, amount)
				if e != nil {
					if _, rollback := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT exam_reward_sink`); rollback != nil {
						return rollback
					}
				} else {
					if _, e = tx.ExecContext(ctx, `UPDATE owned_exam_rewards SET state='applied' WHERE account_id=$1 AND test_id=$2`, a.AccountID, a.TestID); e != nil {
						return e
					}
				}
				if _, e = tx.ExecContext(ctx, `RELEASE SAVEPOINT exam_reward_sink`); e != nil {
					return e
				}
			}
		}
	}
	if a.Status == tickets.Passed {
		_, e := r.activation(ctx, tx, a.AccountID, true, now)
		return e
	}
	return nil
}
func (r *Repository) activation(ctx context.Context, tx *sql.Tx, actor string, request bool, now time.Time) (state tickets.ActivationState, e error) {
	state.Tests = []tickets.ActivationTest{}
	state.PolicyConfigured = r.cfg.Activation != nil
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_exam_activation_requests WHERE account_id=$1)`, actor).Scan(&state.ActivationPending); e != nil {
		return state, e
	}
	if r.cfg.Activation == nil {
		return state, nil
	}
	policy := r.cfg.Activation
	state.RequireVerifiedContact = policy.RequireVerifiedContact
	state.TestsEnabled = len(policy.RequiredTestKeys) > 0
	state.IsSatisfied = true
	if policy.RequireVerifiedContact {
		state.RequiredRequirementCount++
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_authority_credentials WHERE account_id=$1 AND verified_at IS NOT NULL)`, actor).Scan(&state.HasVerifiedContact); e != nil {
			return state, e
		}
		if state.HasVerifiedContact {
			state.CompletedRequirementCount++
		} else {
			state.IsSatisfied = false
		}
	}
	for _, key := range policy.RequiredTestKeys {
		item := tickets.ActivationTest{Key: key, Title: key}
		var id string
		var data []byte
		var archived bool
		e = tx.QueryRowContext(ctx, `SELECT id::text,data,is_published,is_archived FROM owned_exams WHERE key=$1`, key).Scan(&id, &data, &item.Available, &archived)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return state, e
		}
		if e == nil {
			var test tickets.TestInput
			if e = json.Unmarshal(data, &test); e != nil {
				return state, e
			}
			item.Title = test.Title
			item.MaxAttempts = test.MaxAttempts
			item.Available = item.Available && !archived
			if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_exam_attempts WHERE account_id=$1 AND test_id=$2 AND NOT is_trial AND status=2)`, actor, id).Scan(&item.Passed); e != nil {
				return state, e
			}
			if item.MaxAttempts != nil {
				if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_exam_attempts WHERE account_id=$1 AND test_id=$2 AND NOT is_trial AND status<>0 AND started_at>=$3`, actor, id, now.Add(-time.Duration(test.AttemptPeriodDays)*24*time.Hour)).Scan(&item.UsedAttemptCount); e != nil {
					return state, e
				}
			}
		}
		state.RequiredRequirementCount++
		if item.Passed {
			state.CompletedRequirementCount++
		} else {
			state.IsSatisfied = false
		}
		state.Tests = append(state.Tests, item)
	}
	if request && state.IsSatisfied {
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_exam_activation_requests(account_id,created_at)VALUES($1,$2)ON CONFLICT DO NOTHING`, actor, now); e != nil {
			return state, e
		}
		state.ActivationPending = true
	}
	return state, nil
}
