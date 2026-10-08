// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"fmt"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/develop/bots"
	profiles "solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/notabledays"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/posts"
	"strings"
	"testing"
)

func TestPGBackendBotMiniappComposition(t *testing.T) {
	f := newBackendFixture(t)
	suffix := strings.ReplaceAll(f.actor, "-", "")
	query(t, f.db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at)VALUES($1,2,now())`, f.actor)
	pub := backendDecode[profiles.Publisher](t, backendCall(t, f.app, "POST", "/sphere/publishers/individual", f.token, fmt.Sprintf(`{"name":%q,"nick":"Bot owner"}`, "portal_"+suffix), 200))
	backendCall(t, f.app, "POST", "/develop/developers/"+pub.Name+"/enroll", f.token, "", 200)
	project := backendDecode[apps.Project](t, backendCall(t, f.app, "POST", "/develop/private/projects?dev="+pub.Name, f.token, `{"slug":"portal","name":"Portal"}`, 201))
	q := "?dev=" + pub.Name + "&proj=" + project.ID
	app := backendDecode[apps.App](t, backendCall(t, f.app, "POST", "/develop/private/apps"+q, f.token, fmt.Sprintf(`{"slug":%q,"name":"Portal app","status":1}`, "portal-"+suffix), 201))
	q += "&app=" + app.ID
	query(t, f.db, `INSERT INTO owned_bot_quotas(publisher_id,bots_total,miniapps_total,starts_at)VALUES($1,2,2,now())`, pub.ID)
	bot := backendDecode[bots.Bot](t, backendCall(t, f.app, "POST", "/develop/private/bots"+q, f.token, fmt.Sprintf(`{"name":%q,"nick":"Composed bot","slug":"composed"}`, "synthetic_bot_"+suffix), 200))
	if bot.Account.ID == f.actor || bot.Account.AutomatedID != bot.ID {
		t.Fatal("canonical bot identity", bot)
	}
	backendCall(t, f.app, "GET", "/api/bots/"+bot.ID, "", "", 200)
	backendCall(t, f.app, "POST", "/develop/private/bots"+q, f.otherToken, fmt.Sprintf(`{"name":%q,"nick":"Denied","slug":"denied"}`, "synthetic_other_"+suffix), 403)
	mini := backendDecode[bots.MiniApp](t, backendCall(t, f.app, "POST", "/develop/private/miniapps"+q, f.token, fmt.Sprintf(`{"slug":%q,"stage":2,"manifest":{"id":%q,"name":"Composed miniapp","version":"1.0.0","entry":"main.js","permissions":[]}}`, "mini-"+suffix, "test.synthetic."+suffix), 201))
	backendCall(t, f.app, "GET", "/api/miniapps/"+mini.Slug, "", "", 200)
	backendCall(t, f.app, "GET", "/api/miniapps/"+mini.Slug+"/package", "", "", 404)
	backendCall(t, f.app, "DELETE", "/develop/private/bots/"+bot.ID+q, f.token, "", 204)
	backendCall(t, f.app, "GET", "/api/bots/"+bot.ID, "", "", 404)
}

func TestPGBackendCalendarComposition(t *testing.T) {
	f := newBackendFixture(t)
	query(t, f.db, `UPDATE owned_authority_accounts SET region='CN' WHERE id=$1`, f.actor)
	body := `{"title":"Composed calendar","start_time":"2026-03-07T14:00:00Z","end_time":"2026-03-07T15:00:00Z","time_zone":"America/New_York","recurrence":{"frequency":1,"interval":1,"occurrences":3}}`
	event := backendDecode[notabledays.Event](t, backendCall(t, f.app, "POST", "/passport/accounts/me/calendar/events", f.token, body, 201))
	backendCall(t, f.app, "GET", "/api/accounts/unknown/calendar/events/"+event.ID, f.otherToken, "", 404)
	month := backendDecode[[]notabledays.DailyCalendar](t, backendCall(t, f.app, "GET", "/passport/accounts/me/calendar?year=2026&month=3&timeZone=America%2FNew_York", f.token, "", 200))
	if len(month) != 31 || len(month[7].UserEvents) != 1 {
		t.Fatal("calendar route or recurrence composition", len(month))
	}
	backendCall(t, f.app, "PUT", "/api/accounts/me/calendar/events/"+event.ID, f.token, `{"visibility":200}`, 200)
	backendCall(t, f.app, "GET", "/passport/accounts/unknown/calendar/events/"+event.ID, "", "", 200)
	backendCall(t, f.app, "GET", "/api/notable-days?year=2026&region=CN", "", "", 200)
	backendCall(t, f.app, "DELETE", "/api/accounts/me/calendar/events/"+event.ID, f.token, "", 204)
	backendCall(t, f.app, "GET", "/api/accounts/unknown/calendar/events/"+event.ID, "", "", 404)
}

func TestPGBackendPostRealmComposition(t *testing.T) {
	f := newBackendFixture(t)
	suffix := strings.ReplaceAll(f.actor, "-", "")
	query(t, f.db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at) VALUES($1,2,now())`, f.actor)
	query(t, f.db, `INSERT INTO owned_realm_quota(account_id,total,starts_at) VALUES($1,2,now())`, f.actor)
	pub := backendDecode[profiles.Publisher](t, backendCall(t, f.app, "POST", "/sphere/publishers/individual", f.token, fmt.Sprintf(`{"name":%q,"nick":"Realm author"}`, "realm_author_"+suffix), 200))
	realm := backendDecode[social.Realm](t, backendCall(t, f.app, "POST", "/passport/realms", f.token, fmt.Sprintf(`{"slug":%q,"name":"Private scope","is_public":false}`, "private_"+suffix), 200))
	post := backendDecode[posts.Post](t, backendCall(t, f.app, "POST", "/sphere/posts?pub="+pub.Name, f.token, fmt.Sprintf(`{"content":"Composed scoped post","visibility":0,"realm_id":%q}`, realm.ID), 200))
	if post.RealmID == nil || *post.RealmID != realm.ID {
		t.Fatal("realm projection missing")
	}
	backendCall(t, f.app, "GET", "/sphere/posts/"+post.ID, "", "", 404)
	backendCall(t, f.app, "GET", "/sphere/posts/"+post.ID, f.otherToken, "", 404)
	backendCall(t, f.app, "GET", "/sphere/posts/"+post.ID, f.token, "", 200)
	backendCall(t, f.app, "PATCH", "/sphere/posts/"+post.ID, f.token, `{"title":"Preserved scope"}`, 200)
	backendCall(t, f.app, "GET", "/sphere/posts/"+post.ID, "", "", 404)
	backendCall(t, f.app, "PATCH", "/sphere/posts/"+post.ID, f.token, `{"realm_id":null}`, 200)
	backendCall(t, f.app, "GET", "/sphere/posts/"+post.ID, "", "", 200)
}
