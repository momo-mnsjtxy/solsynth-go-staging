// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"fmt"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/passport/social"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"testing"
)

func TestPGBackendChatDetailsAuthoredRoutesAndFailClosedPolicy(t *testing.T) {
	f := newBackendFixture(t)
	scope := detailScope(t, f.db, f.actor)
	room := backendDecode[rooms.Room](t, backendCall(t, f.app, "POST", "/messager/chat", f.token, `{"name":"Details","slug":"details","is_public":true,"is_community":true}`, 200))
	for _, prefix := range []string{"/api/chat/", "/messager/chat/"} {
		got := backendDecode[rooms.Room](t, backendCall(t, f.app, "GET", prefix+"public/"+scope+"/details", "", "", 200))
		if got.ID != room.ID {
			t.Fatal(got)
		}
		backendCall(t, f.app, "GET", prefix+"public/"+scope+"/details", "invalid", "", 401)
		online := backendDecode[rooms.OnlineMembers](t, backendCall(t, f.app, "GET", prefix+room.ID+"/members/online", "", "", 200))
		if online.OnlineCount != 0 {
			t.Fatal(online)
		}
		backendCall(t, f.app, "PATCH", prefix+room.ID+"/members/me/profile", f.token, `{"nick":"closed"}`, 503)
		backendCall(t, f.app, "DELETE", prefix+room.ID+"/members/me/profile", f.token, "", 200)
	}
	backendCall(t, f.app, "PATCH", "/api/chat/"+room.ID, f.token, `{"is_public":false,"is_community":false}`, 200)
	backendCall(t, f.app, "GET", "/api/chat/public/"+scope+"/details", "", "", 404)
	backendCall(t, f.app, "GET", "/api/chat/"+room.ID+"/members/online", f.otherToken, "", 404)
	query(t, f.db, `INSERT INTO owned_realm_quota(account_id,total,starts_at)VALUES($1,2,now())`, f.actor)
	realm := backendDecode[social.Realm](t, backendCall(t, f.app, "POST", "/passport/realms", f.token, fmt.Sprintf(`{"slug":%q,"name":"Moderation","is_public":true,"is_community":true}`, "details-"+f.actor), 200))
	linked := backendDecode[rooms.Room](t, backendCall(t, f.app, "POST", "/api/chat", f.token, fmt.Sprintf(`{"name":"Moderation room","realm_id":%q,"is_public":true,"is_community":true}`, realm.ID), 200))
	backendCall(t, f.app, "POST", "/passport/realms/"+realm.ID+"/members/me", f.otherToken, "", 200)
	backendCall(t, f.app, "POST", "/api/chat/"+linked.ID+"/members/me", f.otherToken, "", 200)
	// The aliases pass through the same host proof stripping and transaction bind.
	m := backendDecode[struct {
		ID string `json:"id"`
	}](t, backendCall(t, f.app, "POST", "/api/chat/"+linked.ID+"/messages", f.otherToken, `{"content":"moderate through host"}`, 200))
	backendCall(t, f.app, "DELETE", "/messager/chat/rooms/"+linked.ID+"/messages/"+m.ID, f.token, `{"reason":"host moderation"}`, 204)
	backendCall(t, f.app, "POST", "/messager/chat/rooms/"+linked.ID+"/members/"+f.other+"/timeout", f.token, `{"duration_minutes":1}`, 200)
	backendCall(t, f.app, "POST", "/api/chat/"+linked.ID+"/messages", f.otherToken, `{"content":"timed out"}`, 403)
}
func TestPGBackendChatDetailsCanonicalProfileConfiguration(t *testing.T) {
	f := newCommerceFixture(t)
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	sub, e := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: f.plan}, "chat-profile-host", false)
	if e != nil {
		t.Fatal(e)
	}
	order, e := f.r.SubscriptionOrder(f.ctx, f.actor, sub.Identifier)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.PayOrder(f.ctx, f.actor, order.ID, &f.wallet.ID); e != nil {
		t.Fatal(e)
	}
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, ChatProfilePerks: map[string]int{f.plan: 2}})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	room := backendDecode[rooms.Room](t, backendCall(t, app, "POST", "/messager/chat", f.token, `{"name":"Paid profile host"}`, 200))
	path := "/messager/chat/" + room.ID + "/members/me/profile"
	member := backendDecode[rooms.Member](t, backendCall(t, app, "PATCH", path, f.token, `{"nick":"Canonical paid nickname"}`, 200))
	if member.Nick == nil || *member.Nick != "Canonical paid nickname" || member.Account == nil {
		t.Fatal(member)
	}
	backendCall(t, app, "DELETE", path, f.token, "", 200)
}
