// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/ports/passport/tickets"
	"testing"
)

func TestPGBackendTicketsExamProgressionComposition(t *testing.T) {
	f := newTicketFixture(t)
	exam, questions := seedExam(t, f, false)
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.authorityConfig, PublicVisibility: &visibility, MessagerPolicy: &policy, Tickets: &f.config})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	ticket := backendDecode[tickets.Ticket](t, backendCall(t, app, "POST", "/passport/tickets", f.token, `{"title":"Composed support","content":"Private support request","type":0}`, 200))
	backendCall(t, app, "GET", "/api/tickets/"+ticket.ID, f.otherToken, "", 404)
	backendCall(t, app, "GET", "/passport/tickets/"+ticket.ID, f.token, "", 200)
	start := backendDecode[tickets.Attempt](t, backendCall(t, app, "POST", "/passport/tests/"+exam.Key+"/attempts", f.token, "{}", 200))
	body, e := json.Marshal(map[string]any{"answers": []tickets.AnswerInput{correctInput(questions[0])}})
	if e != nil {
		t.Fatal(e)
	}
	path := "/api/tests/attempts/" + start.ID + "/submit"
	done := backendDecode[tickets.Attempt](t, backendCall(t, app, "POST", path, f.token, string(body), 200))
	if done.Status != tickets.Passed || done.RewardPending || done.PermissionClaim != "granted" {
		t.Fatal("exam effects not canonical", done)
	}
	backendCall(t, app, "POST", path, f.token, string(body), 200)
	var count int
	var xp int64
	if e = f.db.QueryRow(`SELECT count(*),coalesce(sum(delta),0) FROM owned_progression_experience WHERE account_id=$1 AND reason_type='exam'`, f.actor).Scan(&count, &xp); e != nil || count != 1 || xp != 11 {
		t.Fatal("exam XP replay", count, xp, e)
	}
	backendCall(t, app, "GET", "/api/tests/attempts/"+start.ID, f.otherToken, "", 404)
}
