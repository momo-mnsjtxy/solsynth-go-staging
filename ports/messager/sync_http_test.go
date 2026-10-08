// SPDX-License-Identifier: AGPL-3.0-only
package messager

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const syncRoom = "b0000000-0000-0000-0000-000000000001"
const syncAccount = "b0000000-0000-0000-0000-000000000002"
const syncMessage = "b0000000-0000-0000-0000-000000000003"

type syncFake struct {
	calls        int
	syncQuery    SyncQuery
	listQuery    MessageListQuery
	messageQuery MessageQuery
	err          error
	response     SyncResponse
}

func (f *syncFake) Sync(_ context.Context, q SyncQuery) (SyncResponse, error) {
	f.calls++
	f.syncQuery = q
	return f.response, f.err
}
func (f *syncFake) ListMessages(_ context.Context, q MessageListQuery) (MessagePage, error) {
	f.calls++
	f.listQuery = q
	return MessagePage{Messages: []MessageProjection{}, TotalCount: 3}, f.err
}
func (f *syncFake) GetMessage(_ context.Context, q MessageQuery) (MessageProjection, error) {
	f.calls++
	f.messageQuery = q
	return MessageProjection{ID: syncMessage}, f.err
}
func syntheticSync(t *testing.T, f *syncFake, account string) http.Handler {
	t.Helper()
	h, err := NewSyntheticProjectionSyncHandler(NewSyntheticProjectionSyncService(f), account)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func syncHTTP(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
	return w
}
func TestSyncDefaultConstructionDeniesBeforeReaders(t *testing.T) {
	f := &syncFake{}
	for _, service := range []*SyncService{nil, {}, NewSyncService(f)} {
		if _, err := service.Sync(context.Background(), SyncQuery{}); !errors.Is(err, ErrSyncPolicyUnverified) {
			t.Fatal(err)
		}
	}
	for _, h := range []http.Handler{NewSyncHandler(NewSyntheticProjectionSyncService(f)), func() http.Handler {
		h, _ := NewSyntheticProjectionSyncHandler(NewSyncService(f), syncAccount)
		return h
	}()} {
		w := syncHTTP(h, "POST", "/api/chat/"+syncRoom+"/sync", `{"last_sync_timestamp":0}`)
		if w.Code != 503 || w.Header().Get("X-Total") != "" || f.calls != 0 {
			t.Fatal(w, f.calls)
		}
	}
}
func TestSyncHTTPRecoversInt64AndPreservesProjection(t *testing.T) {
	f := &syncFake{response: SyncResponse{Messages: []MessageProjection{}, CurrentTimestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), TotalCount: 1234}}
	w := syncHTTP(syntheticSync(t, f, syncAccount), "POST", "/api/chat/"+strings.ToUpper(syncRoom)+"/sync", `{"last_sync_timestamp":9223372036854775807,"missing_sequences":[9007199254740993,9223372036854775807],"missing_sequence_ranges":[{"start_sequence":9,"end_sequence":7}],"last_sync_message_id":"`+syncMessage+`"}`)
	if w.Code != 200 || w.Header().Get("X-Total") != "1234" || w.Header().Get("X-Solsynth-Projection") != SyncProjectionVersion || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w)
	}
	q := f.syncQuery
	if q.RoomID != syncRoom || q.AccountID != syncAccount || q.Request.MissingSequences[0] != 9007199254740993 || q.Request.MissingSequences[1] != math.MaxInt64 || q.Request.LastSyncTimestamp != math.MaxInt64 {
		t.Fatal(q)
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body) != 3 || string(body["messages"]) != "[]" || body["current_message_id"] != nil {
		t.Fatal(w.Body.String())
	}
}
func TestSyncHTTPBindingAndCredentialRejection(t *testing.T) {
	bad := []string{``, `null`, `[]`, `{}`, `{"last_sync_timestamp":null}`, `{"last_sync_timestamp":"0"}`, `{"last_sync_timestamp":1.5}`, `{"last_sync_timestamp":9223372036854775808}`, `{"last_sync_timestamp":0,"last_sync_timestamp":1}`, `{"last_sync_timestamp":0} {}`, `{"last_sync_timestamp":0,"unknown":true}`, `{"last_sync_timestamp":0,"missing_sequences":["3"]}`, `{"last_sync_timestamp":0,"last_sync_message_id":"bad"}`, `{"last_sync_timestamp":0,"missing_sequence_ranges":[{"start_sequence":1,"end_sequence":9223372036854775808}]}`, `{"last_sync_timestamp":0,"last_sync_message_id":"` + strings.Repeat("x", 1<<20) + `"}`}

	bad = append(bad, `{"last_sync_timestamp":0,"missing_sequences":[null]}`, `{"last_sync_timestamp":0,"missing_sequence_ranges":[null]}`, `{"last_sync_timestamp":0,"missing_sequence_ranges":[{"start_sequence":null,"end_sequence":5}]}`)
	f := &syncFake{}
	h := syntheticSync(t, f, syncAccount)
	for _, body := range bad {
		w := syncHTTP(h, "POST", "/api/chat/"+syncRoom+"/sync", body)
		if w.Code != 400 || f.calls != 0 {
			t.Fatalf("invalid body len=%d => %d calls=%d", len(body), w.Code, f.calls)
		}
	}
	for _, header := range []string{"Authorization", "Cookie", "X-Test-Account", "X-Account-ID"} {
		req := httptest.NewRequest("POST", "/api/chat/"+syncRoom+"/sync", strings.NewReader(`{"last_sync_timestamp":0}`))
		req.Header[header] = []string{""}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 403 || f.calls != 0 {
			t.Fatal(header, w.Code)
		}
	}
	for _, path := range []string{"/messager/chat/" + syncRoom + "/sync", "/api/chat/bad/sync", "/api/chat/" + syncRoom + "/sync/", "/api/chat/" + syncRoom + "/sync/" + syncMessage} {
		if w := syncHTTP(h, "POST", path, `{}`); w.Code != 404 {
			t.Fatal(path, w.Code)
		}
	}
	w := syncHTTP(h, "GET", "/api/chat/"+syncRoom+"/sync", "")
	if w.Code != 405 || w.Header().Get("Allow") != "POST" {
		t.Fatal(w)
	}
	w = syncHTTP(syntheticSync(t, f, ""), "POST", "/api/chat/"+syncRoom+"/sync", `{}`)
	if w.Code != 401 || f.calls != 0 {
		t.Fatal(w)
	}
}
func TestMessageHTTPPaginationAndIndividualRead(t *testing.T) {
	f := &syncFake{}
	h := syntheticSync(t, f, "")
	w := syncHTTP(h, "GET", "/api/chat/"+syncRoom+"/messages?take=0&offset=3", "")
	if w.Code != 200 || w.Body.String() != "[]\n" || w.Header().Get("X-Total") != "3" || f.listQuery.Take != 0 || f.listQuery.Offset != 3 || f.listQuery.AccountID != "" {
		t.Fatal(w, f)
	}
	w = syncHTTP(h, "GET", "/api/chat/"+syncRoom+"/messages/"+syncMessage, "")
	if w.Code != 200 || w.Header().Get("X-Total") != "" || f.messageQuery.MessageID != syncMessage {
		t.Fatal(w, f)
	}
	for _, q := range []string{"take=-1", "take=1001", "offset=-1", "offset=2147483648", "take=1&take=2", "account_id=" + syncAccount, "take=%ZZ"} {
		w = syncHTTP(h, "GET", "/api/chat/"+syncRoom+"/messages?"+q, "")
		if w.Code != 400 {
			t.Fatal(q, w.Code)
		}
	}
	if w = syncHTTP(h, "GET", "/api/chat/"+syncRoom+"/messages/"+syncMessage+"?offset=0", ""); w.Code != 400 {
		t.Fatal(w)
	}
}
func TestSyncHTTPErrorsAndInvalidJSONFailWithoutTotal(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{{ErrSyncUnauthorized, 401}, {ErrSyncForbidden, 403}, {ErrSyncRoomNotFound, 404}, {ErrSyncMessageNotFound, 404}, {ErrSyncInvalidRequest, 400}, {errors.New("password=never-echo-db-details"), 503}, {ErrSyncPolicyUnverified, 503}} {
		f := &syncFake{err: tc.err}
		w := syncHTTP(syntheticSync(t, f, syncAccount), "POST", "/api/chat/"+syncRoom+"/sync", `{"last_sync_timestamp":0}`)
		if w.Code != tc.status || w.Header().Get("X-Total") != "" || strings.Contains(w.Body.String(), "password") {
			t.Fatal(w)
		}
	}
	f := &syncFake{response: SyncResponse{Messages: []MessageProjection{{Meta: json.RawMessage(`broken`)}}}}
	w := syncHTTP(syntheticSync(t, f, syncAccount), "POST", "/api/chat/"+syncRoom+"/sync", `{"last_sync_timestamp":0}`)
	if w.Code != 503 || w.Header().Get("X-Total") != "" {
		t.Fatal(w)
	}
}
func TestSyncQueryResourceAndDateBounds(t *testing.T) {
	q := SyncQuery{RoomID: syncRoom, AccountID: syncAccount}
	for _, timestamp := range []int64{-62135596800001, 253402300800000} {
		q.Request.LastSyncTimestamp = timestamp
		if !errors.Is(ValidateSyncQuery(q), ErrSyncInvalidRequest) {
			t.Fatal(timestamp)
		}
	}
	q.Request = SyncRequest{MissingSequences: make([]int64, MaxRecoverySelectors+1)}
	if ValidateSyncQuery(q) == nil {
		t.Fatal("unbounded selectors")
	}
	q.Request = SyncRequest{MissingSequenceRanges: make([]SequenceRange, MaxRecoverySelectors+1)}
	if ValidateSyncQuery(q) == nil {
		t.Fatal("unbounded ranges")
	}
	if _, err := NewSyntheticProjectionSyncHandler(nil, "invalid"); err == nil {
		t.Fatal("invalid fixture identity")
	}
}

type unreadFake struct {
	syncFake
	counts map[string]UnreadSummary
}

func (f *unreadFake) Unread(_ context.Context, account string) (map[string]UnreadSummary, error) {
	f.calls++
	return f.counts, f.err
}
func TestUnreadHTTPRequiresIdentityAndReturnsInteger(t *testing.T) {
	f := &unreadFake{counts: map[string]UnreadSummary{syncRoom: {UnreadCount: 5, HasUnread: true}, "none": {HasUnread: true}}}
	h, _ := NewSyntheticProjectionSyncHandler(NewSyntheticProjectionSyncService(f), syncAccount)
	w := syncHTTP(h, "GET", "/api/chat/unread", "")
	if w.Code != 200 || w.Body.String() != "5\n" || w.Header().Get("X-Total") != "" {
		t.Fatal(w)
	}
	for _, target := range []string{"/api/chat/unread?account=" + syncAccount, "/api/chat/unread?bad=%ZZ"} {
		if w = syncHTTP(h, "GET", target, ""); w.Code != 400 {
			t.Fatal(w)
		}
	}
	anonymous, _ := NewSyntheticProjectionSyncHandler(NewSyntheticProjectionSyncService(f), "")
	if w = syncHTTP(anonymous, "GET", "/api/chat/unread", ""); w.Code != 401 {
		t.Fatal(w)
	}
	f.counts = map[string]UnreadSummary{"one": {UnreadCount: math.MaxInt32}, "two": {UnreadCount: 1}}
	if w = syncHTTP(h, "GET", "/api/chat/unread", ""); w.Code != 503 {
		t.Fatal(w)
	}
}
