// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/ports/passport/accounts"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
	"testing"
)

func TestPGBackendLivestreamChatComposition(t *testing.T) {
	f := newBackendFixture(t)
	provider := &streamChatProvider{streamProvider: &streamProvider{endpoint: "wss://synthetic-chat-" + f.actor + ".invalid"}}
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	chat := chatStreamConfig()
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, Livestreams: &application.LivestreamConfig{Provider: provider, Policy: streamConfig(), Chat: &chat}})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at)VALUES($1::uuid,2,$2::timestamptz)`, f.actor, f.config.Now())
	pub := backendDecode[accounts.Publisher](t, backendJSONCall(t, app, "POST", "/sphere/publishers/individual", f.token, map[string]string{"name": "livechat-" + f.actor, "nick": "Synthetic chat"}, 200))
	stream := backendDecode[live.Stream](t, backendJSONCall(t, app, "POST", "/sphere/livestreams?pub="+pub.Name, f.token, map[string]string{"title": "Synthetic chat stream"}, 200))
	path := "/api/livestreams/" + stream.ID
	backendJSONCall(t, app, "POST", path+"/start", f.token, map[string]bool{"no_ingress": true}, 200)
	viewer := backendDecode[live.TokenResponse](t, backendCall(t, app, "GET", path+"/token", f.otherToken, "", 200))
	backendCall(t, app, "GET", path+"/chat", "", "", 401)
	msg := backendDecode[live.ChatMessage](t, backendJSONCall(t, app, "POST", path+"/chat", f.otherToken, map[string]string{"content": "Synthetic persisted chat"}, 200))
	if msg.SenderID != f.other {
		t.Fatal("wrong canonical sender")
	}
	if _, e = app.ProcessExternalDue(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	packets := provider.snapshot()
	if len(packets) != 1 || len(packets[0].Recipients) != 1 || packets[0].Recipients[0] != viewer.Identity {
		t.Fatal("explicit eligible delivery")
	}
	history := backendDecode[[]live.ChatMessage](t, backendCall(t, app, "GET", "/sphere/livestreams/"+stream.ID+"/chat", f.token, "", 200))
	if len(history) != 1 || history[0].ID != msg.ID || history[0].DeliveryState != "accepted" {
		t.Fatal("durable provider acceptance")
	}
	backendCall(t, app, "DELETE", path+"/chat/"+msg.ID, f.token, "", 200)
	history = backendDecode[[]live.ChatMessage](t, backendCall(t, app, "GET", path+"/chat", f.otherToken, "", 200))
	if len(history) != 0 {
		t.Fatal("deleted chat retained in history")
	}
	backendCall(t, app, "POST", path+"/end", f.token, "", 200)
}
