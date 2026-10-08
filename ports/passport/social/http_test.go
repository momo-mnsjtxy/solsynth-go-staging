// SPDX-License-Identifier: AGPL-3.0-only
package social

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"testing"
)

const testActor = "c0510000-0000-4000-8000-000000000001"
const testTarget = "c0510000-0000-4000-8000-000000000002"

type fakeAuthority struct {
	deny    string
	keys    []string
	trusted bool
}

func (a *fakeAuthority) AuthenticateRequest(r *http.Request) (authority.Principal, error) {
	if !a.trusted && r.Header.Get("Authorization") != "Bearer fixture" {
		return authority.Principal{}, authority.ErrInvalid
	}
	return authority.Principal{Account: authority.Account{ID: testActor}}, nil
}
func (a *fakeAuthority) AuthenticatePrincipal(_ context.Context, p authority.Principal) (authority.Principal, error) {
	return p, nil
}
func (a *fakeAuthority) AuthorizePrincipal(_ context.Context, p authority.Principal, keys ...string) (authority.Principal, error) {
	a.keys = append([]string{}, keys...)
	for _, k := range keys {
		if k == a.deny {
			return p, authority.ErrForbidden
		}
	}
	return p, nil
}

type repositorySpy struct {
	Repository
	calls         int
	actor, target string
	change        RelationshipChange
}

func (r *repositorySpy) ChangeRelationship(_ context.Context, a, b string, c RelationshipChange) (Relationship, error) {
	r.calls++
	r.actor = a
	r.target = b
	r.change = c
	return Relationship{ID: testTarget, AccountID: a, RelatedID: b, Status: Friend}, nil
}
func (r *repositorySpy) Relationships(_ context.Context, actor string, requests bool, off, take int) ([]Relationship, int64, error) {
	r.calls++
	r.actor = actor
	return []Relationship{}, 0, nil
}
func (r *repositorySpy) ChangeMember(_ context.Context, actor, ref string, c MemberChange) (Member, error) {
	r.calls++
	if !c.Role.Valid() {
		return Member{}, ErrInvalid
	}
	return Member{RealmID: testTarget, AccountID: actor, Role: c.Role}, nil
}
func TestClientRelationshipRoutesAndPermissions(t *testing.T) {
	for _, tc := range []struct {
		method, path, body, key string
		action                  RelationshipAction
		status                  int
	}{{"POST", "/friends", "", "relationships.friends.manage", RequestFriend, 200}, {"POST", "/friends/accept", "", "relationships.friends.manage", AcceptFriend, 200}, {"POST", "/friends/decline", "", "relationships.friends.manage", DeclineFriend, 204}, {"DELETE", "/friends", "", "relationships.friends.manage", CancelFriend, 204}, {"POST", "/block", "null", "relationships.block.manage", Block, 200}, {"POST", "/close-friend", "", "relationships.close.friends.manage", SetClose, 200}, {"DELETE", "/close-friend", "", "relationships.close.friends.manage", UnsetClose, 200}, {"PATCH", "/alias", `{"alias":"Friend"}`, "relationships.alias.manage", SetAlias, 200}, {"DELETE", "", "", "relationships.delete", Forget, 204}} {
		t.Run(string(tc.action), func(t *testing.T) {
			repo := &repositorySpy{}
			auth := &fakeAuthority{}
			req := httptest.NewRequest(tc.method, "/stargate/relationships/"+testTarget+tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer fixture")
			rec := httptest.NewRecorder()
			(&Service{Repository: repo, Authority: auth}).Handler().ServeHTTP(rec, req)
			if rec.Code != tc.status || repo.calls != 1 || repo.actor != testActor || repo.target != testTarget || repo.change.Action != tc.action || !reflect.DeepEqual(auth.keys, []string{tc.key}) {
				t.Fatal(rec.Code, rec.Body.String(), repo, auth.keys)
			}
		})
	}
}
func TestPATCHCannotBypassSpecificPermissions(t *testing.T) {
	for _, tc := range []struct{ status, denied string }{{"-100", "relationships.block.manage"}, {"200", "relationships.close.friends.manage"}, {"100", "relationships.friends.manage"}, {"100", "relationships.close.friends.manage"}} {
		repo := &repositorySpy{}
		auth := &fakeAuthority{deny: tc.denied}
		req := httptest.NewRequest("PATCH", "/stargate/relationships/"+testTarget, strings.NewReader(`{"status":`+tc.status+`}`))
		req.Header.Set("Authorization", "Bearer fixture")
		rec := httptest.NewRecorder()
		(&Service{Repository: repo, Authority: auth}).Handler().ServeHTTP(rec, req)
		if rec.Code != 403 || repo.calls != 0 {
			t.Fatal("permission bypass", tc, rec.Code, repo.calls)
		}
	}
}
func TestSocialRejectsAmbiguousAndUnsupportedInput(t *testing.T) {
	for _, tc := range []struct {
		method, path, body, header string
		status                     int
	}{{"POST", "/stargate/relationships/" + testTarget + "/block", `{"expires_in":"1h"}`, "", 501}, {"POST", "/stargate/relationships/" + testTarget + "/block", `{"degrade_to":100}`, "", 501}, {"POST", "/stargate/relationships/" + testTarget + "/block", `{"other":1}`, "", 400}, {"PATCH", "/stargate/relationships/" + testTarget, `{}`, "", 400}, {"PATCH", "/stargate/relationships/" + testTarget, `null`, "", 400}, {"PATCH", "/stargate/relationships/" + testTarget, `{"status":100} {}`, "", 400}, {"POST", "/stargate/relationships/" + testTarget + "/mute", "", "", 501}, {"GET", "/stargate/relationships?offset=-1", "", "", 400}, {"GET", "/stargate/relationships?take=101", "", "", 400}, {"GET", "/stargate/relationships?token=secret", "", "", 400}, {"GET", "/stargate/relationships", "", "X-Account-ID", 403}, {"PATCH", "/api/realms/test/members/" + testTarget + "/role", "null", "", 400}} {
		t.Run(tc.path+tc.body, func(t *testing.T) {
			repo := &repositorySpy{}
			auth := &fakeAuthority{}
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer fixture")
			if tc.header != "" {
				req.Header.Set(tc.header, testActor)
			}
			rec := httptest.NewRecorder()
			(&Service{Repository: repo, Authority: auth}).Handler().ServeHTTP(rec, req)
			if rec.Code != tc.status || repo.calls != 0 {
				t.Fatal(rec.Code, rec.Body.String(), repo.calls)
			}
		})
	}
}
func TestRealmValidationAndRoleBoundaries(t *testing.T) {
	for _, name := range []string{"invites", "quota", "search", "me", testActor, "../realm", "with/slash"} {
		v := name
		n := "Name"
		if e := ValidateRealm(RealmPatch{Slug: &v, Name: &n}, true); e == nil {
			t.Fatal("reserved/unsafe slug", name)
		}
	}
	slug, name := "synthetic-realm", "Name"
	if e := ValidateRealm(RealmPatch{Slug: &slug, Name: &name}, true); e != nil {
		t.Fatal(e)
	}
	for _, role := range []Role{-1, 101, 200} {
		if role.Valid() {
			t.Fatal("unsupported role", role)
		}
	}
}

func (r *repositorySpy) GetRealm(_ context.Context, actor, ref string) (Realm, error) {
	r.calls++
	r.actor = actor
	return Realm{ID: testTarget, AccountID: actor}, nil
}
func TestHostBoundProofSurvivesRemovedBearerHeader(t *testing.T) {
	repo := &repositorySpy{}
	auth := &fakeAuthority{trusted: true}
	req := httptest.NewRequest("GET", "/api/realms/private", nil)
	rec := httptest.NewRecorder()
	(&Service{Repository: repo, Authority: auth}).Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || repo.actor != testActor {
		t.Fatal(rec.Code, repo.actor)
	}
}
