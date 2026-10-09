// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import (
	"context"
	"encoding/json"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"testing"
)

type subscriptionSyncFake struct {
	OrganizationRepository
	calls       int
	actor, room string
	global      GlobalSyncRequest
	joined      JoinedRoomSyncRequest
}

func (f *subscriptionSyncFake) RoomSubscriptions(_ context.Context, a, r string) ([]RoomSubscription, error) {
	f.calls++
	f.actor = a
	f.room = r
	return []RoomSubscription{}, nil
}
func (f *subscriptionSyncFake) AccountSubscriptions(_ context.Context, a string) ([]AccountSubscription, error) {
	f.calls++
	f.actor = a
	return []AccountSubscription{}, nil
}
func (f *subscriptionSyncFake) ChatStatus(_ context.Context, a string) (ChatAccountStatus, error) {
	f.calls++
	f.actor = a
	return ChatAccountStatus{AccountID: a, PushNotificationsMaySendForUnsubscribedRooms: true, Subscriptions: []SubscriptionRoomStatus{}}, nil
}
func (f *subscriptionSyncFake) GlobalSync(_ context.Context, a string, q GlobalSyncRequest) (GlobalSyncResponse, error) {
	f.calls++
	f.actor = a
	f.global = q
	return GlobalSyncResponse{}, nil
}
func (f *subscriptionSyncFake) JoinedRoomSync(_ context.Context, a string, q JoinedRoomSyncRequest) (JoinedRoomSyncResponse, error) {
	f.calls++
	f.actor = a
	f.joined = q
	return JoinedRoomSyncResponse{}, nil
}
func TestSubscriptionSyncHTTPContract(t *testing.T) {
	for _, v := range []struct{ method, path, body, permission string }{
		{"GET", "/" + testRoom + "/subscriptions", "", ""},
		{"GET", "/accounts/me/subscriptions", "", ""},
		{"GET", "/accounts/me/status", "", ""},
		{"POST", "/sync", `{"last_sync_timestamp":123,"last_sync_message_id":"` + testRoom + `"}`, "chat.sync"},
		{"POST", "/rooms/sync", `{"last_sync_timestamp":123}`, "chat.sync"},
	} {
		t.Run(v.path, func(t *testing.T) {
			a := &fakeAuthority{}
			f := &subscriptionSyncFake{}
			h := (&Service{Authority: a, Organization: f}).Handler()
			w := call(h, v.method, "/api/chat"+v.path, v.body, "Bearer fixture")
			if w.Code != 200 || f.calls != 1 || f.actor != testActor || a.permission != v.permission || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(w.Body.String(), w.Code, f, a)
			}
			if v.path == "/sync" && (f.global.LastSyncTimestamp != 123 || f.global.LastSyncMessageID == nil || *f.global.LastSyncMessageID != testRoom) {
				t.Fatal(f.global)
			}
			if v.path == "/rooms/sync" && f.joined.LastSyncTimestamp != 123 {
				t.Fatal(f.joined)
			}
			w = call(h, v.method, "/api/chat"+v.path, v.body, "")
			if w.Code != 401 || f.calls != 1 {
				t.Fatal("anonymous", w.Code, f)
			}
		})
	}
}
func TestSubscriptionSyncRejectsMalformedOrStaleRequests(t *testing.T) {
	for _, v := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/sync", `{}`, 400}, {"POST", "/sync", `{"last_sync_timestamp":null}`, 400}, {"POST", "/rooms/sync", `{}`, 400}, {"POST", "/sync", `{"last_sync_timestamp":-1}`, 400}, {"POST", "/sync", `{"last_sync_timestamp":253402300800000}`, 400},
		{"POST", "/sync", `{"last_sync_timestamp":1,"last_sync_timestamp":2}`, 400}, {"POST", "/sync", `{"last_sync_timestamp":1.2}`, 400},
		{"POST", "/sync", `{"last_sync_timestamp":0,"last_sync_message_id":"bad"}`, 400}, {"POST", "/sync", `{"last_sync_timestamp":0,"missing_sequences":[1]}`, 501},
		{"POST", "/rooms/sync", `{"last_sync_timestamp":0,"last_sync_message_id":"` + testRoom + `"}`, 400},
		{"POST", "/sync?take=10", `{"last_sync_timestamp":0}`, 400}, {"GET", "/accounts/me/status?account_id=x", "", 400},
		{"GET", "/accounts/me/subscriptions", `{"account_id":"x"}`, 400}, {"GET", "/bad/subscriptions", "", 400},
		{"GET", "/sync", "", 405}, {"POST", "/accounts/me/status", "{}", 405},
	} {
		f := &subscriptionSyncFake{}
		h := (&Service{Authority: &fakeAuthority{}, Organization: f}).Handler()
		w := call(h, v.method, "/api/chat"+v.path, v.body, "Bearer fixture")
		if w.Code != v.status || f.calls != 0 {
			t.Fatal(v, w.Code, w.Body.String(), f)
		}
	}
	f := &subscriptionSyncFake{}
	h := (&Service{Authority: &fakeAuthority{denied: authority.ErrRevoked}, Organization: f}).Handler()
	w := call(h, "GET", "/api/chat/accounts/me/status", "", "Bearer fixture")
	if w.Code != 401 || f.calls != 0 {
		t.Fatal(w, f)
	}
}
func TestSubscriptionStatusSerializationNamesAndNoCredential(t *testing.T) {
	v := ChatAccountStatus{AccountID: testActor, Subscriptions: []SubscriptionRoomStatus{{Devices: []SubscriptionDeviceStatus{{SubscriptionDevice: SubscriptionDevice{DeviceToken: testRoom}, IsWebSocketConnected: true}}}}, HasAnyWebSocketConnection: true}
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	s := string(b)
	for _, key := range []string{`"has_any_web_socket_connection":true`, `"is_web_socket_connected":true`, `"device_token":"` + testRoom + `"`} {
		if !strings.Contains(s, key) {
			t.Fatal(s, key)
		}
	}
	for _, key := range []string{"session_id", "authorization_epoch", "refresh_hash", "account_version"} {
		if strings.Contains(s, key) {
			t.Fatal(s)
		}
	}
}
