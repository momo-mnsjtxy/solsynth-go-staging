// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"testing"
	"time"
)

const actorID = "12300000-0000-4000-8000-000000000001"

type testRepo struct {
	Repository
	lookups, writes int
	actor           string
	patch           ProfilePatch
}

func (r *testRepo) LookupAccount(_ context.Context, id string) (Account, error) {
	r.lookups++
	r.actor = id
	return Account{ID: actorID, Name: "alice", Nick: "Alice", CreatedAt: time.Unix(1, 0)}, nil
}
func (r *testRepo) UpdateProfile(_ context.Context, id string, p ProfilePatch) (Account, error) {
	r.writes++
	r.actor = id
	r.patch = p
	return Account{ID: id, Name: "alice", Nick: *p.Nick}, nil
}

type testAuth struct {
	permissions []string
	denied      bool
	calls       int
	freshChecks int
}

func (a *testAuth) AuthenticateRequest(r *http.Request) (authority.Principal, error) {
	a.calls++
	if r.Header.Get("Authorization") != "Bearer test" {
		return authority.Principal{}, authority.ErrInvalid
	}
	return authority.Principal{Account: authority.Account{ID: actorID, Superuser: true}, Session: authority.Session{ID: "session"}}, nil
}
func (a *testAuth) AuthenticatePrincipal(_ context.Context, p authority.Principal) (authority.Principal, error) {
	a.freshChecks++
	if a.denied {
		return authority.Principal{}, authority.ErrForbidden
	}
	return p, nil
}
func (a *testAuth) AuthorizePrincipal(_ context.Context, p authority.Principal, perms ...string) (authority.Principal, error) {
	if len(perms) == 0 {
		return authority.Principal{}, authority.ErrForbidden
	}
	a.permissions = append(a.permissions, perms...)
	if a.denied {
		return authority.Principal{}, authority.ErrForbidden
	}
	return p, nil
}
func request(s *Service, method, path, body string, auth bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if auth {
		r.Header.Set("Authorization", "Bearer test")
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func TestPublicSafeProjection(t *testing.T) {
	repo := &testRepo{}
	s := &Service{Repository: repo, Authority: &testAuth{}}
	for _, path := range []string{"/api/accounts/alice", "/stargate/accounts/alice", "/api/accounts/me", "/stargate/accounts/me"} {
		w := request(s, "GET", path, "", strings.HasSuffix(path, "/me"))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		for _, forbidden := range []string{"is_superuser", "contacts", "credentials", "grants", "punishments", "birthday", "profile"} {
			if strings.Contains(w.Body.String(), forbidden) {
				t.Fatalf("unavailable/private field %s: %s", forbidden, w.Body)
			}
		}
	}
}
func TestSelfUpdateIsStrictAndBoundToActor(t *testing.T) {
	repo := &testRepo{}
	auth := &testAuth{}
	s := &Service{Repository: repo, Authority: auth}
	w := request(s, "PATCH", "/api/accounts/me", `{"nick":"Updated","bio":"Hello"}`, true)
	if w.Code != 200 || repo.writes != 1 || repo.actor != actorID {
		t.Fatalf("%d %+v", w.Code, repo)
	}
	if !strings.Contains(strings.Join(auth.permissions, ","), "accounts.manage") {
		t.Fatal("missing permission")
	}
	for _, body := range []string{`null`, `[]`, `{"nick":"x","is_superuser":true}`, `{"nick":"x","account_id":"other"}`, `{"nick":"x","grants":["*"]}`, `{"nick":"x"} {}`, `{"name":"free-name","nick":"x"}`, `{"picture_id":"invented","nick":"x"}`, `{"nick":"` + strings.Repeat("x", 70000) + `"}`} {
		before := repo.writes
		w = request(s, "PATCH", "/api/accounts/me", body, true)
		if w.Code < 400 || repo.writes != before {
			t.Fatalf("body accepted %d", w.Code)
		}
	}
}

type publisherRepo struct {
	Repository
	actor, reference string
	changes          []MemberChange
	reads, writes    int
}

func (p *publisherRepo) ListPublishers(_ context.Context, actor string) ([]Publisher, error) {
	p.actor = actor
	p.reads++
	return []Publisher{}, nil
}
func (p *publisherRepo) ListInvites(_ context.Context, actor string) ([]Member, error) {
	p.actor = actor
	p.reads++
	return []Member{}, nil
}
func (p *publisherRepo) LookupPublisher(_ context.Context, ref string) (Publisher, error) {
	p.reference = ref
	p.reads++
	return Publisher{ID: "12300000-0000-4000-8000-000000000002", Name: "publication"}, nil
}
func (p *publisherRepo) Membership(_ context.Context, pub, actor string) (Member, error) {
	p.reference, p.actor = pub, actor
	p.reads++
	return Member{PublisherID: pub, AccountID: actor}, nil
}
func (p *publisherRepo) ListMembers(_ context.Context, ref string, offset, take int) ([]Member, int64, error) {
	p.reference = ref
	p.reads++
	return []Member{{AccountID: actorID}}, 23, nil
}
func (p *publisherRepo) ChangeMember(_ context.Context, actor, ref string, change MemberChange) (Member, error) {
	p.actor, p.reference = actor, ref
	p.changes = append(p.changes, change)
	p.writes++
	return Member{PublisherID: "12300000-0000-4000-8000-000000000002", AccountID: change.AccountID}, nil
}
func (p *publisherRepo) CreatePublisher(_ context.Context, actor string, patch PublisherPatch) (Publisher, error) {
	p.actor = actor
	p.writes++
	return Publisher{ID: "12300000-0000-4000-8000-000000000002", Name: *patch.Name}, nil
}
func (p *publisherRepo) UpdatePublisher(_ context.Context, actor, ref string, patch PublisherPatch) (Publisher, error) {
	p.actor, p.reference = actor, ref
	p.writes++
	return Publisher{ID: "12300000-0000-4000-8000-000000000002", Name: ref}, nil
}
func (p *publisherRepo) DeletePublisher(_ context.Context, actor, ref string) error {
	p.actor, p.reference = actor, ref
	p.writes++
	return nil
}
func TestPublisherAuthOnlyReadsAndSelfLeave(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/publishers"}, {"GET", "/api/publishers/invites"},
		{"GET", "/api/publishers/publication/members/me"}, {"DELETE", "/api/publishers/publication/members/me"},
	} {
		r, a := &publisherRepo{}, &testAuth{}
		w := request(&Service{Repository: r, Authority: a}, tc.method, tc.path, "", true)
		if w.Code != 200 && w.Code != 204 {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body)
		}
		if len(a.permissions) != 0 || a.freshChecks == 0 || r.actor != actorID {
			t.Fatalf("auth-only contract: %+v %+v", a, r)
		}
	}
}
func TestPublisherMutationPermissionRouting(t *testing.T) {
	for _, tc := range []struct{ method, path, body, permission string }{
		{"POST", "/api/publishers/individual", `{"name":"publication","nick":"Publication"}`, "publishers.create"},
		{"PATCH", "/api/publishers/publication", `{"nick":"Updated"}`, "publishers.update"},
		{"DELETE", "/api/publishers/publication", "", "publishers.delete"},
		{"POST", "/api/publishers/invites/publication", `{"related_user_id":"12300000-0000-4000-8000-000000000003","role":50}`, "publishers.invites.manage"},
		{"POST", "/api/publishers/invites/publication/accept", "", "publishers.invites.manage"},
		{"POST", "/api/publishers/invites/publication/decline", "", "publishers.invites.manage"},
		{"DELETE", "/api/publishers/publication/members/12300000-0000-4000-8000-000000000003", "", "publishers.members.manage"},
		{"PATCH", "/api/publishers/publication/members/12300000-0000-4000-8000-000000000003/role", "50", "publishers.members.manage"},
	} {
		r, a := &publisherRepo{}, &testAuth{}
		s := &Service{Repository: r, Authority: a}
		w := request(s, tc.method, tc.path, tc.body, true)
		if (w.Code != 200 && w.Code != 204) || r.writes != 1 || r.actor != actorID || len(a.permissions) != 1 || a.permissions[0] != tc.permission {
			t.Fatalf("%s %s: %d %+v %+v", tc.method, tc.path, w.Code, r, a)
		}
		a.denied = true
		w = request(s, tc.method, tc.path, tc.body, true)
		if w.Code != 403 || r.writes != 1 {
			t.Fatalf("denied mutation reached repository: %d %+v", w.Code, r)
		}
	}
}
func TestPublisherPaginationAndNullRole(t *testing.T) {
	r := &publisherRepo{}
	s := &Service{Repository: r, Authority: &testAuth{}}
	w := request(s, "GET", "/api/publishers/publication/members?offset=5&take=1", "", false)
	if w.Code != 200 || w.Header().Get("X-Total") != "23" {
		t.Fatal(w.Code, w.Header(), w.Body)
	}
	for _, q := range []string{"offset=-1", "take=0", "take=101", "offset=no", "take=no"} {
		w = request(s, "GET", "/api/publishers/publication/members?"+q, "", false)
		if w.Code != 400 || r.reads != 1 {
			t.Fatal("invalid pagination reached repository", q, w.Code, r.reads)
		}
	}
	w = request(s, "PATCH", "/api/publishers/publication/members/"+actorID+"/role", "null", true)
	if w.Code != 400 || r.writes != 0 {
		t.Fatal("null role treated as viewer", w.Code)
	}
}
func TestInvalidSuppliedAuthorizationNeverDowngrades(t *testing.T) {
	for _, key := range []string{"Authorization", "authorization", "AUTHORIZATION"} {
		r := &testRepo{}
		s := &Service{Repository: r, Authority: &testAuth{}}
		req := httptest.NewRequest("GET", "/api/accounts/alice", nil)
		req.Header[key] = []string{"invalid"}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != 401 || r.lookups != 0 {
			t.Fatalf("%s: invalid credentials downgraded: %d", key, w.Code)
		}
	}
}
func TestFailClosedAuthAndHeaderInjection(t *testing.T) {
	repo := &testRepo{}
	s := &Service{Repository: repo, Authority: &testAuth{}}
	w := request(s, "PATCH", "/api/accounts/me", `{"nick":"x"}`, false)
	if w.Code != 401 || repo.writes != 0 {
		t.Fatal("unauthenticated write")
	}
	for _, header := range []string{"X-Account-Id", "X-User-Id", "X-Role", "X-Publisher-Role", "X-Synthetic-Account", "X-Test-Role", "Proxy-Authorization"} {
		r := httptest.NewRequest("GET", "/api/accounts/alice", nil)
		r.Header.Set(header, "owner")
		w = httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("%s: %d", header, w.Code)
		}
	}
	w = request(s, "GET", "/api/accounts/alice?access_token=test", "", false)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	s.Authority = &testAuth{denied: true}
	w = request(s, "PATCH", "/api/accounts/me", `{"nick":"x"}`, true)
	if w.Code != 403 || repo.writes != 0 {
		t.Fatal("denied authority ignored")
	}
}
func TestUnsupportedDependenciesCannotSucceed(t *testing.T) {
	s := &Service{Repository: &testRepo{}, Authority: &testAuth{}}
	for _, tc := range []struct{ method, path, body string }{{"POST", "/api/accounts/me/name-change-card/use", `{"new_name":"free"}`}, {"POST", "/api/publishers/organization/realm", `{"name":"org","nick":"Org"}`}, {"GET", "/api/publishers/x/stats", ""}, {"GET", "/api/publishers/x/rating", ""}, {"POST", "/api/publishers/individual", `{"name":"test","nick":"Test","picture_id":"fake"}`}} {
		w := request(s, tc.method, tc.path, tc.body, true)
		if w.Code != 501 {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body)
		}
	}
}
func TestHEADNeverReadsOrMutates(t *testing.T) {
	r := &testRepo{}
	s := &Service{Repository: r}
	w := request(s, "HEAD", "/api/accounts/me", "", false)
	if w.Code != 405 || w.Body.Len() != 0 || r.lookups+r.writes != 0 {
		t.Fatal("HEAD invoked domain")
	}
}
func TestValidation(t *testing.T) {
	str := func(s string) *string { return &s }
	for _, p := range []ProfilePatch{{Name: str("rename")}, {Nick: str("")}, {Bio: str(strings.Repeat("x", 4097))}, {TimeZone: str("bogus/zone")}, {Links: &[]Link{{Name: "x", URL: "javascript:alert(1)"}}}, {Links: &[]Link{{URL: "https://user:pass@example.test/"}}}} {
		if ValidateProfile(p) == nil {
			t.Fatal("bad profile accepted")
		}
	}
	if ValidateProfile(ProfilePatch{Bio: str("雪"), Links: &[]Link{{Name: "Site", URL: "https://example.test"}}}) != nil {
		t.Fatal("valid profile rejected")
	}
	for _, name := range []string{"bad/slug", ".edge", "invites", actorID} {
		if ValidatePublisher(PublisherPatch{Name: &name, Nick: str("N")}, true) == nil {
			t.Fatal(name)
		}
	}
	for _, role := range []Role{-1, 1, 51, 101, 201, 9999} {
		if role.Valid() {
			t.Fatal(role)
		}
	}
	var a Account
	if e := json.Unmarshal([]byte(`{"id":"x"}`), &a); e != nil {
		t.Fatal(e)
	}
	if !errors.Is(NormalizeError(errors.New("private database detail")), ErrUnavailable) {
		t.Fatal("error leak")
	}
}
