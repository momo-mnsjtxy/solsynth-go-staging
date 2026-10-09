// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"solsynth.local/go-migration/ports/authority"
)

type organizationFake struct {
	OrganizationRepository
	calls       int
	actor, room string
	search      SearchRequest
	expired     bool
}

func (f *organizationFake) hit(actor string) { f.calls++; f.actor = actor }
func (f *organizationFake) Summary(_ context.Context, a string) (map[string]ChatSummary, error) {
	f.hit(a)
	return map[string]ChatSummary{testRoom: {UnreadCount: 2, HasUnread: true}}, nil
}
func (f *organizationFake) ReadAll(_ context.Context, a string) error { f.hit(a); return nil }
func (f *organizationFake) Search(_ context.Context, a string, q SearchRequest) (SearchPage, error) {
	f.hit(a)
	f.search = q
	return SearchPage{Rooms: []SearchRoom{}, Total: 7}, nil
}
func (f *organizationFake) Pins(_ context.Context, a, r string, b bool) ([]Pin, error) {
	f.hit(a)
	f.room = r
	f.expired = b
	return []Pin{{ID: testRoom, MessageID: testActor}}, nil
}
func (f *organizationFake) Pin(_ context.Context, a, r string, q PinRequest) (Pin, error) {
	f.hit(a)
	return Pin{ID: testRoom, MessageID: q.MessageID}, nil
}
func (f *organizationFake) Unpin(_ context.Context, a, r, p string) error { f.hit(a); return nil }
func (f *organizationFake) Groups(_ context.Context, a string) ([]ChatGroup, error) {
	f.hit(a)
	return []ChatGroup{}, nil
}
func (f *organizationFake) CreateGroup(_ context.Context, a string, q GroupPatch) (ChatGroup, error) {
	f.hit(a)
	return ChatGroup{ID: testRoom, AccountID: a, Name: *q.Name, Color: q.Color, Icon: q.Icon}, nil
}
func (f *organizationFake) UpdateGroup(_ context.Context, a, g string, q GroupPatch) (ChatGroup, error) {
	f.hit(a)
	return ChatGroup{ID: g, AccountID: a}, nil
}
func (f *organizationFake) DeleteGroup(_ context.Context, a, g string) error { f.hit(a); return nil }
func (f *organizationFake) MoveToGroup(_ context.Context, a, r string, g *string) error {
	f.hit(a)
	return nil
}
func TestOrganizationHTTPRoutesShapesAndPermissions(t *testing.T) {
	for _, c := range []struct{ method, path, body, permission string }{
		{"GET", "/summary", "", ""}, {"POST", "/read-all", "", "chat.read.all"}, {"GET", "/messages/search?query=needle", "", ""},
		{"GET", "/groups", "", ""}, {"POST", "/groups", `{"name":"Work","color":"blue","icon":"work","order":2}`, "chat.groups.manage"},
		{"PATCH", "/groups/" + testRoom, `{"name":"Renamed"}`, ""}, {"DELETE", "/groups/" + testRoom, "", ""},
		{"PATCH", "/rooms/" + testRoom + "/group", `{"group_id":null}`, ""},
		{"GET", "/" + testRoom + "/pins?includeExpired=true", "", ""}, {"POST", "/" + testRoom + "/pins", `{"message_id":"` + testActor + `"}`, "chat.pins.manage"},
		{"DELETE", "/" + testRoom + "/pins/" + testActor, "", "chat.pins.manage"},
	} {
		t.Run(c.method+c.path, func(t *testing.T) {
			a := &fakeAuthority{}
			repo := &organizationFake{}
			h := (&Service{Organization: repo, Authority: a}).Handler()
			w := call(h, c.method, "/api/chat"+c.path, c.body, "Bearer fixture")
			if w.Code != 200 || repo.calls != 1 || repo.actor != testActor || a.permission != c.permission || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(w, repo, a)
			}
			if strings.Contains(c.path, "search") && w.Header().Get("X-Total") != "7" {
				t.Fatal(w.Header())
			}
		})
	}
	repo := &organizationFake{}
	h := (&Service{Organization: repo, Authority: &fakeAuthority{}}).Handler()
	w := call(h, "GET", "/api/chat/summary", "", "Bearer fixture")
	var result map[string]map[string]json.RawMessage
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || string(result[testRoom]["unread_count"]) != "2" || string(result[testRoom]["last_message"]) != "null" {
		t.Fatal(w.Body.String())
	}
}
func TestOrganizationHTTPIdentityAndStrictInputs(t *testing.T) {
	repo := &organizationFake{}
	a := &fakeAuthority{}
	h := (&Service{Organization: repo, Authority: a}).Handler()
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/messages/search?query=", ""}, {"GET", "/messages/search?query=x&take=0", ""}, {"GET", "/messages/search?query=x&query=y", ""}, {"GET", "/messages/search?query=x&after=bad", ""}, {"GET", "/messages/search?query=x&room_ids=not-a-uuid", ""}, {"GET", "/messages/search?query=x&unexpected=1", ""}, {"GET", "/messages/search?query=x%zz", ""},
		{"POST", "/read-all?take=1", ""}, {"POST", "/read-all", `{"account_id":"x"}`}, {"POST", "/groups", `{"name":"one","name":"two"}`}, {"POST", "/groups", `{"name":null}`}, {"POST", "/groups", `{"name":"  "}`}, {"PATCH", "/groups/" + testRoom, `{"account_id":"x"}`}, {"PATCH", "/rooms/" + testRoom + "/group", `{"group_id":"bad"}`}, {"GET", "/" + testRoom + "/pins?includeExpired=yes", ""}, {"GET", "/" + testRoom + "/pins?include_expired=true", ""},
	} {
		w := call(h, c.method, "/api/chat"+c.path, c.body, "Bearer fixture")
		if w.Code != 400 || repo.calls != 0 {
			t.Fatal(c, w, repo)
		}
	}
	for _, path := range []string{"/summary", "/groups", "/messages/search?query=x"} {
		if w := call(h, "GET", "/api/chat"+path, "", ""); w.Code != 401 || repo.calls != 0 {
			t.Fatal(w, repo)
		}
	}
	w := call(h, "GET", "/api/chat/"+testRoom+"/pins", "", "")
	if w.Code != 200 || repo.actor != "" || repo.calls != 1 {
		t.Fatal(w, repo)
	}
	h = (&Service{Organization: repo, Authority: &boundAuthority{fakeAuthority: fakeAuthority{denied: authority.ErrInvalid}}}).Handler()
	w = call(h, "GET", "/api/chat/"+testRoom+"/pins", "", "")
	if w.Code != 401 || repo.calls != 1 {
		t.Fatal("stripped revoked proof downgraded", w, repo)
	}
}
func TestOrganizationSearchSelectorsAndGroupBounds(t *testing.T) {
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, e := ValidateSearch(SearchRequest{Query: "x", Take: 20, After: &after, Before: &after}); e != ErrSearchDates {
		t.Fatal(e)
	}
	q, e := ValidateSearch(SearchRequest{Query: "  100%_\\literal  ", Take: 20, RoomIDs: []string{testRoom, testRoom}})
	if e != nil || q.Query != "100%_\\literal" || len(q.RoomIDs) != 1 {
		t.Fatal(q, e)
	}
	tooLong := strings.Repeat("界", 257)
	if ValidateGroup(GroupPatch{Name: &tooLong}, true) == nil {
		t.Fatal("name bound")
	}
	repo := &organizationFake{}
	h := (&Service{Organization: repo, Authority: &fakeAuthority{}}).Handler()
	values := url.Values{"query": {" literal%_ "}, "room_ids": {testRoom, testActor}, "take": {"9"}, "offset": {"2"}, "sender": {"named"}, "after": {after.Format(time.RFC3339)}}
	w := call(h, "GET", "/api/chat/messages/search?"+values.Encode(), "", "Bearer fixture")
	if w.Code != 200 || repo.search.Query != "literal%_" || repo.search.Take != 9 || repo.search.Offset != 2 || len(repo.search.RoomIDs) != 2 || repo.search.After == nil {
		t.Fatal(w, repo.search)
	}
	for _, method := range []string{"HEAD", "PUT", "PATCH"} {
		before := repo.calls
		w = call(h, method, "/api/chat/summary", "", "Bearer fixture")
		if w.Code != 405 || w.Header().Get("Allow") != "GET" || repo.calls != before {
			t.Fatal(w)
		}
	}
}

func TestPinAuthoredMillisecondExpiryAndTimestampValidation(t *testing.T) {
	for _, value := range []string{`0`, `"1970-01-01T00:00:00Z"`} {
		var q PinRequest
		if e := json.Unmarshal([]byte(`{"message_id":"`+testActor+`","expires_at":`+value+`}`), &q); e != nil || q.ExpiresAt == nil || q.ExpiresAt.UnixMilli() != 0 {
			t.Fatal(value, q, e)
		}
	}
	for _, value := range []string{`1.5`, `1e3`, `253402300800000`, `-62135596800001`, `true`, `"not-a-time"`} {
		var q PinRequest
		if e := json.Unmarshal([]byte(`{"expires_at":`+value+`}`), &q); e == nil {
			t.Fatal(value)
		}
	}
	long := strings.Repeat("😀", 129)
	if ValidateGroup(GroupPatch{Name: &long}, true) == nil {
		t.Fatal("UTF16 bound lost")
	}
	if _, e := ValidateSearch(SearchRequest{Query: long, Take: 20}); e == nil {
		t.Fatal("search UTF16 bound lost")
	}
}
