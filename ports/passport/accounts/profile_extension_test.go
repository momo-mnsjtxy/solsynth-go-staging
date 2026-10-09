// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"context"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	"testing"
)

type authoredProfileAuth struct{ testAuth }

func (a *authoredProfileAuth) AuthenticateRequest(r *http.Request) (authority.Principal, error) {
	p, e := a.testAuth.AuthenticateRequest(r)
	p.Account.Name = "alice"
	return p, e
}

type authoredProfileRepo struct{ testRepo }

func (r *authoredProfileRepo) UpdateProfile(_ context.Context, id string, p ProfilePatch) (Account, error) {
	r.writes++
	r.actor = id
	r.patch = p
	return Account{ID: id, Name: "alice"}, nil
}
func TestAuthoredProfileAliasesNeutralStyleAndBirthdayClear(t *testing.T) {
	repo := &authoredProfileRepo{}
	s := &Service{Repository: repo, Authority: &authoredProfileAuth{}}
	w := request(s, "PATCH", "/stargate/accounts/me", `{"name":"alice","nick":"Alice"}`, true)
	if w.Code != 200 || repo.patch.Name != nil {
		t.Fatal("unchanged name required a paid change", w.Code)
	}
	w = request(s, "PATCH", "/stargate/accounts/me/profile", `{"birthday":null,"first_name":"Owned fixture","username_color":{"type":"plain"}}`, true)
	if w.Code != 200 || !repo.patch.BirthdayPresent || repo.patch.Birthday != nil || repo.patch.FirstName == nil {
		t.Fatal(w.Code, repo.patch)
	}
	for _, raw := range []string{`{"birthday":null,"birthday":null}`, `{"nick":"outside profile contract"}`, `{"username_color":{"type":"gradient","colors":["#ffffff","#000000"]}}`} {
		w = request(s, "PATCH", "/stargate/accounts/me/profile", raw, true)
		if w.Code < 400 {
			t.Fatal("unsupported/ambiguous payload accepted", raw)
		}
	}
}

func TestProfileMediaReferenceNullAndOmission(t *testing.T) {
	repo := &authoredProfileRepo{}
	s := &Service{Repository: repo, Authority: &authoredProfileAuth{}}
	id := "a7100000-0000-4000-8000-000000000001"
	w := request(s, "PATCH", "/stargate/accounts/me/profile", `{"picture_id":"`+id+`","background_id":null}`, true)
	if w.Code != 200 || repo.patch.PictureID == nil || *repo.patch.PictureID != id || !repo.patch.PicturePresent || !repo.patch.BackgroundPresent || repo.patch.BackgroundID != nil {
		t.Fatal(w.Code, repo.patch)
	}
	w = request(s, "PATCH", "/stargate/accounts/me/profile", `{"bio":"retains media"}`, true)
	if w.Code != 200 || repo.patch.PicturePresent || repo.patch.BackgroundPresent {
		t.Fatal(w.Code, repo.patch)
	}
	for _, raw := range []string{`{"picture_id":"bad"}`, `{"picture_id":""}`, `{"picture_id":null,"picture_id":null}`, `{"Picture_ID":null}`, `{"picture_id":null,"PICTURE_ID":"` + id + `"}`} {
		if w := request(s, "PATCH", "/stargate/accounts/me/profile", raw, true); w.Code != 400 {
			t.Fatal("ambiguous reference accepted", raw, w.Code)
		}
	}
}
