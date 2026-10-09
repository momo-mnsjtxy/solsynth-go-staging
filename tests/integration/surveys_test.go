// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/internal/mediastore"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/internal/stickerpg"
	"solsynth.local/go-migration/internal/surveypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/stickers"
	"solsynth.local/go-migration/ports/sphere/surveys"
	"strings"
	"sync"
	"testing"
	"time"
)

func surveyDB(t *testing.T) *sql.DB {
	db := postDB(t)
	query(t, db, surveypg.Schema)
	query(t, db, stickerpg.Schema)
	return db
}
func surveyQuestions() []surveys.Question {
	return []surveys.Question{{Title: "Choose one", Type: surveys.SingleChoice, Required: true, Options: []surveys.Option{{Label: "Alpha"}, {Label: "Beta"}}}, {Title: "Choose several", Type: surveys.MultipleChoice, MaxSelections: postInt(2), Options: []surveys.Option{{Label: "One"}, {Label: "Two"}, {Label: "Three"}}}, {Title: "Would you repeat?", Type: surveys.YesNo, Required: true}, {Title: "Rate the synthetic event", Type: surveys.Rating, MinValue: surveyFloat(0), MaxValue: surveyFloat(5)}, {Title: "Comment", Type: surveys.FreeText, MaxLength: postInt(32)}}
}
func surveyFloat(v float64) *float64 { return &v }
func surveyAnswers(s surveys.Survey) map[string]json.RawMessage {
	return map[string]json.RawMessage{s.Questions[0].ID: json.RawMessage(fmt.Sprintf("%q", s.Questions[0].Options[0].ID)), s.Questions[1].ID: json.RawMessage(fmt.Sprintf("[%q,%q]", s.Questions[1].Options[0].ID, s.Questions[1].Options[1].ID)), s.Questions[2].ID: json.RawMessage(`false`), s.Questions[3].ID: json.RawMessage(`2.5`), s.Questions[4].ID: json.RawMessage(`"synthetic answer"`)}
}
func makeSurvey(t *testing.T, r *surveypg.Repository, actor, pub string, anonymous, hidden bool) surveys.Survey {
	t.Helper()
	q := surveyQuestions()
	s, e := r.Create(context.Background(), actor, pub, surveys.Patch{Title: postString("Synthetic questionnaire"), Questions: &q, Anonymous: &anonymous, HideResults: &hidden})
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestPGSurveysLifecyclePrivacyAndValidation(t *testing.T) {
	db := surveyDB(t)
	r := surveypg.New(db)
	ctx := context.Background()
	owner, pub := postPublisher(t, db, 4300)
	participant := postAccount(t, db, 4301)
	other := postAccount(t, db, 4302)
	s := makeSurvey(t, r, owner, pub.Name, true, true)
	for _, actor := range []string{"", participant} {
		if _, e := r.Get(ctx, actor, s.ID); !errors.Is(e, surveys.ErrNotFound) {
			t.Fatal("draft exposed", actor, e)
		}
	}
	if _, e := r.Update(ctx, participant, s.ID, surveys.Patch{Title: postString("hijack")}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("cross owner update", e)
	}
	if _, e := r.Transition(ctx, owner, s.ID, "archive"); !errors.Is(e, surveys.ErrConflict) {
		t.Fatal("draft archive", e)
	}
	var e error
	s, e = r.Transition(ctx, owner, s.ID, "publish")
	if e != nil || s.Status != surveys.Published || s.PublishedAt == nil {
		t.Fatal(s, e)
	}
	if _, e = r.Update(ctx, owner, s.ID, surveys.Patch{Title: postString("changed")}); !errors.Is(e, surveys.ErrConflict) {
		t.Fatal("published mutable", e)
	}
	a, e := r.Answer(ctx, participant, s.ID, surveyAnswers(s))
	if e != nil || a.AccountID != nil {
		t.Fatal("anonymous response", a, e)
	}
	for _, actor := range []string{"", other} {
		got, e := r.Get(ctx, actor, s.ID)
		if e != nil || got.UserAnswer != nil || len(got.Stats) != 0 {
			t.Fatal("answer/results leaked", got, e)
		}
	}
	mine, e := r.Get(ctx, participant, s.ID)
	if e != nil || mine.UserAnswer == nil || mine.UserAnswer.ID != a.ID || len(mine.Stats) != 0 {
		t.Fatal("own answer", mine, e)
	}
	if _, e = r.Results(ctx, participant, s.ID); !errors.Is(e, surveys.ErrForbidden) {
		t.Fatal("private stats", e)
	}
	if _, _, e = r.Feedback(ctx, participant, s.ID, surveys.Page{Take: 20}); !errors.Is(e, surveys.ErrForbidden) {
		t.Fatal("feedback exposure", e)
	}
	feedback, n, e := r.Feedback(ctx, owner, s.ID, surveys.Page{Take: 20})
	raw, _ := json.Marshal(feedback)
	if e != nil || n != 1 || len(feedback) != 1 || feedback[0].AccountID != nil || strings.Contains(string(raw), participant) {
		t.Fatal("anonymous feedback identity", string(raw), e)
	}
	stats, e := r.Results(ctx, owner, s.ID)
	if e != nil || stats.Total != 1 || stats.Questions[s.Questions[0].ID].Choices[s.Questions[0].Options[0].ID] != 1 || *stats.Questions[s.Questions[3].ID].Average != 2.5 {
		t.Fatal(stats, e)
	}
	visible, e := r.Get(ctx, owner, s.ID)
	if e != nil || visible.Stats[s.Questions[3].ID]["rating"] != 2 {
		t.Fatal("documented bankers rounding", visible.Stats, e)
	}
	replacement := surveyAnswers(s)
	replacement[s.Questions[0].ID] = json.RawMessage(fmt.Sprintf("%q", s.Questions[0].Options[1].ID))
	replaced, e := r.Answer(ctx, participant, s.ID, replacement)
	if e != nil || replaced.ID != a.ID {
		t.Fatal("replace unique response", replaced, e)
	}
	invalid := surveyAnswers(s)
	invalid[s.Questions[1].ID] = json.RawMessage(fmt.Sprintf("[%q,%q]", s.Questions[1].Options[0].ID, s.Questions[1].Options[0].ID))
	if _, e = r.Answer(ctx, participant, s.ID, invalid); !errors.Is(e, surveys.ErrValidation) {
		t.Fatal("duplicate selections", e)
	}
	got, e := r.Results(ctx, owner, s.ID)
	if e != nil || got.Total != 1 || got.Questions[s.Questions[0].ID].Choices[s.Questions[0].Options[1].ID] != 1 {
		t.Fatal("invalid replacement changed data", got, e)
	}
	clone, e := r.Transition(ctx, owner, s.ID, "clone")
	if e != nil || clone.ID == s.ID || clone.Status != surveys.Draft || clone.Questions[0].ID == s.Questions[0].ID || clone.Questions[0].Options[0].ID == s.Questions[0].Options[0].ID {
		t.Fatal("clone identities", clone, e)
	}
	if _, e = r.Transition(ctx, owner, s.ID, "archive"); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Answer(ctx, other, s.ID, surveyAnswers(s)); !errors.Is(e, surveys.ErrConflict) {
		t.Fatal("archive accepted", e)
	}
	if e = r.Withdraw(ctx, participant, s.ID); e != nil {
		t.Fatal(e)
	}
	if e = r.Withdraw(ctx, participant, s.ID); e != nil {
		t.Fatal("idempotent withdrawal", e)
	}
	if e = r.Delete(ctx, owner, s.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Get(ctx, owner, s.ID); !errors.Is(e, surveys.ErrNotFound) {
		t.Fatal("deleted visible", e)
	}
}
func TestPGSurveysLinkedPostVisibilityAndDeadline(t *testing.T) {
	db := surveyDB(t)
	ctx := context.Background()
	soc := socialpg.New(db)
	r := surveypg.New(db).WithVisibility(soc)
	pr := postpg.New(db).WithVisibility(soc)
	owner, pub := postPublisher(t, db, 4310)
	viewer := postAccount(t, db, 4311)
	post := createPost(t, pr, owner, pub.Name, "private questionnaire", posts.Private, false)
	s := makeSurvey(t, r, owner, pub.Name, false, false)
	if _, e := r.Update(ctx, owner, s.ID, surveys.Patch{PostID: &post.ID}); e != nil {
		t.Fatal(e)
	}
	s, e := r.Transition(ctx, owner, s.ID, "publish")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.Get(ctx, viewer, s.ID); !errors.Is(e, surveys.ErrNotFound) {
		t.Fatal("linked private leak", e)
	}
	if _, e = r.Answer(ctx, viewer, s.ID, surveyAnswers(s)); !errors.Is(e, surveys.ErrNotFound) {
		t.Fatal("linked private response", e)
	}
	if _, e = pr.Update(ctx, owner, post.ID, posts.Patch{Visibility: postInt(posts.Public)}); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Get(ctx, viewer, s.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = soc.ChangeRelationship(ctx, owner, viewer, social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Get(ctx, viewer, s.ID); !errors.Is(e, surveys.ErrNotFound) {
		t.Fatal("blocked survey leak", e)
	}
	query(t, db, `UPDATE owned_surveys SET ended_at=CURRENT_TIMESTAMP-interval '1 second' WHERE id=$1`, s.ID)
	if _, e = r.Answer(ctx, owner, s.ID, surveyAnswers(s)); !errors.Is(e, surveys.ErrConflict) {
		t.Fatal("expired accepted", e)
	}
	if list, n, e := r.List(ctx, owner, surveys.Page{Take: 20, Active: true}); e != nil || n != 0 || len(list) != 0 {
		t.Fatal("expired active list", list, n, e)
	}
	if e = pr.Delete(ctx, owner, post.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Get(ctx, owner, s.ID); !errors.Is(e, surveys.ErrNotFound) {
		t.Fatal("deleted linked post visible", e)
	}
}
func TestPGSurveysConcurrentReplacementAndRollback(t *testing.T) {
	db := surveyDB(t)
	a := surveypg.New(db)
	b := surveypg.New(database(t))
	ctx := context.Background()
	owner, pub := postPublisher(t, db, 4320)
	actor := postAccount(t, db, 4321)
	s := makeSurvey(t, a, owner, pub.Name, false, false)
	s, e := a.Transition(ctx, owner, s.ID, "publish")
	if e != nil {
		t.Fatal(e)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	ids := make(chan string, 2)
	var wg sync.WaitGroup
	for _, r := range []*surveypg.Repository{a, b} {
		wg.Add(1)
		go func(r *surveypg.Repository) {
			defer wg.Done()
			<-start
			v, e := r.Answer(ctx, actor, s.ID, surveyAnswers(s))
			ids <- v.ID
			errs <- e
		}(r)
	}
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if x, y := <-ids, <-ids; x == "" || x != y {
		t.Fatal("duplicate submissions", x, y)
	}
	var count int
	if e = db.QueryRow(`SELECT count(*) FROM owned_survey_answers WHERE survey_id=$1`, s.ID).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	query(t, db, `ALTER TABLE owned_survey_audit ADD CONSTRAINT survey_synthetic_failure CHECK(actor_id<>'d5100000-0000-4000-8000-000000004321'::uuid OR action<>'withdraw')`)
	t.Cleanup(func() {
		query(t, db, `ALTER TABLE owned_survey_audit DROP CONSTRAINT IF EXISTS survey_synthetic_failure`)
	})
	if e = a.Withdraw(ctx, actor, s.ID); e == nil {
		t.Fatal("audit failure hidden")
	}
	if e = db.QueryRow(`SELECT count(*) FROM owned_survey_answers WHERE survey_id=$1`, s.ID).Scan(&count); e != nil || count != 1 {
		t.Fatal("partial withdrawal", count, e)
	}
}
func TestPGSurveysVerifiedHTTPAndRevocation(t *testing.T) {
	db := surveyDB(t)
	owner, pub := postPublisher(t, db, 4330)
	_, v, guard, token := interactionAuthority(t, db, owner)
	r := surveypg.NewWithAuthority(db, guard)
	h := surveys.NewVerifiedHandler(r, v)
	q := surveyQuestions()
	body, _ := json.Marshal(surveys.Patch{Questions: &q})
	if w := postHTTP(h, "POST", "/api/surveys?pub="+pub.Name, string(body), token); w.Code != 403 {
		t.Fatal("missing grant", w.Code, w.Body.String())
	}
	if e := authoritypg.New(db).ReplacePolicy(context.Background(), owner, []authority.Rule{{Pattern: "surveys.*"}}, nil); e != nil {
		t.Fatal(e)
	}
	w := postHTTP(h, "POST", "/api/surveys?pub="+pub.Name, string(body), token)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var s surveys.Survey
	if e := json.Unmarshal(w.Body.Bytes(), &s); e != nil {
		t.Fatal(e)
	}
	if w := postHTTP(h, "POST", "/api/surveys/"+s.ID+"/publish", "", token); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	req := httptest.NewRequest("GET", "/api/surveys", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	proof, e := v.AuthenticateRequest(req)
	if e != nil {
		t.Fatal(e)
	}
	query(t, db, `UPDATE owned_authority_sessions SET revoked_at=CURRENT_TIMESTAMP WHERE account_id=$1`, owner)
	if _, e = r.Answer(guard.Bind(context.Background(), proof), owner, s.ID, surveyAnswers(s)); e == nil {
		t.Fatal("revoked bound proof accepted")
	}
	if w := postHTTP(h, "GET", "/api/surveys/"+s.ID, "", token); w.Code != 401 {
		t.Fatal("revoked read", w.Code)
	}
}

func waitSurveyLock(t *testing.T, ctx context.Context, db *sql.DB, table string) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if e := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%'||$1||'%' AND (query LIKE '%FOR UPDATE%' OR query LIKE '%FOR SHARE%'))`, table).Scan(&blocked); e != nil {
			t.Fatal(e)
		}
		if blocked {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("transaction did not reach lock", table)
		case <-ticker.C:
		}
	}
}

func TestPGSurveysArchiveWinsQueuedResponse(t *testing.T) {
	db := surveyDB(t)
	r := surveypg.New(db)
	owner, pub := postPublisher(t, db, 4360)
	actor := postAccount(t, db, 4361)
	s := makeSurvey(t, r, owner, pub.Name, false, false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, e := r.Transition(ctx, owner, s.ID, "publish")
	if e != nil {
		t.Fatal(e)
	}
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_surveys WHERE id=$1 FOR UPDATE`, s.ID); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := r.Answer(ctx, actor, s.ID, surveyAnswers(s)); done <- e }()
	waitSurveyLock(t, ctx, db, "owned_surveys")
	if _, e = tx.ExecContext(ctx, `UPDATE owned_surveys SET status=2 WHERE id=$1`, s.ID); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, surveys.ErrConflict) {
		t.Fatal("queued answer ignored archive", e)
	}
	var n int
	if e = db.QueryRow(`SELECT count(*) FROM owned_survey_answers WHERE survey_id=$1`, s.ID).Scan(&n); e != nil || n != 0 {
		t.Fatal("archived response persisted", n, e)
	}
}

func TestPGSurveysPublisherRevocationWhileWaiting(t *testing.T) {
	db := surveyDB(t)
	r := surveypg.New(db)
	owner, pub := postPublisher(t, db, 4370)
	editor := postAccount(t, db, 4371)
	joinPostEditor(t, db, owner, editor, pub.ID)
	s := makeSurvey(t, r, owner, pub.Name, false, false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_publishers WHERE id=$1 FOR UPDATE`, pub.ID); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, e := r.Update(ctx, editor, s.ID, surveys.Patch{Title: postString("revoked mutation")})
		done <- e
	}()
	waitSurveyLock(t, ctx, db, "owned_publishers")
	if _, e = tx.ExecContext(ctx, `UPDATE owned_publisher_members SET revoked_at=CURRENT_TIMESTAMP WHERE publisher_id=$1 AND account_id=$2`, pub.ID, editor); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("stale member mutation", e)
	}
	got, e := r.Get(ctx, owner, s.ID)
	if e != nil || got.Title == nil || *got.Title == "revoked mutation" {
		t.Fatal(got, e)
	}
}

func TestPGSurveysAuthorityRevocationWhileWaiting(t *testing.T) {
	db := surveyDB(t)
	owner, pub := postPublisher(t, db, 4380)
	base := surveypg.New(db)
	s := makeSurvey(t, base, owner, pub.Name, false, false)
	s, e := base.Transition(context.Background(), owner, s.ID, "publish")
	if e != nil {
		t.Fatal(e)
	}
	_, v, g, token := interactionAuthority(t, db, owner)
	if e = authoritypg.New(db).ReplacePolicy(context.Background(), owner, []authority.Rule{{Pattern: "surveys.*"}}, nil); e != nil {
		t.Fatal(e)
	}
	request := httptest.NewRequest("POST", "/api/surveys/"+s.ID+"/answer", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	proof, e := v.AuthenticateRequest(request)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(g.Bind(context.Background(), proof), 10*time.Second)
	defer cancel()
	r := surveypg.NewWithAuthority(db, g)
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, owner); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := r.Answer(ctx, owner, s.ID, surveyAnswers(s)); done <- e }()
	waitSurveyLock(t, ctx, db, "owned_authority_accounts")
	if _, e = tx.ExecContext(ctx, `UPDATE owned_authority_sessions SET revoked_at=CURRENT_TIMESTAMP WHERE account_id=$1`, owner); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e == nil {
		t.Fatal("revoked proof committed")
	}
	var n int
	if e = db.QueryRow(`SELECT count(*) FROM owned_survey_answers WHERE survey_id=$1`, s.ID).Scan(&n); e != nil || n != 0 {
		t.Fatal("revoked answer persisted", n, e)
	}
}

func stickerFixture(t *testing.T, n int) (mediaFixture, *stickerpg.Repository, string, accounts.Publisher, media.File) {
	t.Helper()
	db := surveyDB(t)
	f := newMediaFixture(t)
	actor, pub := postPublisher(t, db, n)
	u := mediaUpload("sticker", "synthetic-image-bytes", true)
	u.MIMEType = "image/png"
	u.Name = "fixture.png"
	file, _, e := f.repo.Upload(f.ctx, f.actor, u, strings.NewReader("synthetic-image-bytes"))
	if e != nil {
		t.Fatal(e)
	}
	return f, stickerpg.New(db).WithMedia(f.repo), actor, pub, file
}
func makePack(t *testing.T, r *stickerpg.Repository, actor string, pub accounts.Publisher, prefix string) stickers.Pack {
	t.Helper()
	p, e := r.CreatePack(context.Background(), actor, pub.Name, stickers.PackPatch{Name: postString("Synthetic pack"), Prefix: &prefix})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func TestPGStickersOwnershipContentAndMediaPrivacy(t *testing.T) {
	f, r, owner, pub, file := stickerFixture(t, 4340)
	ctx := context.Background()
	other := postAccount(t, f.db, 4341)
	pack := makePack(t, r, owner, pub, "Fixture4340")
	s, e := r.CreateSticker(ctx, owner, pack.ID, stickers.StickerPatch{Slug: postString("Smile"), ImageID: &file.ID, Size: postInt(3), Mode: postInt(1)})
	if e != nil {
		t.Fatal(e)
	}
	got, e := r.Lookup(ctx, "", " fixture4340+SMILE ")
	if e != nil || got.ID != s.ID || got.Size != 3 || got.Mode != 1 {
		t.Fatal(got, e)
	}
	if _, e = r.UpdatePack(ctx, other, pack.ID, stickers.PackPatch{Name: postString("hijack")}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("pack hijack", e)
	}
	if _, e = r.UpdateSticker(ctx, other, pack.ID, s.ID, stickers.StickerPatch{Name: postString("hijack")}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("sticker hijack", e)
	}
	one, e := r.Own(ctx, other, pack.ID)
	if e != nil {
		t.Fatal(e)
	}
	two, e := r.Own(ctx, other, pack.ID)
	if e != nil || one.ID != two.ID {
		t.Fatal("own idempotence", one, two, e)
	}
	if _, e = r.Ownership(ctx, owner, pack.ID); !errors.Is(e, stickers.ErrNotFound) {
		t.Fatal("foreign ownership", e)
	}
	owned, e := r.Owned(ctx, other)
	if e != nil || len(owned) != 1 || len(owned[0].Pack.Stickers) != 1 {
		t.Fatal(owned, e)
	}
	query(t, f.db, `UPDATE owned_media_files SET is_public=false WHERE id=$1`, file.ID)
	if _, e = r.Lookup(ctx, owner, s.Identifier); !errors.Is(e, stickers.ErrNotFound) {
		t.Fatal("private image leaked", e)
	}
	if list, e := r.Content(ctx, "", pack.ID); e != nil || len(list) != 0 {
		t.Fatal("private content metadata", list, e)
	}
	if _, e = r.CreateSticker(ctx, owner, pack.ID, stickers.StickerPatch{Slug: postString("Private"), ImageID: &file.ID}); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("private attached", e)
	}
	query(t, f.db, `UPDATE owned_media_files SET is_public=true WHERE id=$1`, file.ID)
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
	if _, e = r.Lookup(ctx, "", s.Identifier); !errors.Is(e, stickers.ErrNotFound) {
		t.Fatal("disabled media owner", e)
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=false WHERE id=$1`, f.actor)
	if e = r.DeletePack(ctx, owner, pack.ID); e != nil {
		t.Fatal(e)
	}
	if list, e := r.Owned(ctx, other); e != nil || len(list) != 0 {
		t.Fatal("deleted owned pack", list, e)
	}
	if e = r.Release(ctx, other, pack.ID); e != nil {
		t.Fatal("deleted pack release", e)
	}
	if _, e = r.Lookup(ctx, "", s.Identifier); !errors.Is(e, stickers.ErrNotFound) {
		t.Fatal("deleted lookup", e)
	}
}
func TestPGStickersCapacityConcurrencyAndAtomicBatch(t *testing.T) {
	f, r, owner, pub, file := stickerFixture(t, 4350)
	ctx := context.Background()
	editor := postAccount(t, f.db, 4351)
	joinPostEditor(t, f.db, owner, editor, pub.ID)
	pack := makePack(t, r, owner, pub, "Fixture4350")
	var first stickers.Sticker
	for i := 0; i < 23; i++ {
		s, e := r.CreateSticker(ctx, owner, pack.ID, stickers.StickerPatch{Slug: postString(fmt.Sprintf("S%d", i)), ImageID: &file.ID})
		if e != nil {
			t.Fatal(e)
		}
		if i == 0 {
			first = s
		}
	}
	second := stickerpg.New(database(t)).WithMedia(f.repo)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i, actor := range []string{owner, editor} {
		wg.Add(1)
		go func(i int, actor string) {
			defer wg.Done()
			<-start
			repo := r
			if i == 1 {
				repo = second
			}
			_, e := repo.CreateSticker(ctx, actor, pack.ID, stickers.StickerPatch{Slug: postString(fmt.Sprintf("Final%d", i)), ImageID: &file.ID})
			errs <- e
		}(i, actor)
	}
	close(start)
	wg.Wait()
	close(errs)
	success, capacity := 0, 0
	for e := range errs {
		if e == nil {
			success++
		} else if errors.Is(e, stickers.ErrCapacity) {
			capacity++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || capacity != 1 {
		t.Fatal("capacity race", success, capacity)
	}
	absent := "10000000-0000-4000-8000-000000000099"
	if _, e := r.Rendering(ctx, owner, pack.ID, stickers.Rendering{StickerIDs: []string{first.ID, absent}, Size: postInt(3)}); !errors.Is(e, stickers.ErrNotFound) {
		t.Fatal("mixed batch", e)
	}
	if got, e := r.GetSticker(ctx, owner, pack.ID, first.ID); e != nil || got.Size != 0 {
		t.Fatal("partial rendering", got, e)
	}
	if e := r.Reorder(ctx, owner, pack.ID, []stickers.OrderItem{{ID: first.ID, Order: 77}, {ID: absent, Order: 1}}); !errors.Is(e, stickers.ErrNotFound) {
		t.Fatal("mixed reorder", e)
	}
	if got, e := r.GetSticker(ctx, owner, pack.ID, first.ID); e != nil || got.Order != 0 {
		t.Fatal("partial reorder", got, e)
	}
	query(t, f.db, `ALTER TABLE owned_sticker_audit ADD CONSTRAINT sticker_synthetic_failure CHECK(actor_id<>'d5100000-0000-4000-8000-000000004350'::uuid OR action<>'sticker.update')`)
	t.Cleanup(func() {
		query(t, f.db, `ALTER TABLE owned_sticker_audit DROP CONSTRAINT IF EXISTS sticker_synthetic_failure`)
	})
	if _, e := r.UpdateSticker(ctx, owner, pack.ID, first.ID, stickers.StickerPatch{Name: postString("must rollback")}); e == nil {
		t.Fatal("audit failure hidden")
	}
	if got, e := r.GetSticker(ctx, "", pack.ID, first.ID); e != nil || got.Name == "must rollback" {
		t.Fatal("partial edit", got, e)
	}
}

func TestPGStickersVerifiedHTTPAndRevocation(t *testing.T) {
	f, plain, owner, pub, file := stickerFixture(t, 4400)
	pack := makePack(t, plain, owner, pub, "Http4400")
	item, e := plain.CreateSticker(context.Background(), owner, pack.ID, stickers.StickerPatch{Slug: postString("Smile"), ImageID: &file.ID})
	if e != nil {
		t.Fatal(e)
	}
	_, v, g, token := interactionAuthority(t, f.db, owner)
	r := stickerpg.NewWithAuthority(f.db, g).WithMedia(f.repo)
	h := stickers.NewVerifiedHandler(r, v)
	if w := postHTTP(h, "GET", "/api/stickers/lookup/Http4400+Smile", "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), item.ID) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := postHTTP(h, "GET", "/api/stickers/lookup/Http4400+Smile/open", "", ""); w.Code != 302 || w.Header().Get("Location") != "/drive/files/"+file.ID+"?original=true" {
		t.Fatal("media adapter redirect", w.Code, w.Header())
	}
	if w := postHTTP(h, "POST", "/api/stickers/"+pack.ID+"/own", "", token); w.Code != 403 {
		t.Fatal("missing grant", w.Code)
	}
	if e = authoritypg.New(f.db).ReplacePolicy(context.Background(), owner, []authority.Rule{{Pattern: "stickers.*"}}, nil); e != nil {
		t.Fatal(e)
	}
	if w := postHTTP(h, "POST", "/api/stickers/"+pack.ID+"/own", "", token); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := postHTTP(h, "PATCH", "/api/stickers/"+pack.ID+"/content/"+item.ID, `{"mode":7}`, token); w.Code != 400 {
		t.Fatal("invalid rendering", w.Code, w.Body.String())
	}
	if w := postHTTP(h, "POST", "/api/stickers/lookup/batch", `{"placeholders":["Http4400+Smile","missing"]}`, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"sticker":null`) {
		t.Fatal("batch missing projection", w.Code, w.Body.String())
	}
	request := httptest.NewRequest("GET", "/api/stickers/me", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	proof, e := v.AuthenticateRequest(request)
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_authority_sessions SET revoked_at=CURRENT_TIMESTAMP WHERE account_id=$1`, owner)
	if e = r.Release(g.Bind(context.Background(), proof), owner, pack.ID); e == nil {
		t.Fatal("revoked mutation accepted")
	}
	if w := postHTTP(h, "GET", "/api/stickers/me", "", token); w.Code != 401 {
		t.Fatal("revoked private read", w.Code)
	}
}
func TestPGSurveysStickersRestartPersistence(t *testing.T) {
	db := database(t)
	ctx := context.Background()
	root := os.Getenv("SOLSYNTH_DISPOSABLE_PG")
	path := filepath.Join(root, "surveys-stickers-restart.json")
	objects := filepath.Join(root, "surveys-stickers-objects")
	type record struct{ Actor, Survey, Pack, Sticker, Media string }
	if os.Getenv("PG_TEST_PHASE") == "after" {
		raw, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		var ids record
		if e = json.Unmarshal(raw, &ids); e != nil {
			t.Fatal(e)
		}
		store, e := mediastore.NewFilesystem(objects, 1<<20)
		if e != nil {
			t.Fatal(e)
		}
		defer store.Close()
		m, e := mediapg.New(db, store, authoritypg.NewTransactionGuard(nil, nil), mediapg.Config{MaxFileBytes: 1 << 20, PendingTTL: time.Minute})
		if e != nil {
			t.Fatal(e)
		}
		r := surveypg.New(db).WithMedia(m)
		s, e := r.Get(ctx, ids.Actor, ids.Survey)
		if e != nil || s.UserAnswer == nil || s.Status != surveys.Published {
			t.Fatal("survey restart", s, e)
		}
		stats, e := r.Results(ctx, ids.Actor, ids.Survey)
		if e != nil || stats.Total != 1 {
			t.Fatal(stats, e)
		}
		st := stickerpg.New(db).WithMedia(m)
		v, e := st.GetSticker(ctx, "", ids.Pack, ids.Sticker)
		if e != nil || v.Image.ID != ids.Media {
			t.Fatal("sticker restart", v, e)
		}
		owned, e := st.Owned(ctx, ids.Actor)
		if e != nil || len(owned) != 1 || owned[0].PackID != ids.Pack {
			t.Fatal(owned, e)
		}
		mediaRead(t, m, ctx, "", ids.Media, "durable-survey-image")
		return
	}
	query(t, db, surveypg.Schema)
	query(t, db, stickerpg.Schema)
	f := newMediaFixture(t)
	if e := os.Mkdir(objects, 0700); e != nil {
		t.Fatal(e)
	}
	f.root = objects
	f = mediaPool(t, f, db)
	owner, pub := postPublisher(t, db, 4390)
	u := mediaUpload("restart-survey", "durable-survey-image", true)
	u.MIMEType = "image/png"
	u.Name = "fixture.png"
	file, _, e := f.repo.Upload(f.ctx, f.actor, u, strings.NewReader("durable-survey-image"))
	if e != nil {
		t.Fatal(e)
	}
	r := surveypg.New(db).WithMedia(f.repo)
	s := makeSurvey(t, r, owner, pub.Name, false, false)
	s, e = r.Transition(ctx, owner, s.ID, "publish")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.Answer(ctx, owner, s.ID, surveyAnswers(s)); e != nil {
		t.Fatal(e)
	}
	st := stickerpg.New(db).WithMedia(f.repo)
	p := makePack(t, st, owner, pub, "Restart4390")
	v, e := st.CreateSticker(ctx, owner, p.ID, stickers.StickerPatch{Slug: postString("Saved"), ImageID: &file.ID})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.Own(ctx, owner, p.ID); e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(record{owner, s.ID, p.ID, v.ID, file.ID})
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
}
