// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"solsynth.local/go-migration/internal/notabledaypg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/passport/notabledays"
	"solsynth.local/go-migration/ports/passport/social"
)

func TestPGCalendarActivityStringProjectionPrivacyAndTimeZone(t *testing.T) {
	f := newCalendarFixture(t)
	status, checkin := authorityUUID(t), authorityUUID(t)
	created := calendarTime("2026-10-08T01:00:00Z")
	query(t, f.db, `INSERT INTO owned_presence_statuses(id,account_id,attitude,type,label,meta,created_at,updated_at,cleared_at)VALUES($1::uuid,$2::uuid,0,3,'Visible label','{"private_device":"do not project"}'::jsonb,$3::timestamptz,$3::timestamptz,$3::timestamptz+interval '1 hour')`, status, f.actor, created)
	query(t, f.db, `INSERT INTO owned_presence_preferences(account_id,status_visibility,updated_at)VALUES($1::uuid,'private',$2::timestamptz)`, f.actor, f.now)
	query(t, f.db, `INSERT INTO owned_progression_checkins(id,account_id,day,level,reward_experience,created_at)VALUES($1::uuid,$2::uuid,'2026-10-07'::date,2,100,$3::timestamptz)`, checkin, f.actor, created)
	f.config.Activity = notabledaypg.CanonicalActivityOverlay{}
	f = calendarPool(t, f, f.db)
	h := calendarHTTP(f)
	path := "/api/accounts/me/calendar/merged?year=2026&month=10&timeZone=America%2FNew_York"
	w := calendarCall(t, h, "GET", path, f.token, "", "", 200)
	mine := backendDecode[notabledays.MergedCalendar](t, w)
	if len(mine.ActivityEvents) != 2 || mine.ActivityEvents[0].Kind != "check_in" || mine.ActivityEvents[0].ID != checkin || mine.ActivityEvents[1].Kind != "status" || mine.ActivityEvents[1].ID != status || strings.Contains(w.Body.String(), "private_device") {
		t.Fatal(w.Body.String())
	}
	for _, v := range mine.MergedEvents {
		if v.ID != nil && (*v.ID == checkin || *v.ID == status) {
			t.Fatal("guessed numeric type", v)
		}
	}
	if len(mine.UnavailableComponents) != 2 || mine.UnavailableComponents[0] != "merged_check_ins" || mine.UnavailableComponents[1] != "merged_statuses" {
		t.Fatal(mine.UnavailableComponents)
	}
	days := backendDecode[[]notabledays.DailyCalendar](t, calendarCall(t, h, "GET", "/api/accounts/me/calendar?year=2026&month=10&timeZone=America%2FNew_York", f.token, "", "", 200))
	if len(days[6].Statuses) != 1 || string(days[6].CheckInResult) == "null" || len(days[7].Statuses) != 0 {
		t.Fatal("local date shifted", days[6], days[7])
	}
	calendarFriend(t, f)
	otherPath := "/api/accounts/" + f.actor + "/calendar/merged?year=2026&month=10&timeZone=America%2FNew_York"
	other := backendDecode[notabledays.MergedCalendar](t, calendarCall(t, h, "GET", otherPath, f.otherToken, "", "", 200))
	if len(other.ActivityEvents) != 0 {
		t.Fatal("private activity leak", other.ActivityEvents)
	}
	query(t, f.db, `UPDATE owned_presence_preferences SET status_visibility='friends' WHERE account_id=$1::uuid`, f.actor)
	other = backendDecode[notabledays.MergedCalendar](t, calendarCall(t, h, "GET", otherPath, f.otherToken, "", "", 200))
	if len(other.ActivityEvents) != 1 || other.ActivityEvents[0].ID != status {
		t.Fatal(other.ActivityEvents)
	}
	var projected map[string]any
	if e := json.Unmarshal(other.Statuses[0], &projected); e != nil {
		t.Fatal(e)
	}
	if projected["type"] != float64(0) || projected["meta"] != nil {
		t.Fatal(projected)
	}
	anonymous := backendDecode[notabledays.MergedCalendar](t, calendarCall(t, h, "GET", otherPath, "", "", "", 200))
	if len(anonymous.ActivityEvents) != 0 {
		t.Fatal(anonymous.ActivityEvents)
	}
	query(t, f.db, `UPDATE owned_presence_preferences SET status_visibility='public' WHERE account_id=$1::uuid`, f.actor)
	anonymous = backendDecode[notabledays.MergedCalendar](t, calendarCall(t, h, "GET", otherPath, "", "", "", 200))
	if len(anonymous.ActivityEvents) != 1 || anonymous.ActivityEvents[0].ID != status {
		t.Fatal(anonymous.ActivityEvents)
	}
	if _, e := socialpg.New(f.db).ChangeRelationship(context.Background(), f.actor, f.other, social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	calendarCall(t, h, "GET", otherPath, f.otherToken, "", "", 404)
	query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind)VALUES($1::uuid,'disable_account')`, f.actor)
	calendarCall(t, h, "GET", otherPath, "", "", "", 404)
}

func TestPGCalendarActivityRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	type saved struct {
		Actor, Other, Status, CheckIn string
		Token, OtherToken             string
		Authority                     restartAuthoritySnapshot
	}
	if phase == "before" {
		f := calendarRestartAuthority(t, newCalendarFixture(t))
		calendarFriend(t, f)
		status, checkin := authorityUUID(t), authorityUUID(t)
		query(t, f.db, `INSERT INTO owned_presence_statuses(id,account_id,attitude,type,label,created_at,updated_at)VALUES($1::uuid,$2::uuid,0,0,'Durable visible label',$3::timestamptz,$3::timestamptz)`, status, f.actor, f.now)
		query(t, f.db, `INSERT INTO owned_presence_preferences(account_id,status_visibility,updated_at)VALUES($1::uuid,'friends',$2::timestamptz)`, f.actor, f.now)
		query(t, f.db, `INSERT INTO owned_progression_checkins(id,account_id,day,level,reward_experience,created_at)VALUES($1::uuid,$2::uuid,'2026-10-08'::date,2,100,$3::timestamptz)`, checkin, f.actor, f.now)
		saveRestartFixture(t, "calendar-activity.json", saved{Actor: f.actor, Other: f.other, Status: status, CheckIn: checkin, Token: f.token, OtherToken: f.otherToken, Authority: saveAuthoritySnapshot(t, f.authConfig)})
		return
	}
	var state saved
	loadRestartFixture(t, "calendar-activity.json", &state)
	cfg := restoreAuthoritySnapshot(t, state.Authority)
	f := calendarFixture{db: db, actor: state.Actor, other: state.Other, authConfig: cfg, token: state.Token, otherToken: state.OtherToken, now: state.Authority.Now, config: notabledaypg.Config{Activity: notabledaypg.CanonicalActivityOverlay{}}}
	f = calendarPool(t, f, db)
	h := calendarHTTP(f)
	mine := backendDecode[notabledays.MergedCalendar](t, calendarCall(t, h, "GET", "/api/accounts/me/calendar/merged?year=2026&month=10", f.token, "", "", 200))
	if len(mine.ActivityEvents) != 2 || mine.ActivityEvents[0].ID != state.CheckIn || mine.ActivityEvents[1].ID != state.Status {
		t.Fatal(mine.ActivityEvents)
	}
	path := "/api/accounts/" + state.Actor + "/calendar/merged?year=2026&month=10"
	other := backendDecode[notabledays.MergedCalendar](t, calendarCall(t, h, "GET", path, f.otherToken, "", "", 200))
	if len(other.ActivityEvents) != 1 || other.ActivityEvents[0].ID != state.Status {
		t.Fatal(other.ActivityEvents)
	}
	anonymous := backendDecode[notabledays.MergedCalendar](t, calendarCall(t, h, "GET", path, "", "", "", 200))
	if len(anonymous.ActivityEvents) != 0 {
		t.Fatal("saved privacy lost", anonymous.ActivityEvents)
	}
}
