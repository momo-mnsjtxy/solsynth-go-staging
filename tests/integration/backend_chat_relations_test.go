// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"fmt"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/passport/social"
	"strings"
	"testing"
)

func TestPGBackendChatRelationsComposition(t *testing.T) {
	f := newBackendFixture(t)
	dm := backendDecode[rooms.Room](t, backendJSONCall(t, f.app, "POST", "/messager/chat/direct", f.token, rooms.DirectRequest{RelatedUserID: f.other}, 200))
	if dm.Type != 1 || dm.IsPublic {
		t.Fatal("direct projection")
	}
	backendJSONCall(t, f.app, "POST", "/api/chat/"+dm.ID+"/messages", f.otherToken, rooms.TextRequest{Content: "Unconfirmed"}, 403)
	backendJSONCall(t, f.app, "POST", "/api/chat/"+dm.ID+"/messages", f.token, rooms.TextRequest{Content: "Synthetic message"}, 200)
	backendCall(t, f.app, "GET", "/messager/chat/"+dm.ID+"/messages", f.otherToken, "", 200)
	backendCall(t, f.app, "POST", "/messager/chat/"+dm.ID+"/block", f.otherToken, "", 204)
	backendCall(t, f.app, "GET", "/messager/chat/"+dm.ID+"/messages", f.otherToken, "", 403)
	backendCall(t, f.app, "DELETE", "/messager/chat/"+dm.ID+"/block", f.otherToken, "", 200)
	backendCall(t, f.app, "POST", "/messager/chat/"+dm.ID+"/confirm", f.otherToken, "", 200)
	backendJSONCall(t, f.app, "POST", "/api/chat/"+dm.ID+"/messages", f.otherToken, rooms.TextRequest{Content: "Confirmed"}, 200)
	query(t, f.db, `INSERT INTO owned_realm_quota(account_id,total,starts_at)VALUES($1,2,now())`, f.actor)
	realm := backendDecode[social.Realm](t, backendCall(t, f.app, "POST", "/passport/realms", f.token, fmt.Sprintf(`{"slug":%q,"name":"Chat scope","is_public":false}`, "chat_"+strings.ReplaceAll(f.actor, "-", "")), 200))
	yes := true
	name := "Private realm public room"
	room := backendDecode[rooms.Room](t, backendJSONCall(t, f.app, "POST", "/messager/chat", f.token, rooms.RoomPatch{Name: &name, RealmID: &realm.ID, IsPublic: &yes, IsCommunity: &yes}, 200))
	if room.RealmID == nil || *room.RealmID != realm.ID {
		t.Fatal("realm projection")
	}
	listed := backendDecode[[]rooms.Room](t, backendCall(t, f.app, "GET", "/api/realms/"+realm.Slug+"/chat", f.token, "", 200))
	if len(listed) != 1 || listed[0].ID != room.ID {
		t.Fatal("realm chat routing")
	}
	backendCall(t, f.app, "GET", "/messager/realms/"+realm.Slug+"/chat", f.otherToken, "", 403)
	backendCall(t, f.app, "GET", "/api/chat/"+room.ID+"/messages", f.otherToken, "", 403)
}
