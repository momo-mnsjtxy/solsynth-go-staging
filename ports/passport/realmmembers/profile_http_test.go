// SPDX-License-Identifier: AGPL-3.0-only
package realmmembers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/social"
	"strings"
	"testing"
)

type profileFake struct {
	Repository
	calls   int
	profile ProfileRequest
}

func (f *profileFake) UpdateProfile(_ context.Context, _, _ string, q ProfileRequest) (social.Member, error) {
	f.calls++
	f.profile = q
	return social.Member{Nick: q.Nick, Bio: q.Bio}, nil
}
func (f *profileFake) DeleteProfile(context.Context, string, string) (social.Member, error) {
	f.calls++
	return social.Member{}, nil
}
func (f *profileFake) ModerationLogs(context.Context, string, string, int, int) ([]ModerationLog, int64, error) {
	f.calls++
	return []ModerationLog{}, 0, nil
}
func (f *profileFake) Quota(context.Context, string) (Quota, error) {
	f.calls++
	return Quota{Records: []QuotaRecord{}}, nil
}
func TestProfileStrictReplacementAndWireContract(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{"Nick":"x"}`, `{"nick":"a","nick":"b"}`, `{"nick":"a","\\u006eick":"b"}`, `{"avatar_id":"x"}`, `{"background_id":"x"}`, `{"bio":12}`} {
		var q ProfileRequest
		if json.Unmarshal([]byte(raw), &q) == nil {
			t.Fatal(raw)
		}
	}
	for _, q := range []ProfileRequest{{Nick: str(strings.Repeat("😀", 513))}, {Bio: str(strings.Repeat("a", 4097))}, {Nick: str("a\x00")}} {
		if ValidateProfile(q) == nil {
			t.Fatal("invalid length accepted")
		}
	}
	var q ProfileRequest
	if json.Unmarshal([]byte(`{"nick":"hello"}`), &q) != nil || q.Nick == nil || q.Bio != nil {
		t.Fatal(q)
	}
	f := &profileFake{}
	auth := func(r *http.Request, required bool) (string, error) {
		if !required {
			t.Fatal("sensitive read not authenticated")
		}
		if r.Header.Get("Authorization") != "Bearer yes" {
			return "", authority.ErrInvalid
		}
		return testID, nil
	}
	h := NewHandler(f, auth)
	for _, c := range []struct {
		method, path, body, token string
		status                    int
	}{{"PATCH", "/api/realms/x/members/me/profile", `{"nick":"realm","bio":null}`, "yes", 200}, {"DELETE", "/passport/realms/x/members/me/profile", "", "yes", 200}, {"GET", "/api/realms/x/posts/moderation-logs?offset=0&take=20", "", "yes", 200}, {"GET", "/passport/realms/quota", "", "yes", 200}, {"GET", "/api/realms/quota", "", "", 401}, {"PATCH", "/api/realms/x/members/me/profile", `{"account_id":"x"}`, "yes", 400}, {"GET", "/api/realms/x/posts/moderation-logs?take=0", "", "yes", 400}, {"GET", "/api/realms/x/posts/moderation-logs?take=1&take=2", "", "yes", 400}, {"GET", "/api/realms/quota?account_id=x", "", "yes", 400}, {"DELETE", "/api/realms/x/members/me/profile", `{}`, "yes", 400}, {"GET", "/api/realms/x/members/me/profile", "", "yes", 405}} {
		r := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		r.Header.Set("Authorization", "Bearer "+c.token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.status {
			t.Fatal(c, w.Code, w.Body.String())
		}
	}
	if f.calls != 4 {
		t.Fatal(f.calls)
	}
	raw, _ := json.Marshal(Quota{PerkLevel: 2, Records: []QuotaRecord{}})
	if !strings.Contains(string(raw), `"perk_level":2`) || strings.Contains(string(raw), "perkLevel") {
		t.Fatal(string(raw))
	}
	if Handles("/api/realms/x/members/" + testID + "/profile") {
		t.Fatal("accepted arbitrary target profile")
	}
}
