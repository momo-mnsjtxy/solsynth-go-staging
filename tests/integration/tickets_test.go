// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/ticketpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/tickets"
)

type ticketFixture struct {
	db                                                               *sql.DB
	repo                                                             *ticketpg.Repository
	guard                                                            *authoritypg.TransactionGuard
	verifier                                                         *authority.Verifier
	issuer                                                           *authority.Service
	authorityConfig                                                  authority.Config
	config                                                           ticketpg.Config
	clock                                                            *atomic.Int64
	actor, other, admin, token, otherToken, adminToken, groupID, key string
	ctx, otherCtx, adminCtx                                          context.Context
}

func newTicketFixture(t *testing.T) ticketFixture {
	t.Helper()
	f := ticketFixture{db: database(t), clock: &atomic.Int64{}, actor: authorityUUID(t), other: authorityUUID(t), admin: authorityUUID(t), groupID: authorityUUID(t), key: "exam-" + authorityUUID(t)}
	f.clock.Store(time.Now().UTC().Unix())
	f.authorityConfig = authorityConfig(t, authorityKeys(t), f.clock)
	for _, id := range []string{f.actor, f.other, f.admin} {
		if e := authoritypg.SeedSynthetic(context.Background(), f.db, id, "synthetic_ticket_"+strings.ReplaceAll(id, "-", ""), time.Now()); e != nil {
			t.Fatal(e)
		}
		rules := []authority.Rule{{Pattern: "tests.take"}, {Pattern: "tickets.create"}, {Pattern: "tickets.messages.create"}}
		if id == f.admin {
			rules = []authority.Rule{{Pattern: "*"}}
		}
		if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), id, rules, nil); e != nil {
			t.Fatal(e)
		}
	}
	query(t, f.db, `INSERT INTO owned_authority_groups(id,name)VALUES($1,$2)`, f.groupID, "exam-group-"+f.actor)
	query(t, f.db, `INSERT INTO owned_authority_group_grants(group_id,pattern)VALUES($1,'earned.exam.access')`, f.groupID)
	var e error
	f.issuer, e = authority.New(authoritypg.New(f.db), f.authorityConfig)
	if e != nil {
		t.Fatal(e)
	}
	f.token = pgIssue(t, f.issuer, f.actor).Token
	f.otherToken = pgIssue(t, f.issuer, f.other).Token
	f.adminToken = pgIssue(t, f.issuer, f.admin).Token
	f.config = ticketpg.Config{GrantedGroups: map[string]string{"graduates": f.groupID}, Activation: &ticketpg.ActivationPolicy{RequiredTestKeys: []string{f.key}, RequireVerifiedContact: true}, Now: func() time.Time { return time.Unix(f.clock.Load(), 0).UTC() }}
	return ticketPool(t, f, f.db)
}
func ticketPool(t *testing.T, f ticketFixture, db *sql.DB) ticketFixture {
	t.Helper()
	f.db = db
	var e error
	f.verifier, e = authority.NewVerifier(authoritypg.New(db), f.authorityConfig)
	if e != nil {
		t.Fatal(e)
	}
	f.guard = authoritypg.NewTransactionGuard(authoritypg.New(db), f.verifier)
	f.repo, e = ticketpg.New(db, f.guard, f.config)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.repo.Ready(context.Background()); e != nil {
		t.Fatal("guarded runner must install ticketpg schema", e)
	}
	f.ctx = ticketProof(t, f, f.token)
	f.otherCtx = ticketProof(t, f, f.otherToken)
	f.adminCtx = ticketProof(t, f, f.adminToken)
	return f
}
func ticketProof(t *testing.T, f ticketFixture, token string) context.Context {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/tests", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	p, e := f.verifier.AuthenticateRequest(r)
	if e != nil {
		t.Fatal(e)
	}
	return f.repo.BindAuthority(context.Background(), p)
}
func ticketExec[T any](t *testing.T, f ticketFixture, ctx context.Context, actor string, c tickets.Command) T {
	t.Helper()
	if c.Query.Take == 0 {
		c.Query.Take = 20
	}
	out, e := f.repo.Execute(ctx, actor, c)
	if e != nil {
		t.Fatal(c.Operation, e)
	}
	v, ok := out.Value.(T)
	if !ok {
		t.Fatalf("%s returned %T", c.Operation, out.Value)
	}
	return v
}
func ticketCommand(t *testing.T, f ticketFixture, ctx context.Context, actor string, c tickets.Command) {
	t.Helper()
	if c.Query.Take == 0 {
		c.Query.Take = 20
	}
	if _, e := f.repo.Execute(ctx, actor, c); e != nil {
		t.Fatal(c.Operation, e)
	}
}
func ticketHTTP(f ticketFixture) http.Handler {
	return (&tickets.Service{Repository: f.repo, Authority: f.verifier}).Handler()
}
func ticketRequest(t *testing.T, h http.Handler, method, path, token, key, body string, status int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s got %d: %s", method, path, w.Code, w.Body.String())
	}
	return w
}
func seedExam(t *testing.T, f ticketFixture, manual bool) (tickets.Test, []tickets.Question) {
	t.Helper()
	group := ticketExec[tickets.Group](t, f, f.adminCtx, f.admin, tickets.Command{Operation: "group.save", Group: &tickets.GroupInput{Key: f.key + "-group", Title: "Reusable questions", Config: map[string]any{"admin_secret": "hidden"}}})
	input := tickets.QuestionInput{QuestionGroupKey: group.Key, Content: "Choose the correct answer", Points: 2, Type: tickets.SingleChoice, GradingMode: tickets.Auto, Difficulty: 1, Choices: []tickets.ChoiceInput{{Content: "Right", IsCorrect: true}, {Content: "Wrong"}}}
	q := ticketExec[tickets.Question](t, f, f.adminCtx, f.admin, tickets.Command{Operation: "question.save", Question: &input})
	qs := []tickets.Question{q}
	if manual {
		v := tickets.QuestionInput{QuestionGroupKey: group.Key, Content: "Explain your answer", Points: 1, Type: tickets.FreeText, GradingMode: tickets.Manual}
		qs = append(qs, ticketExec[tickets.Question](t, f, f.adminCtx, f.admin, tickets.Command{Operation: "question.save", Question: &v}))
	}
	v := tickets.DefaultTestInput()
	v.Key = f.key
	v.Title = "Entry exam"
	v.IsPublished = true
	v.PassingScore = 75
	v.QuestionGroups = []tickets.Assignment{{QuestionGroupKey: group.Key}}
	grant := "graduates"
	reward := int64(11)
	v.GrantedPermissionGroupKey = &grant
	v.RewardExperience = &reward
	exam := ticketExec[tickets.Test](t, f, f.adminCtx, f.admin, tickets.Command{Operation: "test.save", Test: &v})
	return exam, qs
}
func correctInput(q tickets.Question) tickets.AnswerInput {
	ids := []string{}
	for _, c := range q.Choices {
		if c.IsCorrect {
			ids = append(ids, c.ID)
		}
	}
	return tickets.AnswerInput{QuestionID: q.ID, ChoiceIDs: ids}
}
func ticketCount(t *testing.T, f ticketFixture, query string, args ...any) int {
	t.Helper()
	var n int
	if e := f.db.QueryRow(query, args...).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}

func TestPGTicketsPrivateSupportLifecycle(t *testing.T) {
	f := newTicketFixture(t)
	h := ticketHTTP(f)
	body := `{"title":"Synthetic support","content":"Private first message","type":0}`
	w := ticketRequest(t, h, "POST", "/api/tickets", f.token, "case-one", body, 200)
	one := backendDecode[tickets.Ticket](t, w)
	if one.CreatorID != f.actor || one.Priority != 1 || len(one.Messages) != 1 || one.Messages[0].SenderID != f.actor || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(one, w.Header())
	}
	replay := backendDecode[tickets.Ticket](t, ticketRequest(t, h, "POST", "/api/tickets", f.token, "case-one", body, 200))
	if replay.ID != one.ID || len(replay.Messages) != 1 {
		t.Fatal(replay)
	}
	ticketRequest(t, h, "POST", "/api/tickets", f.token, "case-one", `{"title":"Changed support","content":"other","type":0}`, 409)
	ticketRequest(t, h, "GET", "/api/tickets/"+one.ID, f.otherToken, "", "", 404)
	ticketRequest(t, h, "GET", "/api/tickets", f.token, "", "", 403)
	ticketRequest(t, h, "GET", "/api/tickets?creatorId="+f.other, f.token, "", "", 403)
	mine := backendDecode[[]tickets.Ticket](t, ticketRequest(t, h, "GET", "/api/tickets/me", f.token, "", "", 200))
	if len(mine) != 1 || mine[0].ID != one.ID {
		t.Fatal(mine)
	}
	ticketRequest(t, h, "GET", "/api/tickets/count?creatorId="+f.actor, f.token, "", "", 200)
	ticketRequest(t, h, "PUT", "/api/tickets/"+one.ID, f.token, "", `{"title":"Owner cannot edit admin metadata"}`, 403)
	ticketRequest(t, h, "POST", "/api/tickets/"+one.ID+"/assign", f.adminToken, "", fmt.Sprintf(`{"assignee_id":%q}`, f.other), 200)
	ticketRequest(t, h, "GET", "/api/tickets/"+one.ID, f.otherToken, "", "", 200)
	ticketRequest(t, h, "POST", "/api/tickets/"+one.ID+"/messages", f.otherToken, "", `{"content":"Assignee cannot impersonate staff"}`, 403)
	ticketRequest(t, h, "POST", "/api/tickets/"+one.ID+"/messages", f.adminToken, "staff-reply", `{"content":"Staff reply"}`, 200)
	ticketRequest(t, h, "POST", "/api/tickets/"+one.ID+"/messages", f.adminToken, "staff-reply", `{"content":"Staff reply"}`, 200)
	resolved := backendDecode[tickets.Ticket](t, ticketRequest(t, h, "POST", "/api/tickets/"+one.ID+"/status", f.adminToken, "", `{"status":2}`, 200))
	if resolved.Status != 2 || resolved.ResolvedAt == nil || len(resolved.Messages) != 2 {
		t.Fatal(resolved)
	}
	ticketRequest(t, h, "POST", "/api/tickets/"+one.ID+"/status", f.adminToken, "", `{"status":4}`, 501)
	oncall := backendDecode[tickets.OnCall](t, ticketRequest(t, h, "POST", "/api/admin/tickets/on-call", f.adminToken, "", fmt.Sprintf(`{"account_id":%q}`, f.other), 200))
	again := backendDecode[tickets.OnCall](t, ticketRequest(t, h, "POST", "/api/admin/tickets/on-call", f.adminToken, "", fmt.Sprintf(`{"account_id":%q}`, f.other), 200))
	if oncall.ID != again.ID {
		t.Fatal(oncall, again)
	}
	ticketRequest(t, h, "DELETE", "/api/admin/tickets/on-call/"+f.other, f.adminToken, "", "", 204)
	ticketRequest(t, h, "DELETE", "/api/admin/tickets/on-call/"+f.other, f.adminToken, "", "", 204)
	ticketRequest(t, h, "POST", "/api/tickets", f.token, "", `{"title":"Unsupported attachment","content":"text","type":0,"file_ids":["file"]}`, 400)
	ticketRequest(t, h, "DELETE", "/api/tickets/"+one.ID, f.adminToken, "", "", 204)
	ticketRequest(t, h, "GET", "/api/tickets/"+one.ID, f.token, "", "", 404)
	if ticketCount(t, f, `SELECT count(*) FROM owned_ticket_messages WHERE ticket_id=$1`, one.ID) != 2 {
		t.Fatal("message replay duplicated data")
	}
}

func TestPGExamsSnapshotsClaimsAndPrivateAnswers(t *testing.T) {
	f := newTicketFixture(t)
	exam, qs := seedExam(t, f, false)
	h := ticketHTTP(f)
	public := ticketRequest(t, h, "GET", "/api/tests", "", "", "", 200)
	if strings.Contains(public.Body.String(), "is_correct") || strings.Contains(public.Body.String(), "admin_secret") || strings.Contains(public.Body.String(), qs[0].Content) {
		t.Fatal("public definition leaks answer bank", public.Body.String())
	}
	start := ticketExec[tickets.Attempt](t, f, f.ctx, f.actor, tickets.Command{Operation: "attempt.start", Key: exam.Key, IdempotencyKey: "start-one"})
	encoded, _ := json.Marshal(start)
	if strings.Contains(string(encoded), "is_correct") || strings.Contains(string(encoded), "snapshot") || len(start.Questions) != 1 || len(start.Answers) != 0 {
		t.Fatal("participant snapshot leak", string(encoded))
	}
	ticketRequest(t, h, "GET", "/api/tests/attempts/"+start.ID, f.otherToken, "", "", 404)
	ticketRequest(t, h, "POST", "/api/tests/attempts/"+start.ID+"/submit", f.otherToken, "", `{"answers":[]}`, 404)
	// Change the live answer key after start; grading must use the stored snapshot.
	q := qs[0]
	replacement := tickets.QuestionInput{QuestionGroupKey: exam.QuestionGroups[0].QuestionGroupKey, Content: "Edited question", Type: q.Type, GradingMode: q.GradingMode, Points: 20, Choices: []tickets.ChoiceInput{}}
	for _, ch := range q.Choices {
		id := ch.ID
		replacement.Choices = append(replacement.Choices, tickets.ChoiceInput{ID: &id, Content: ch.Content, IsCorrect: !ch.IsCorrect})
	}
	ticketExec[tickets.Question](t, f, f.adminCtx, f.admin, tickets.Command{Operation: "question.save", ID: q.ID, Question: &replacement})
	submitted := ticketExec[tickets.Attempt](t, f, f.ctx, f.actor, tickets.Command{Operation: "attempt.submit", ID: start.ID, Answers: []tickets.AnswerInput{correctInput(q)}})
	if submitted.Status != tickets.Passed || submitted.Score == nil || *submitted.Score != 100 || submitted.PermissionClaim != "granted" || !submitted.RewardPending || submitted.ActivationPending {
		t.Fatal(submitted)
	}
	again := ticketExec[tickets.Attempt](t, f, f.ctx, f.actor, tickets.Command{Operation: "attempt.submit", ID: start.ID, Answers: []tickets.AnswerInput{correctInput(q)}})
	if again.Answers[0].ID != submitted.Answers[0].ID {
		t.Fatal("replayed submission replaced answers")
	}
	if _, e := f.repo.Execute(f.ctx, f.actor, tickets.Command{Operation: "attempt.submit", ID: start.ID, Answers: []tickets.AnswerInput{}}); !errors.Is(e, tickets.ErrConflict) {
		t.Fatal(e)
	}
	if n := ticketCount(t, f, `SELECT count(*) FROM owned_authority_group_members WHERE account_id=$1 AND group_id=$2`, f.actor, f.groupID); n != 1 {
		t.Fatal(n)
	}
	if n := ticketCount(t, f, `SELECT count(*) FROM owned_exam_rewards WHERE account_id=$1 AND test_id=$2`, f.actor, exam.ID); n != 1 {
		t.Fatal(n)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+f.token)
	p, e := f.verifier.AuthenticateRequest(req)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.verifier.AuthorizePrincipal(context.Background(), p, "earned.exam.access"); e != nil {
		t.Fatal("claim not canonical", e)
	}
	review := ticketExec[[]tickets.Attempt](t, f, f.adminCtx, f.admin, tickets.Command{Operation: "attempt.list", Key: exam.Key})
	if len(review) != 1 || review[0].Snapshot == nil {
		t.Fatal(review)
	}
	query(t, f.db, `INSERT INTO owned_authority_credentials(account_id,email,verified_at)VALUES($1,$2,now())`, f.actor, f.actor+"@example.invalid")
	state := ticketExec[tickets.ActivationState](t, f, f.ctx, f.actor, tickets.Command{Operation: "activation.recheck"})
	if !state.IsSatisfied || !state.ActivationPending || state.IsActivated || !state.HasVerifiedContact {
		t.Fatal("activation falsely completed", state)
	}
	ticketExec[tickets.ActivationState](t, f, f.ctx, f.actor, tickets.Command{Operation: "activation.recheck"})
	if n := ticketCount(t, f, `SELECT count(*) FROM owned_exam_activation_requests WHERE account_id=$1`, f.actor); n != 1 {
		t.Fatal(n)
	}
	if e = authoritypg.New(f.db).ReplacePolicy(context.Background(), f.admin, []authority.Rule{{Pattern: "tests.take"}}, nil); e != nil {
		t.Fatal(e)
	}
	ticketRequest(t, h, "GET", "/api/admin/tests/"+exam.Key+"/attempts", f.adminToken, "", "", 403)
}

func TestPGExamsManualReviewTrialsAndLimits(t *testing.T) {
	f := newTicketFixture(t)
	exam, qs := seedExam(t, f, true)
	trial := ticketExec[tickets.Trial](t, f, f.adminCtx, f.admin, tickets.Command{Operation: "test.trial.create", Key: exam.Key})
	started := ticketExec[tickets.Attempt](t, f, f.ctx, f.actor, tickets.Command{Operation: "trial.start", Key: trial.Key})
	text := "Subjective answer"
	inputs := []tickets.AnswerInput{correctInput(qs[0]), {QuestionID: qs[1].ID, Text: &text}}
	pending := ticketExec[tickets.Attempt](t, f, f.ctx, f.actor, tickets.Command{Operation: "attempt.submit", ID: started.ID, Answers: inputs})
	if pending.Status != tickets.PendingReview || pending.Score != nil {
		t.Fatal(pending)
	}
	var manualID string
	for _, a := range pending.Answers {
		if a.QuestionID == qs[1].ID {
			manualID = a.ID
		}
	}
	graded := ticketExec[tickets.Attempt](t, f, f.adminCtx, f.admin, tickets.Command{Operation: "answer.review", ID: manualID, Review: &tickets.ReviewInput{IsCorrect: true, AwardedPoints: 999}})
	if graded.Status != tickets.Passed || !graded.IsTrial || graded.PermissionClaim != "none" || graded.RewardPending || graded.ActivationPending {
		t.Fatal("trial granted real effects", graded)
	}
	if n := ticketCount(t, f, `SELECT count(*) FROM owned_authority_group_members WHERE account_id=$1 AND group_id=$2`, f.actor, f.groupID); n != 0 {
		t.Fatal(n)
	}
	limit := 1
	input := exam.TestInput
	input.MaxAttempts = &limit
	ticketExec[tickets.Test](t, f, f.adminCtx, f.admin, tickets.Command{Operation: "test.save", Key: exam.Key, Test: &input})
	started = ticketExec[tickets.Attempt](t, f, f.ctx, f.actor, tickets.Command{Operation: "attempt.start", Key: exam.Key})
	pending = ticketExec[tickets.Attempt](t, f, f.ctx, f.actor, tickets.Command{Operation: "attempt.submit", ID: started.ID, Answers: inputs})
	for _, a := range pending.Answers {
		if a.QuestionID == qs[1].ID {
			manualID = a.ID
		}
	}
	if _, e := f.repo.Execute(f.ctx, f.actor, tickets.Command{Operation: "answer.review", ID: manualID, Review: &tickets.ReviewInput{IsCorrect: true, AwardedPoints: 1}}); !errors.Is(e, authority.ErrForbidden) {
		t.Fatal("participant reviewed own answer", e)
	}
	graded = ticketExec[tickets.Attempt](t, f, f.adminCtx, f.admin, tickets.Command{Operation: "answer.review", ID: manualID, Review: &tickets.ReviewInput{IsCorrect: true, AwardedPoints: 999}})
	if graded.Status != tickets.Passed || graded.Score == nil || *graded.Score != 100 || graded.PermissionClaim != "granted" {
		t.Fatal(graded)
	}
	ticketExec[tickets.Attempt](t, f, f.adminCtx, f.admin, tickets.Command{Operation: "answer.review", ID: manualID, Review: &tickets.ReviewInput{IsCorrect: true, AwardedPoints: 999}})
	if _, e := f.repo.Execute(f.adminCtx, f.admin, tickets.Command{Operation: "answer.review", ID: manualID, Review: &tickets.ReviewInput{IsCorrect: false, AwardedPoints: 0}}); !errors.Is(e, tickets.ErrConflict) {
		t.Fatal(e)
	}
	if _, e := f.repo.Execute(f.ctx, f.actor, tickets.Command{Operation: "attempt.start", Key: exam.Key}); !errors.Is(e, tickets.ErrLimit) {
		t.Fatal("attempt cap ignored", e)
	}
	deadline := 1
	input.TimeLimitSeconds = &deadline
	input.MaxAttempts = nil
	ticketExec[tickets.Test](t, f, f.adminCtx, f.admin, tickets.Command{Operation: "test.save", Key: exam.Key, Test: &input})
	expired := ticketExec[tickets.Attempt](t, f, f.otherCtx, f.other, tickets.Command{Operation: "attempt.start", Key: exam.Key})
	f.clock.Add(12)
	expired = ticketExec[tickets.Attempt](t, f, f.otherCtx, f.other, tickets.Command{Operation: "attempt.submit", ID: expired.ID, Answers: inputs})
	if expired.Status != tickets.Expired || expired.Score != nil || len(expired.Answers) != 0 || expired.PermissionClaim != "none" {
		t.Fatal(expired)
	}
}

func TestPGExamsIndependentPoolIdempotencyAndAtomicClaims(t *testing.T) {
	f := newTicketFixture(t)
	exam, qs := seedExam(t, f, false)
	fresh := ticketPool(t, f, database(t))
	start := make(chan struct{})
	results := make(chan tickets.Result, 2)
	errs := make(chan error, 2)
	for _, x := range []ticketFixture{f, fresh} {
		go func(x ticketFixture) {
			<-start
			out, e := x.repo.Execute(x.ctx, x.actor, tickets.Command{Operation: "attempt.start", Key: exam.Key, IdempotencyKey: "concurrent"})
			results <- out
			errs <- e
		}(x)
	}
	close(start)
	var attempt tickets.Attempt
	for i := 0; i < 2; i++ {
		out := <-results
		e := <-errs
		if e != nil {
			t.Fatal(e)
		}
		a := out.Value.(tickets.Attempt)
		if attempt.ID != "" && attempt.ID != a.ID {
			t.Fatal("multiple active attempts", attempt.ID, a.ID)
		}
		attempt = a
	}
	// Inject failure after canonical membership insertion. Every side effect must
	// roll back with the failed claim receipt, including answers and final status.
	query(t, f.db, `CREATE FUNCTION synthetic_exam_claim_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.account_id::text=TG_ARGV[0] THEN RAISE EXCEPTION 'synthetic claim receipt failure'; END IF; RETURN NEW; END $$`)
	query(t, f.db, fmt.Sprintf(`CREATE TRIGGER synthetic_exam_claim_failure BEFORE INSERT ON owned_exam_claims FOR EACH ROW EXECUTE FUNCTION synthetic_exam_claim_failure('%s')`, f.actor))
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DROP TRIGGER IF EXISTS synthetic_exam_claim_failure ON owned_exam_claims; DROP FUNCTION IF EXISTS synthetic_exam_claim_failure()`)
	})
	if _, e := f.repo.Execute(f.ctx, f.actor, tickets.Command{Operation: "attempt.submit", ID: attempt.ID, Answers: []tickets.AnswerInput{correctInput(qs[0])}}); e == nil {
		t.Fatal("receipt failure hidden")
	}
	query(t, f.db, `DROP TRIGGER synthetic_exam_claim_failure ON owned_exam_claims;DROP FUNCTION synthetic_exam_claim_failure()`)
	if n := ticketCount(t, f, `SELECT count(*) FROM owned_authority_group_members WHERE account_id=$1 AND group_id=$2`, f.actor, f.groupID); n != 0 {
		t.Fatal("authority mutation survived rollback", n)
	}
	if n := ticketCount(t, f, `SELECT count(*) FROM owned_exam_answers WHERE attempt_id=$1`, attempt.ID); n != 0 {
		t.Fatal("answer survived rollback", n)
	}
	start = make(chan struct{})
	for _, x := range []ticketFixture{f, fresh} {
		go func(x ticketFixture) {
			<-start
			out, e := x.repo.Execute(x.ctx, x.actor, tickets.Command{Operation: "attempt.submit", ID: attempt.ID, Answers: []tickets.AnswerInput{correctInput(qs[0])}})
			results <- out
			errs <- e
		}(x)
	}
	close(start)
	for i := 0; i < 2; i++ {
		out := <-results
		e := <-errs
		if e != nil || out.Value.(tickets.Attempt).Status != tickets.Passed {
			t.Fatal(out, e)
		}
	}
	if n := ticketCount(t, f, `SELECT count(*) FROM owned_exam_answers WHERE attempt_id=$1`, attempt.ID); n != 1 {
		t.Fatal(n)
	}
	if n := ticketCount(t, f, `SELECT count(*) FROM owned_exam_claims WHERE account_id=$1 AND test_id=$2`, f.actor, exam.ID); n != 1 {
		t.Fatal(n)
	}
}

func TestPGExamsFreshAuthorityAndDisabledIsolation(t *testing.T) {
	f := newTicketFixture(t)
	exam, _ := seedExam(t, f, false)
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
	blocker, pid := atomicBlocker(t, locks, "owned_authority_accounts", f.actor)
	if _, e := blocker.ExecContext(ctx, `DELETE FROM owned_authority_grants WHERE account_id=$1`, f.actor); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, e := f.repo.Execute(ctx, f.actor, tickets.Command{Operation: "attempt.start", Key: exam.Key})
		done <- e
	}()
	waitAtomicBlocked(t, locks, pid, "owned_authority_accounts")
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, authority.ErrForbidden) {
		t.Fatal("revoked permission accepted", e)
	}
	if n := ticketCount(t, f, `SELECT count(*) FROM owned_exam_attempts WHERE account_id=$1`, f.actor); n != 0 {
		t.Fatal(n)
	}
	if _, e := f.repo.Execute(context.Background(), f.other, tickets.Command{Operation: "attempt.start", Key: exam.Key}); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("asserted identity accepted", e)
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.other)
	ticketRequest(t, ticketHTTP(f), "GET", "/api/tests/"+exam.Key, f.otherToken, "", "", 423)
}

func TestPGTicketsExamsRestartPersistence(t *testing.T) {
	db := database(t)
	record := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "tickets-exams-record.json")
	type saved struct{ Actor, GroupID, AttemptID, TicketID, Key string }
	if os.Getenv("PG_TEST_PHASE") == "before" {
		f := newTicketFixture(t)
		exam, qs := seedExam(t, f, false)
		a := ticketExec[tickets.Attempt](t, f, f.ctx, f.actor, tickets.Command{Operation: "attempt.start", Key: exam.Key})
		a = ticketExec[tickets.Attempt](t, f, f.ctx, f.actor, tickets.Command{Operation: "attempt.submit", ID: a.ID, Answers: []tickets.AnswerInput{correctInput(qs[0])}})
		one := ticketExec[tickets.Ticket](t, f, f.ctx, f.actor, tickets.Command{Operation: "ticket.create", TicketCreate: &tickets.TicketCreate{Title: "Persisted support", Content: "Private durable message", Priority: 1}})
		b, e := json.Marshal(saved{f.actor, f.groupID, a.ID, one.ID, exam.Key})
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(record, b, 0600); e != nil {
			t.Fatal(e)
		}
		return
	}
	if os.Getenv("PG_TEST_PHASE") != "after" {
		t.Fatal("explicit restart phase required")
	}
	b, e := os.ReadFile(record)
	if e != nil {
		t.Fatal(e)
	}
	var state saved
	if e = json.Unmarshal(b, &state); e != nil {
		t.Fatal(e)
	}
	now := &atomic.Int64{}
	now.Store(time.Now().UTC().Unix())
	cfg := authorityConfig(t, authorityKeys(t), now)
	issuer, e := authority.New(authoritypg.New(db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	v, e := authority.NewVerifier(authoritypg.New(db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(db), v)
	repo, e := ticketpg.New(db, guard, ticketpg.Config{GrantedGroups: map[string]string{"graduates": state.GroupID}})
	if e != nil {
		t.Fatal(e)
	}
	token := pgIssue(t, issuer, state.Actor).Token
	h := (&tickets.Service{Repository: repo, Authority: v}).Handler()
	a := backendDecode[tickets.Attempt](t, ticketRequest(t, h, "GET", "/api/tests/attempts/"+state.AttemptID, token, "", "", 200))
	if a.Status != tickets.Passed || a.Score == nil || *a.Score != 100 || a.PermissionClaim != "granted" || a.Snapshot != nil {
		t.Fatal(a)
	}
	one := backendDecode[tickets.Ticket](t, ticketRequest(t, h, "GET", "/api/tickets/"+state.TicketID, token, "", "", 200))
	if len(one.Messages) != 1 || one.Messages[0].Content != "Private durable message" {
		t.Fatal(one)
	}
	ticketRequest(t, h, "GET", "/api/tests/attempts/"+state.AttemptID, "", "", "", 401)
}

type syntheticExamRewardSink struct{ fail bool }

func (s *syntheticExamRewardSink) GrantExamRewardInTransaction(ctx context.Context, tx *sql.Tx, accountID, testID, attemptID string, amount int64) error {
	if _, e := tx.ExecContext(ctx, `INSERT INTO synthetic_exam_reward_calls(account_id,test_id,attempt_id,amount)VALUES($1,$2,$3,$4)ON CONFLICT DO NOTHING`, accountID, testID, attemptID, amount); e != nil {
		return e
	}
	if s.fail {
		return errors.New("synthetic progression provider unavailable")
	}
	return nil
}
func TestPGExamsRewardSinkAtomicAndIdempotent(t *testing.T) {
	f := newTicketFixture(t)
	exam, qs := seedExam(t, f, false)
	query(t, f.db, `CREATE TABLE IF NOT EXISTS synthetic_exam_reward_calls(account_id uuid NOT NULL,test_id uuid NOT NULL,attempt_id uuid NOT NULL,amount bigint NOT NULL,PRIMARY KEY(account_id,test_id))`)
	sink := &syntheticExamRewardSink{fail: true}
	cfg := f.config
	cfg.Rewards = sink
	var e error
	f.repo, e = ticketpg.New(f.db, f.guard, cfg)
	if e != nil {
		t.Fatal(e)
	}
	start := ticketExec[tickets.Attempt](t, f, f.ctx, f.actor, tickets.Command{Operation: "attempt.start", Key: exam.Key})
	done := ticketExec[tickets.Attempt](t, f, f.ctx, f.actor, tickets.Command{Operation: "attempt.submit", ID: start.ID, Answers: []tickets.AnswerInput{correctInput(qs[0])}})
	if done.Status != tickets.Passed || !done.RewardPending {
		t.Fatal("provider failure blocked result", done)
	}
	if n := ticketCount(t, f, `SELECT count(*) FROM synthetic_exam_reward_calls WHERE account_id=$1 AND test_id=$2`, f.actor, exam.ID); n != 0 {
		t.Fatal("partial sink write survived savepoint", n)
	}
	sink.fail = false
	// Place only this synthetic receipt first so the bounded worker case cannot
	// consume unrelated pending fixtures in this shared disposable database.
	query(t, f.db, `UPDATE owned_exam_rewards SET created_at='2000-01-01T00:00:00Z' WHERE account_id=$1 AND test_id=$2`, f.actor, exam.ID)
	if n, e := f.repo.RetryPendingRewards(f.ctx, 1); e != nil || n != 1 {
		t.Fatal("durable reward retry", n, e)
	}
	recovered := ticketExec[tickets.Attempt](t, f, f.ctx, f.actor, tickets.Command{Operation: "attempt.get", ID: start.ID})
	if recovered.RewardPending {
		t.Fatal("successful retry left reward pending")
	}
	for i := 0; i < 2; i++ {
		start = ticketExec[tickets.Attempt](t, f, f.ctx, f.actor, tickets.Command{Operation: "attempt.start", Key: exam.Key})
		done = ticketExec[tickets.Attempt](t, f, f.ctx, f.actor, tickets.Command{Operation: "attempt.submit", ID: start.ID, Answers: []tickets.AnswerInput{correctInput(qs[0])}})
		if done.Status != tickets.Passed || done.RewardPending {
			t.Fatal(done)
		}
	}
	if n := ticketCount(t, f, `SELECT count(*) FROM synthetic_exam_reward_calls WHERE account_id=$1 AND test_id=$2`, f.actor, exam.ID); n != 1 {
		t.Fatal("reward reapplied", n)
	}
	var amount int64
	if e = f.db.QueryRow(`SELECT amount FROM synthetic_exam_reward_calls WHERE account_id=$1 AND test_id=$2`, f.actor, exam.ID).Scan(&amount); e != nil || amount != 11 {
		t.Fatal(amount, e)
	}
}
