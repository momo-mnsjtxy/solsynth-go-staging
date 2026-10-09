// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"net/http/httptest"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/notificationpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/internal/surveypg"
	"solsynth.local/go-migration/ports/authority"
	profiles "solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/surveys"
	"strings"
	"sync"
	"testing"
	"time"
)

type surveySubscriptionFixture struct {
	backendFixture
	pub    profiles.Publisher
	survey surveys.Survey
	post   posts.Post
}

func newSurveySubscriptionFixture(t *testing.T, publish bool) surveySubscriptionFixture {
	t.Helper()
	return surveySubscriptionFixtureWithBackend(t, newBackendFixture(t), publish)
}
func surveySubscriptionFixtureWithBackend(t *testing.T, base backendFixture, publish bool) surveySubscriptionFixture {
	t.Helper()
	f := surveySubscriptionFixture{backendFixture: base}
	query(t, f.db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at) VALUES($1,2,now())`, f.actor)
	name := "survey_" + strings.ReplaceAll(f.actor, "-", "")
	f.pub = backendDecode[profiles.Publisher](t, backendJSONCall(t, f.app, "POST", "/sphere/publishers/individual", f.token, map[string]any{"name": name, "nick": "Survey subscriptions"}, 200))
	f.post = backendDecode[posts.Post](t, backendCall(t, f.app, "POST", "/sphere/posts?pub="+name, f.token, `{"content":"Survey invitation","visibility":0}`, 200))
	q := surveyQuestions()
	notify, anonymous, hidden := true, true, true
	f.survey = backendDecode[surveys.Survey](t, backendJSONCall(t, f.app, "POST", "/sphere/surveys?pub="+name, f.token, surveys.Patch{Title: postString("Survey notice"), Questions: &q, PostID: &f.post.ID, NotifySubscribers: &notify, Anonymous: &anonymous, HideResults: &hidden}, 200))
	if publish {
		backendCall(t, f.app, "POST", "/api/surveys/"+f.survey.ID+"/publish", f.token, "", 200)
	}
	return f
}
func surveyCount(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if e := db.QueryRow(q, args...).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func surveySubscribe(t *testing.T, f surveySubscriptionFixture, token string) surveys.Subscription {
	t.Helper()
	return backendDecode[surveys.Subscription](t, backendCall(t, f.app, "POST", "/sphere/surveys/"+f.survey.ID+"/subscribe", token, "", 200))
}
func surveySendAnswer(t *testing.T, f surveySubscriptionFixture, token string, value map[string]json.RawMessage, status int) *httptest.ResponseRecorder {
	t.Helper()
	return backendJSONCall(t, f.app, "POST", "/sphere/surveys/"+f.survey.ID+"/answer", token, map[string]any{"answer": value}, status)
}
func surveyProcess(t *testing.T, f surveySubscriptionFixture) {
	t.Helper()
	for i := 0; i < 40; i++ {
		n, e := f.app.ProcessSurveyNotifications(context.Background(), 100)
		if e != nil {
			t.Fatal(e)
		}
		if n == 0 {
			return
		}
	}
	t.Fatal("queue failed to drain within fixture bound")
}
func surveyRepo(t *testing.T, f surveySubscriptionFixture, db *sql.DB) (*surveypg.Repository, *authoritypg.TransactionGuard, *authority.Verifier) {
	t.Helper()
	store := authoritypg.New(db)
	v, e := authority.NewVerifier(store, f.config)
	if e != nil {
		t.Fatal(e)
	}
	g := authoritypg.NewTransactionGuard(store, v)
	return surveypg.NewWithAuthority(db, g).WithVisibility(socialpg.NewWithAuthority(db, g)), g, v
}
func TestPGSurveySubscriptionsAuthoredRoutesAndPrivacy(t *testing.T) {
	f := newSurveySubscriptionFixture(t, false)
	path := "/api/surveys/" + f.survey.ID
	if !f.survey.NotifySubscribers {
		t.Fatal("notify flag omitted")
	}
	owner := backendDecode[surveys.Subscription](t, backendCall(t, f.app, "GET", path+"/subscription", f.token, "", 200))
	if owner.AccountID != f.actor || owner.SurveyID != f.survey.ID {
		t.Fatal(owner)
	}
	backendCall(t, f.app, "GET", path+"/subscription", "", "", 401)
	backendCall(t, f.app, "GET", path+"/subscription", f.otherToken, "", 404)
	backendCall(t, f.app, "POST", path+"/subscribe", f.otherToken, "", 404)
	backendCall(t, f.app, "PATCH", path, f.otherToken, `{"notify_subscribers":false}`, 403)
	backendCall(t, f.app, "POST", path+"/unsubscribe", f.token, "", 204)
	backendCall(t, f.app, "GET", path+"/subscription", f.token, "", 404)
	// An ordinary draft edit preserves the authored auto-subscribe behavior.
	backendCall(t, f.app, "PATCH", path, f.token, `{"description":"draft updated"}`, 200)
	owner2 := backendDecode[surveys.Subscription](t, backendCall(t, f.app, "GET", path+"/subscription", f.token, "", 200))
	if owner2.ID == owner.ID {
		t.Fatal("revoked incarnation resurrected")
	}
	backendCall(t, f.app, "POST", path+"/publish", f.token, "", 200)
	first, second := surveySubscribe(t, f, f.otherToken), surveySubscribe(t, f, f.otherToken)
	if first.ID != second.ID || first.AccountID != f.other {
		t.Fatal(first, second)
	}
	backendCall(t, f.app, "GET", path+"/subscription?account_id="+f.actor, f.otherToken, "", 400)
	backendCall(t, f.app, "POST", path+"/subscribe", f.otherToken, `{"account_id":"foreign"}`, 400)
	backendCall(t, f.app, "POST", path+"/unsubscribe?force=true", f.otherToken, "", 400)
	if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.other, []authority.Rule{{Pattern: "surveys.subscribe"}}, nil); e != nil {
		t.Fatal(e)
	}
	backendCall(t, f.app, "POST", path+"/unsubscribe", f.otherToken, "", 204)
	backendCall(t, f.app, "POST", path+"/unsubscribe", f.otherToken, "", 204)
	w := backendCall(t, f.app, "GET", path+"/subscription", f.otherToken, "", 404)
	if !strings.Contains(w.Body.String(), "SURVEY_SUBSCRIPTION_NOT_FOUND") {
		t.Fatal(w.Body.String())
	}
	backendCall(t, f.app, "PATCH", "/sphere/posts/"+f.post.ID, f.token, `{"visibility":3}`, 200)
	backendCall(t, f.app, "POST", path+"/subscribe", f.otherToken, "", 404)
	backendCall(t, f.app, "POST", path+"/unsubscribe", f.otherToken, "", 204)
	// Clearing the flag does not forge a notification nor remove subscriptions.
	clone := backendDecode[surveys.Survey](t, backendCall(t, f.app, "POST", path+"/clone", f.token, "", 200))
	backendCall(t, f.app, "PATCH", "/api/surveys/"+clone.ID, f.token, `{"notify_subscribers":false}`, 200)
	if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.other, nil, nil); e != nil {
		t.Fatal(e)
	}
	backendCall(t, f.app, "POST", path+"/unsubscribe", f.otherToken, "", 403)
}
func TestPGSurveyNotificationsAtomicReplayAndRollback(t *testing.T) {
	f := newSurveySubscriptionFixture(t, true)
	surveySubscribe(t, f, f.otherToken)
	answer := surveyAnswers(f.survey)
	w := surveySendAnswer(t, f, f.otherToken, answer, 200)
	first := backendDecode[surveys.Answer](t, w)
	if first.AccountID != nil {
		t.Fatal("anonymous answer leaked")
	}
	surveySendAnswer(t, f, f.otherToken, answer, 200)
	if n := surveyCount(t, f.db, `SELECT count(*) FROM owned_survey_notifications WHERE survey_id=$1`, f.survey.ID); n != 1 {
		t.Fatal("identical answer replay duplicated queue", n)
	}
	// The composite FK rejects recipient substitution even on trusted SQL.
	var mismatch *pgconn.PgError
	_, mismatchErr := f.db.Exec(`UPDATE owned_survey_notifications SET account_id=$2 WHERE survey_id=$1`, f.survey.ID, f.other)
	if !errors.As(mismatchErr, &mismatch) || mismatch.Code != "23503" {
		t.Fatal("queue recipient ancestry not enforced", mismatchErr)
	}
	// Queue and response write must roll back together, including failed audit.
	query(t, f.db, `ALTER TABLE owned_survey_audit ADD CONSTRAINT survey_subscription_answer_failure CHECK(survey_id<>'`+f.survey.ID+`'::uuid OR action<>'answer')`)
	t.Cleanup(func() {
		query(t, f.db, `ALTER TABLE owned_survey_audit DROP CONSTRAINT IF EXISTS survey_subscription_answer_failure`)
	})
	changed := surveyAnswers(f.survey)
	changed[f.survey.Questions[4].ID] = json.RawMessage(`"new private answer"`)
	surveySendAnswer(t, f, f.otherToken, changed, 503)
	query(t, f.db, `ALTER TABLE owned_survey_audit DROP CONSTRAINT survey_subscription_answer_failure`)
	if n := surveyCount(t, f.db, `SELECT count(*) FROM owned_survey_notifications WHERE survey_id=$1`, f.survey.ID); n != 1 {
		t.Fatal("queue survived rollback", n)
	}
	var revision int
	if e := f.db.QueryRow(`SELECT revision FROM owned_survey_answers WHERE id=$1`, first.ID).Scan(&revision); e != nil || revision != 1 {
		t.Fatal(revision, e)
	}
	query(t, f.db, `ALTER TABLE owned_survey_notifications ADD CONSTRAINT survey_enqueue_failure CHECK(survey_id<>'`+f.survey.ID+`'::uuid OR answer_revision=1)`)
	t.Cleanup(func() {
		query(t, f.db, `ALTER TABLE owned_survey_notifications DROP CONSTRAINT IF EXISTS survey_enqueue_failure`)
	})
	surveySendAnswer(t, f, f.otherToken, changed, 503)
	query(t, f.db, `ALTER TABLE owned_survey_notifications DROP CONSTRAINT survey_enqueue_failure`)
	if e := f.db.QueryRow(`SELECT revision FROM owned_survey_answers WHERE id=$1`, first.ID).Scan(&revision); e != nil || revision != 1 {
		t.Fatal("response survived enqueue failure", revision, e)
	}
	surveySendAnswer(t, f, f.otherToken, changed, 200)
	query(t, f.db, `ALTER TABLE owned_survey_notifications ADD CONSTRAINT survey_projection_failure CHECK(survey_id<>'`+f.survey.ID+`'::uuid OR processed_at IS NULL)`)
	t.Cleanup(func() {
		query(t, f.db, `ALTER TABLE owned_survey_notifications DROP CONSTRAINT IF EXISTS survey_projection_failure`)
	})
	if _, e := f.app.ProcessSurveyNotifications(context.Background(), 100); e == nil {
		t.Fatal("receipt failure hidden")
	}
	if n := surveyCount(t, f.db, `SELECT count(*) FROM notification_v1.inbox WHERE account_id=$1 AND topic='surveys.answer'`, f.actor); n != 0 {
		t.Fatal("partial inbox", n)
	}
	if n := surveyCount(t, f.db, `SELECT count(*) FROM notification_v1.events WHERE account_id=$1 AND type='notifications.new'`, f.actor); n != 0 {
		t.Fatal("partial replay", n)
	}
	query(t, f.db, `ALTER TABLE owned_survey_notifications DROP CONSTRAINT survey_projection_failure`)
	// Independent pools race over the same durable queue.
	db2 := database(t)
	app2 := backendApp(t, db2, f.config)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, app := range []*application.Backend{f.app, app2} {
		wg.Add(1)
		go func(a *application.Backend) {
			defer wg.Done()
			<-start
			_, e := a.ProcessSurveyNotifications(context.Background(), 100)
			errs <- e
		}(app)
	}
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	surveyProcess(t, f)
	if n := surveyCount(t, f.db, `SELECT count(*) FROM notification_v1.inbox WHERE account_id=$1 AND topic='surveys.answer'`, f.actor); n != 2 {
		t.Fatal("lost/duplicate inbox", n)
	}
	if n := surveyCount(t, f.db, `SELECT count(*) FROM notification_v1.inbox WHERE account_id=$1 AND topic='surveys.answer'`, f.other); n != 0 {
		t.Fatal("answerer notified", n)
	}
	var data string
	if e := f.db.QueryRow(`SELECT string_agg(payload,'') FROM (SELECT to_jsonb(n)::text AS payload FROM notification_v1.inbox n WHERE account_id=$1 UNION ALL SELECT data::text FROM notification_v1.events WHERE account_id=$1 AND type='notifications.new') projection`, f.actor).Scan(&data); e != nil {
		t.Fatal(e)
	}
	for _, secret := range []string{f.other, first.ID, "synthetic answer", "new private answer", "answerer_id", "stats", "answer_revision", "answer_id", "questions", "attachment_ids"} {
		if strings.Contains(data, secret) {
			t.Fatal("private inbox or nested replay data", secret)
		}
	}
	if n := surveyCount(t, f.db, `SELECT count(*) FROM notification_v1.inbox WHERE account_id=$1 AND meta<>jsonb_build_object('survey_id',$2::text,'publisher_id',$3::text,'action_uri','/surveys/'||$2::text)`, f.actor, f.survey.ID, f.pub.ID); n != 0 {
		t.Fatal("notification metadata exceeded its exact public allowlist", n)
	}
	if n := surveyCount(t, f.db, `SELECT count(*) FROM notification_v1.deliveries d JOIN notification_v1.inbox n ON n.id=d.notification_id WHERE n.account_id=$1`, f.actor); n != 0 {
		t.Fatal("external delivery was queued", n)
	}
}
func TestPGSurveyNotificationsCurrentAudience(t *testing.T) {
	for _, mode := range []string{"unsubscribe", "resubscribe", "deleted", "private_post", "disabled_recipient", "disabled_owner", "punished_recipient", "punished_owner", "deleted_publisher", "publisher_mute", "recipient_block", "expired", "archived"} {
		t.Run(mode, func(t *testing.T) {
			f := newSurveySubscriptionFixture(t, true)
			surveySubscribe(t, f, f.otherToken)
			surveySendAnswer(t, f, f.token, surveyAnswers(f.survey), 200)
			path := "/api/surveys/" + f.survey.ID
			switch mode {
			case "unsubscribe", "resubscribe":
				backendCall(t, f.app, "POST", path+"/unsubscribe", f.otherToken, "", 204)
				if mode == "resubscribe" {
					surveySubscribe(t, f, f.otherToken)
				}
			case "deleted":
				backendCall(t, f.app, "DELETE", path, f.token, "", 204)
			case "private_post":
				backendCall(t, f.app, "PATCH", "/api/posts/"+f.post.ID, f.token, `{"visibility":3}`, 200)
			case "disabled_recipient":
				query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.other)
			case "disabled_owner":
				query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
			case "punished_recipient", "punished_owner":
				actor := f.other
				if mode == "punished_owner" {
					actor = f.actor
				}
				query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,pattern) VALUES($1,'disable_account','*')`, actor)
			case "deleted_publisher":
				query(t, f.db, `UPDATE owned_publishers SET deleted_at=clock_timestamp() WHERE id=$1`, f.pub.ID)
			case "publisher_mute":
				if _, e := socialpg.New(f.db).ChangeRelationship(context.Background(), f.actor, f.other, social.RelationshipChange{Action: social.Mute}); e != nil {
					t.Fatal(e)
				}
			case "recipient_block":
				if _, e := socialpg.New(f.db).ChangeRelationship(context.Background(), f.other, f.actor, social.RelationshipChange{Action: social.Block}); e != nil {
					t.Fatal(e)
				}
			case "expired":
				query(t, f.db, `UPDATE owned_survey_notifications SET created_at=clock_timestamp()-interval '2 days',expires_at=clock_timestamp()-interval '1 day' WHERE survey_id=$1`, f.survey.ID)
			case "archived":
				backendCall(t, f.app, "POST", path+"/archive", f.token, "", 200)
			}
			surveyProcess(t, f)
			if n := surveyCount(t, f.db, `SELECT count(*) FROM owned_survey_notifications WHERE survey_id=$1 AND suppressed_at IS NOT NULL`, f.survey.ID); n != 1 {
				t.Fatal("stale audience not suppressed", n)
			}
			if n := surveyCount(t, f.db, `SELECT count(*) FROM notification_v1.inbox WHERE account_id=$1 AND topic='surveys.answer'`, f.other); n != 0 {
				t.Fatal("stale audience received notification", n)
			}
		})
	}
	t.Run("signout_preserves_subscription_and_minimal_history", func(t *testing.T) {
		f := newSurveySubscriptionFixture(t, true)
		sub := surveySubscribe(t, f, f.otherToken)
		surveySendAnswer(t, f, f.token, surveyAnswers(f.survey), 200)
		query(t, f.db, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE account_id=$1`, f.other)
		path := "/api/surveys/" + f.survey.ID
		for _, action := range []string{"subscribe", "unsubscribe"} {
			backendCall(t, f.app, "POST", path+"/"+action, f.otherToken, "", 401)
		}
		backendCall(t, f.app, "GET", path+"/subscription", f.otherToken, "", 401)
		surveyProcess(t, f)
		if n := surveyCount(t, f.db, `SELECT count(*) FROM owned_survey_subscriptions WHERE id=$1 AND revoked_at IS NULL`, sub.ID); n != 1 {
			t.Fatal("signout erased durable account preference", n)
		}
		if n := surveyCount(t, f.db, `SELECT count(*) FROM notification_v1.inbox WHERE account_id=$1 AND topic='surveys.answer'`, f.other); n != 1 {
			t.Fatal("signout incorrectly suppressed subscribed work", n)
		}
		backendCall(t, f.app, "DELETE", path, f.token, "", 204)
		surveyProcess(t, f)
		if n := surveyCount(t, f.db, `SELECT count(*) FROM notification_v1.inbox WHERE account_id=$1 AND topic='surveys.answer'`, f.other); n != 1 {
			t.Fatal("later survey deletion erased minimal delivered history", n)
		}
	})
}

func TestPGSurveySubscriptionsConcurrentAndAuditRollback(t *testing.T) {
	f := newSurveySubscriptionFixture(t, true)
	db2 := database(t)
	repos := make([]*surveypg.Repository, 2)
	contexts := make([]context.Context, 2)
	for i, db := range []*sql.DB{f.db, db2} {
		r, g, v := surveyRepo(t, f, db)
		p, e := v.Authenticate(context.Background(), f.otherToken)
		if e != nil {
			t.Fatal(e)
		}
		repos[i] = r
		contexts[i] = g.Bind(context.Background(), p)
	}
	start := make(chan struct{})
	ids := make(chan string, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := range repos {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			s, e := repos[i].Subscribe(contexts[i], f.other, f.survey.ID)
			ids <- s.ID
			errs <- e
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if a, b := <-ids, <-ids; a == "" || a != b {
		t.Fatal("duplicate live subscription", a, b)
	}
	query(t, f.db, `ALTER TABLE owned_survey_audit ADD CONSTRAINT survey_unsubscribe_failure CHECK(survey_id<>'`+f.survey.ID+`'::uuid OR action<>'unsubscribe')`)
	t.Cleanup(func() {
		query(t, f.db, `ALTER TABLE owned_survey_audit DROP CONSTRAINT IF EXISTS survey_unsubscribe_failure`)
	})
	if e := repos[0].Unsubscribe(contexts[0], f.other, f.survey.ID); e == nil {
		t.Fatal("unsubscribe audit failure hidden")
	}
	if _, e := repos[1].Subscription(contexts[1], f.other, f.survey.ID); e != nil {
		t.Fatal("partial unsubscribe", e)
	}
	query(t, f.db, `ALTER TABLE owned_survey_audit DROP CONSTRAINT survey_unsubscribe_failure`)
	if e := repos[0].Unsubscribe(contexts[0], f.other, f.survey.ID); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `ALTER TABLE owned_survey_audit ADD CONSTRAINT survey_subscribe_failure CHECK(survey_id<>'`+f.survey.ID+`'::uuid OR action<>'subscribe')`)
	t.Cleanup(func() {
		query(t, f.db, `ALTER TABLE owned_survey_audit DROP CONSTRAINT IF EXISTS survey_subscribe_failure`)
	})
	if _, e := repos[0].Subscribe(contexts[0], f.other, f.survey.ID); e == nil {
		t.Fatal("subscribe audit failure hidden")
	}
	if _, e := repos[1].Subscription(contexts[1], f.other, f.survey.ID); !errors.Is(e, surveys.ErrSubscriptionNotFound) {
		t.Fatal("partial subscribe", e)
	}
}

func TestPGSurveySubscriptionsWaitRechecksAuthorityAndExpiry(t *testing.T) {
	for _, mode := range []string{"session", "scope", "grant", "account", "expiry"} {
		t.Run(mode, func(t *testing.T) {
			f := newSurveySubscriptionFixture(t, true)
			// The expiration case uses real elapsed wall time and an original proof.
			// Only the canonical grant deadline is shortened before starting the wait.
			if mode == "expiry" {
				f.config.Now = time.Now
			}
			r, g, v := surveyRepo(t, f, f.db)
			p, e := v.Authenticate(context.Background(), f.otherToken)
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(g.Bind(context.Background(), p), 10*time.Second)
			defer cancel()
			table, id := "owned_authority_accounts", f.other
			var expires time.Time
			if mode == "expiry" {
				if e = f.db.QueryRow(`UPDATE owned_authority_grants SET expires_at=clock_timestamp()+interval '2 seconds' WHERE account_id=$1 RETURNING expires_at`, f.other).Scan(&expires); e != nil {
					t.Fatal(e)
				}
				table, id = "owned_publishers", f.pub.ID
			}
			blocker, e := f.db.BeginTx(ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer blocker.Rollback()
			var pid int
			if e = blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
				t.Fatal(e)
			}
			if _, e = blocker.ExecContext(ctx, `SELECT id FROM `+table+` WHERE id=$1 FOR UPDATE`, id); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() { _, e := r.Subscribe(ctx, f.other, f.survey.ID); done <- e }()
			waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: ctx}, pid, table)
			switch mode {
			case "session":
				_, e = blocker.ExecContext(ctx, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE id=$1`, p.Session.ID)
			case "scope":
				_, e = blocker.ExecContext(ctx, `UPDATE owned_authority_sessions SET scopes='["posts.*"]'::jsonb,epoch=epoch+1,authorization_epoch=authorization_epoch+1 WHERE id=$1`, p.Session.ID)
			case "grant":
				_, e = blocker.ExecContext(ctx, `DELETE FROM owned_authority_grants WHERE account_id=$1`, f.other)
			case "account":
				_, e = blocker.ExecContext(ctx, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.other)
			case "expiry":
				organizationWaitExpired(t, ctx, f.db, expires)
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-done; e == nil {
				t.Fatal("stale authority subscribed", mode)
			}
			if n := surveyCount(t, f.db, `SELECT count(*) FROM owned_survey_subscriptions WHERE survey_id=$1 AND account_id=$2`, f.survey.ID, f.other); n != 0 {
				t.Fatal("revoked subscription persisted", n)
			}
		})
	}
}
func TestPGSurveyNotificationsWaitRechecksAudienceAndExpiry(t *testing.T) {
	for _, mode := range []string{"unsubscribe", "expiry", "private_post"} {
		t.Run(mode, func(t *testing.T) {
			f := newSurveySubscriptionFixture(t, true)
			surveySubscribe(t, f, f.otherToken)
			surveySendAnswer(t, f, f.token, surveyAnswers(f.survey), 200)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var expires time.Time
			if mode == "expiry" {
				if e := f.db.QueryRow(`UPDATE owned_survey_notifications SET expires_at=clock_timestamp()+interval '2 seconds' WHERE survey_id=$1 RETURNING expires_at`, f.survey.ID).Scan(&expires); e != nil {
					t.Fatal(e)
				}
			}
			blocker, e := f.db.BeginTx(ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer blocker.Rollback()
			var pid int
			if e = blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
				t.Fatal(e)
			}
			if _, e = blocker.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, f.other); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() { _, e := f.app.ProcessSurveyNotifications(ctx, 100); done <- e }()
			waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: ctx}, pid, "owned_authority_accounts")
			switch mode {
			case "unsubscribe":
				_, e = blocker.ExecContext(ctx, `UPDATE owned_survey_subscriptions SET revoked_at=clock_timestamp() WHERE survey_id=$1 AND account_id=$2`, f.survey.ID, f.other)
			case "private_post":
				_, e = blocker.ExecContext(ctx, `UPDATE owned_text_posts SET visibility=3 WHERE id=$1`, f.post.ID)
			case "expiry":
				organizationWaitExpired(t, ctx, f.db, expires)
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-done; e != nil {
				t.Fatal(e)
			}
			if n := surveyCount(t, f.db, `SELECT count(*) FROM owned_survey_notifications WHERE survey_id=$1 AND suppressed_at IS NOT NULL`, f.survey.ID); n != 1 {
				t.Fatal("waiter ignored current audience", n)
			}
			if n := surveyCount(t, f.db, `SELECT count(*) FROM notification_v1.inbox WHERE account_id=$1 AND topic='surveys.answer'`, f.other); n != 0 {
				t.Fatal("waiter leaked notification", n)
			}
		})
	}
}
func TestPGSurveySubscriptionsRealmRevocation(t *testing.T) {
	f := newRealmPublishingFixture(t)
	q := surveyQuestions()
	notify := true
	s, e := f.surveys.Create(f.ctx, f.actor, f.privatePub.ID, surveys.Patch{Questions: &q, NotifySubscribers: &notify})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.surveys.Transition(f.ctx, f.actor, s.ID, "publish"); e != nil {
		t.Fatal(e)
	}
	if _, e = f.surveys.Subscribe(f.otherCtx, f.other, s.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.surveys.Subscribe(f.outsideCtx, f.outside, s.ID); !errors.Is(e, surveys.ErrNotFound) {
		t.Fatal("private realm subscription", e)
	}
	if _, e = f.surveys.Answer(f.ctx, f.actor, s.ID, surveyAnswers(s)); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	blocker, e := f.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer blocker.Rollback()
	var pid int
	if e = blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = blocker.ExecContext(ctx, `SELECT id FROM owned_realms WHERE id=$1 FOR UPDATE`, f.private.ID); e != nil {
		t.Fatal(e)
	}
	sink := notificationpg.New(f.db, f.guard, "dev.solsynth.solian")
	done := make(chan error, 1)
	go func() { _, e := f.surveys.ProcessNotifications(ctx, sink, "dev.solsynth.solian", 100); done <- e }()
	waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: ctx}, pid, "owned_realms")
	if _, e = blocker.ExecContext(ctx, `UPDATE owned_realm_members SET leave_at=clock_timestamp() WHERE realm_id=$1 AND account_id=$2`, f.private.ID, f.other); e != nil {
		t.Fatal(e)
	}
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if _, e = f.surveys.Subscription(f.otherCtx, f.other, s.ID); !errors.Is(e, surveys.ErrNotFound) {
		t.Fatal("revoked realm subscription read", e)
	}
	if n := surveyCount(t, f.db, `SELECT count(*) FROM owned_survey_notifications WHERE survey_id=$1 AND suppressed_at IS NOT NULL`, s.ID); n != 1 {
		t.Fatal("revoked realm delivered", n)
	}
	if e = f.surveys.Unsubscribe(f.otherCtx, f.other, s.ID); e != nil {
		t.Fatal("revoked realm cleanup", e)
	}
	for _, mode := range []string{"active", "punished", "expired_punishment", "future_punishment"} {
		t.Run("attached_post_realm_owner_"+mode, func(t *testing.T) {
			f := newRealmPublishingFixture(t)
			// The post realm owner differs from both publisher owner and recipient.
			realm, e := f.social.CreateRealm(f.outsideCtx, f.outside, social.RealmPatch{Slug: postString("survey-notify-realm-" + f.outside), Name: postString("Synthetic notification realm"), IsPublic: boolean(true)})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.social.ChangeMember(f.outsideCtx, f.outside, realm.ID, social.MemberChange{Action: social.Invite, AccountID: f.actor}); e != nil {
				t.Fatal(e)
			}
			if _, e = f.social.ChangeMember(f.ctx, f.actor, realm.ID, social.MemberChange{Action: social.Accept}); e != nil {
				t.Fatal(e)
			}
			post := postRealmCreate(t, f, f.ctx, f.actor, f.individual.ID, realm.ID, "Survey in another owner's public realm")
			q, notify := surveyQuestions(), true
			s, e := f.surveys.Create(f.ctx, f.actor, f.individual.ID, surveys.Patch{Questions: &q, PostID: &post.ID, NotifySubscribers: &notify})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.surveys.Transition(f.ctx, f.actor, s.ID, "publish"); e != nil {
				t.Fatal(e)
			}
			if _, e = f.surveys.Subscribe(f.otherCtx, f.other, s.ID); e != nil {
				t.Fatal(e)
			}
			if _, e = f.surveys.Answer(f.ctx, f.actor, s.ID, surveyAnswers(s)); e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "punished":
				query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,pattern) VALUES($1,'disable_account','*')`, f.outside)
			case "expired_punishment":
				query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,pattern,expires_at) VALUES($1,'disable_account','*',clock_timestamp()-interval '1 second')`, f.outside)
			case "future_punishment":
				query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,pattern,starts_at) VALUES($1,'disable_account','*',clock_timestamp()+interval '1 hour')`, f.outside)
			}
			sink := notificationpg.New(f.db, f.guard, "dev.solsynth.solian")
			if _, e = f.surveys.ProcessNotifications(context.Background(), sink, "dev.solsynth.solian", 100); e != nil {
				t.Fatal(e)
			}
			want := 1
			if mode == "punished" {
				want = 0
			}
			if n := surveyCount(t, f.db, `SELECT count(*) FROM notification_v1.inbox WHERE account_id=$1 AND topic='surveys.answer'`, f.other); n != want {
				t.Fatal("attached post realm owner's current punishment ignored", mode, n)
			}
			if n := surveyCount(t, f.db, `SELECT count(*) FROM owned_survey_notifications WHERE survey_id=$1 AND suppressed_at IS NOT NULL`, s.ID); n != 1-want {
				t.Fatal("source-owner suppression receipt", mode, n)
			}
		})
	}
}

func TestPGSurveySubscriptionsRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	type savedFixture struct {
		Actor, Other, Token, OtherToken, Survey, Subscription, Answer, App string
		Authority                                                          restartAuthoritySnapshot
	}
	var saved savedFixture
	if phase == "before" {
		base := newBackendFixture(t)
		base.config.AccessTTL = time.Hour
		base.config.SessionTTL = time.Hour
		base.config.Now = time.Now
		issuer, e := authority.New(authoritypg.New(base.db), base.config)
		if e != nil {
			t.Fatal(e)
		}
		// All restart target survey/subscription/answer/queue state is created only
		// after these original one-hour credentials; never persisted signing keys.
		base.token = pgIssue(t, issuer, base.actor).Token
		base.otherToken = pgIssue(t, issuer, base.other).Token
		appID := "survey.restart." + base.actor
		base.app = surveySubscriptionBackend(t, base.db, base.config, appID)
		f := surveySubscriptionFixtureWithBackend(t, base, true)
		subscription := surveySubscribe(t, f, f.token)
		a := backendDecode[surveys.Answer](t, surveySendAnswer(t, f, f.otherToken, surveyAnswers(f.survey), 200))
		surveyProcess(t, f)
		changed := surveyAnswers(f.survey)
		changed[f.survey.Questions[4].ID] = json.RawMessage(`"queued before restart"`)
		surveySendAnswer(t, f, f.otherToken, changed, 200)
		// A different composed namespace must not consume or redirect this
		// fixture's durable pending item before its own restart worker resumes.
		otherNamespace := backendApp(t, f.db, f.config)
		if _, e = otherNamespace.ProcessSurveyNotifications(context.Background(), 100); e != nil {
			t.Fatal(e)
		}
		if n := surveyCount(t, f.db, `SELECT count(*) FROM owned_survey_notifications WHERE survey_id=$1 AND processed_at IS NULL`, f.survey.ID); n != 1 {
			t.Fatal(n)
		}
		saved = savedFixture{Actor: f.actor, Other: f.other, Token: f.token, OtherToken: f.otherToken, Survey: f.survey.ID, Subscription: subscription.ID, Answer: a.ID, App: appID, Authority: saveAuthoritySnapshot(t, f.config)}
		saveRestartFixture(t, "survey-subscriptions.json", saved)
		return
	}
	loadRestartFixture(t, "survey-subscriptions.json", &saved)
	config := restoreAuthoritySnapshot(t, saved.Authority)
	app := surveySubscriptionBackend(t, db, config, saved.App)
	sub := backendDecode[surveys.Subscription](t, backendCall(t, app, "GET", "/api/surveys/"+saved.Survey+"/subscription", saved.Token, "", 200))
	if sub.ID != saved.Subscription || sub.AccountID != saved.Actor {
		t.Fatal("subscription did not persist", sub)
	}
	backendCall(t, app, "GET", "/api/surveys/"+saved.Survey+"/subscription", saved.OtherToken, "", 404)
	if n := surveyCount(t, db, `SELECT count(*) FROM owned_survey_notifications WHERE survey_id=$1 AND processed_at IS NULL`, saved.Survey); n != 1 {
		t.Fatal("pending queue did not persist", n)
	}
	// Resume only already-durable work. No schema installation, data seeds,
	// credentials, deadline extensions or replacement sessions occur after restart.
	if _, e := app.ProcessSurveyNotifications(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	if _, e := app.ProcessSurveyNotifications(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	if n := surveyCount(t, db, `SELECT count(*) FROM notification_v1.inbox WHERE account_id=$1 AND topic='surveys.answer'`, saved.Actor); n != 2 {
		t.Fatal("restart replay duplicated/lost inbox", n)
	}
	store := authoritypg.New(db)
	verifier, e := authority.NewVerifier(store, config)
	if e != nil {
		t.Fatal(e)
	}
	proof, e := verifier.Authenticate(context.Background(), saved.Token)
	if e != nil {
		t.Fatal("original proof no longer valid", e)
	}
	guard := authoritypg.NewTransactionGuard(store, verifier)
	sink := notificationpg.New(db, guard, saved.App)
	replay, e := sink.Replay(guard.Bind(context.Background(), proof), saved.Actor, saved.App, 0, 100)
	if e != nil || len(replay.Events) != 2 {
		t.Fatal("restart replay", replay, e)
	}
	raw, _ := json.Marshal(replay)
	if strings.Contains(string(raw), saved.Other) || strings.Contains(string(raw), saved.Answer) || strings.Contains(string(raw), "queued before restart") {
		t.Fatal("restart replay leaked answers")
	}
	expired := config
	expired.Now = func() time.Time { return saved.Authority.Now.Add(2 * time.Hour) }
	expiredApp := surveySubscriptionBackend(t, db, expired, saved.App)
	backendCall(t, expiredApp, "GET", "/api/surveys/"+saved.Survey+"/subscription", saved.Token, "", 401)
}

func TestPGSurveySubscriptionsBoundedCapacity(t *testing.T) {
	f := newSurveySubscriptionFixture(t, true)
	// Imported synthetic recipients fill the cap without thousands of HTTP calls.
	// UUIDs are derived from this random survey namespace, never real accounts.
	query(t, f.db, `INSERT INTO owned_authority_accounts(id,name,nick,created_at) SELECT md5($1::text||':'||n)::uuid,'synthetic_survey_capacity_'||md5($1::text||':'||n),'Synthetic capacity recipient',clock_timestamp() FROM generate_series(1,999) n`, f.survey.ID)
	query(t, f.db, `INSERT INTO owned_survey_subscriptions(id,survey_id,account_id,created_at,updated_at) SELECT md5($1::text||':sub:'||n)::uuid,$1::uuid,md5($1::text||':'||n)::uuid,clock_timestamp(),clock_timestamp() FROM generate_series(1,999) n`, f.survey.ID)
	backendCall(t, f.app, "POST", "/api/surveys/"+f.survey.ID+"/subscribe", f.otherToken, "", 409)
	surveySubscribe(t, f, f.token) // Existing subscription remains idempotent at cap.
	if n := surveyCount(t, f.db, `SELECT count(*) FROM owned_survey_subscriptions WHERE survey_id=$1 AND revoked_at IS NULL`, f.survey.ID); n != surveypg.MaxSubscribers {
		t.Fatal(n)
	}
	backendCall(t, f.app, "POST", "/api/surveys/"+f.survey.ID+"/unsubscribe", f.token, "", 204)
	surveySubscribe(t, f, f.otherToken)
	if n := surveyCount(t, f.db, `SELECT count(*) FROM owned_survey_subscriptions WHERE survey_id=$1 AND revoked_at IS NULL`, f.survey.ID); n != surveypg.MaxSubscribers {
		t.Fatal(n)
	}
	for _, limit := range []int{0, 101} {
		if _, e := f.app.ProcessSurveyNotifications(context.Background(), limit); !errors.Is(e, surveys.ErrInvalid) {
			t.Fatal("unbounded worker limit", limit, e)
		}
	}
}

func TestPGSurveyNotificationsConcurrentAnswerDispatchLockOrder(t *testing.T) {
	for _, mode := range []string{"dispatch", "unsubscribe", "delete"} {
		t.Run(mode, func(t *testing.T) {
			f := newSurveySubscriptionFixture(t, true)
			surveySendAnswer(t, f, f.otherToken, surveyAnswers(f.survey), 200)
			r, g, v := surveyRepo(t, f, database(t))
			proof, e := v.Authenticate(context.Background(), f.otherToken)
			if e != nil {
				t.Fatal(e)
			}
			base, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			ctx := g.Bind(base, proof)
			// The competing action uses a separate pool and its own verifier-bound proof.
			competitor, cg, cv := surveyRepo(t, f, f.db)
			ownerProof, e := cv.Authenticate(base, f.token)
			if e != nil {
				t.Fatal(e)
			}
			ownerCtx := cg.Bind(base, ownerProof)
			blocker, e := f.db.BeginTx(base, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer blocker.Rollback()
			var blockerPID int
			if e = blocker.QueryRowContext(base, `SELECT pg_backend_pid()`).Scan(&blockerPID); e != nil {
				t.Fatal(e)
			}
			// Hold only the queue's insert lock. The answer reaches enqueue while holding
			// its survey lock. The competitor owns the other account while waiting for
			// the survey or publisher. There are no transaction retries in this test.
			if _, e = blocker.ExecContext(base, `LOCK TABLE owned_survey_notifications IN SHARE MODE`); e != nil {
				t.Fatal(e)
			}
			changed := surveyAnswers(f.survey)
			changed[f.survey.Questions[4].ID] = json.RawMessage(`"concurrent private response"`)
			answerDone := make(chan error, 1)
			go func() { _, e := r.Answer(ctx, f.other, f.survey.ID, changed); answerDone <- e }()
			locks := atomicAuthorityFixture{db: f.db, ctx: base}
			answerPID := waitAtomicBlocked(t, locks, blockerPID, "owned_survey_notifications")
			otherDone := make(chan error, 1)
			go func() {
				switch mode {
				case "dispatch":
					_, e := f.app.ProcessSurveyNotifications(base, 100)
					otherDone <- e
				case "unsubscribe":
					otherDone <- competitor.Unsubscribe(ownerCtx, f.actor, f.survey.ID)
				case "delete":
					otherDone <- competitor.Delete(ownerCtx, f.actor, f.survey.ID)
				}
			}()
			waitTable := "owned_surveys"
			if mode == "delete" {
				waitTable = "owned_publishers"
			}
			waitAtomicBlocked(t, locks, answerPID, waitTable)
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-answerDone; e != nil {
				t.Fatal("answer/competitor lock inversion", mode, e)
			}
			if e = <-otherDone; e != nil {
				t.Fatal("competing action failed", mode, e)
			}
			surveyProcess(t, f)
			want := 0
			if mode == "dispatch" {
				want = 2
			}
			if n := surveyCount(t, f.db, `SELECT count(*) FROM notification_v1.inbox WHERE account_id=$1 AND topic='surveys.answer'`, f.actor); n != want {
				t.Fatal("concurrent delivery lost/duplicated or stale", mode, n)
			}
			if mode != "dispatch" {
				if n := surveyCount(t, f.db, `SELECT count(*) FROM owned_survey_notifications WHERE survey_id=$1 AND suppressed_at IS NOT NULL`, f.survey.ID); n != 2 {
					t.Fatal("revoked queue not suppressed", mode, n)
				}
			}
		})
	}
}

func surveySubscriptionBackend(t *testing.T, db *sql.DB, c authority.Config, namespace string) *application.Backend {
	t.Helper()
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(db, nil, application.Config{Authority: c, PublicVisibility: &visibility, MessagerPolicy: &policy, NotificationApp: namespace})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	return app
}
