// SPDX-License-Identifier: AGPL-3.0-only
package federationhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"solsynth.local/go-migration/ports/authority"
	f "solsynth.local/go-migration/ports/federation"
)

type broaderHTTPRepo struct {
	fixtureRepo
	changed f.Change
}

func (r *broaderHTTPRepo) RemoteProfile(context.Context, string) (f.RemoteProfile, error) {
	return f.RemoteProfile{ID: "00000000-0000-4000-8000-000000000002", Actor: "https://remote.example.test/users/alice", Handle: "alice@remote.example.test", Provenance: "unsigned_document_matches_configured_pin"}, nil
}
func (r *broaderHTTPRepo) RemoteProfileByHandle(ctx context.Context, _ string) (f.RemoteProfile, error) {
	return r.RemoteProfile(ctx, "")
}
func (r *broaderHTTPRepo) SearchRemoteProfiles(context.Context, string, int, int) ([]f.RemoteProfile, error) {
	return []f.RemoteProfile{{ID: "00000000-0000-4000-8000-000000000002", Actor: "https://remote.example.test/users/alice"}}, nil
}
func (r *broaderHTTPRepo) RemotePosts(context.Context, string, string, string, int, int) ([]f.Note, error) {
	return []f.Note{{ID: "https://remote.example.test/posts/1", Type: "Note"}}, nil
}
func (r *broaderHTTPRepo) Availability(context.Context, string) (f.Availability, error) {
	return f.Availability{Enabled: true, Publishers: []string{"00000000-0000-4000-8000-000000000003"}}, nil
}
func (r *broaderHTTPRepo) RelationshipView(context.Context, string, string, string) (f.RelationshipView, error) {
	return f.RelationshipView{Following: true}, nil
}
func (r *broaderHTTPRepo) Change(_ context.Context, _ string, _ string, c f.Change) error {
	r.changed = c
	return nil
}

type broaderAuthority struct{}

func (broaderAuthority) AuthenticateRequest(*http.Request) (authority.Principal, error) {
	return authority.Principal{Account: authority.Account{ID: "00000000-0000-4000-8000-000000000001"}}, nil
}
func (broaderAuthority) AuthorizePrincipal(_ context.Context, p authority.Principal, permissions ...string) (authority.Principal, error) {
	if len(permissions) != 1 || permissions[0] != f.ReadPermission {
		return p, authority.ErrForbidden
	}
	return p, nil
}

func TestFediverseClientRoutesAreAuthenticatedAndConsumeRepository(t *testing.T) {
	h, _ := testHandler()
	repo := &broaderHTTPRepo{}
	h.Repository = repo
	w := call(h, "GET", "/api/fediverse/actors/search?q=alice")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatal("unauthenticated route did not fail closed", w.Code)
	}
	h.Authority = broaderAuthority{}
	for _, path := range []string{"/api/fediverse/actors/search?q=alice", "/api/fediverse/actors/availability", "/api/fediverse/actors/00000000-0000-4000-8000-000000000002/posts?publisher_id=00000000-0000-4000-8000-000000000003"} {
		w = call(h, "GET", path)
		if w.Code != http.StatusOK {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest("POST", "https://local.example.test/api/fediverse/actors/00000000-0000-4000-8000-000000000002/follow", strings.NewReader(`{"publisher_id":"00000000-0000-4000-8000-000000000003"}`))
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent || repo.changed.Action != "follow" || repo.changed.RemoteActor != "https://remote.example.test/users/alice" {
		t.Fatal(w.Code, w.Body.String(), repo.changed)
	}
}

func TestFederationRouteClassificationIncludesBroaderAPIs(t *testing.T) {
	for _, path := range []string{"/api/fediverse/actors/search", "/api/fediverse/moderation/rules", "/api/admin/fediverse/keys/stats", "/api/activitypub/realms/00000000-0000-4000-8000-000000000001/settings"} {
		if !IsPrivateRoute(path) {
			t.Fatal(path)
		}
	}
	if !IsPublicRoute("/activitypub/realms/community/outbox") {
		t.Fatal("realm ActivityPub route is not public")
	}
}
