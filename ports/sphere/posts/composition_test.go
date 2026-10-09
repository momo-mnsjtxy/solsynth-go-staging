// SPDX-License-Identifier: AGPL-3.0-only
package posts

import (
	"encoding/json"
	"solsynth.local/go-migration/ports/sphere/reader"
	"strings"
	"testing"
)

func TestComposerNativeAndLegacyStrictRequests(t *testing.T) {
	for _, body := range []string{`{"content":"a","survey_id":"` + testActor + `"}`, `{"content":"a","embeds":[{"type":"calendar_event","id":"` + testActor + `"},{"type":"location","name":"Venue","wkt":"POINT(1 2)"}]}`, `{"content":"a","embeds":[]}`, `{"content":"a","thumbnail_id":null}`} {
		repo := &fakeRepo{}
		w := request(NewHandler(repo, &fakeAuth{actor: testActor}), "POST", "/api/posts", body)
		if w.Code != 200 || repo.calls != 1 {
			t.Fatal(body, w.Code, w.Body.String())
		}
	}
	for _, body := range []string{`{"content":"a","embeds":[{"type":"survey","id":"` + testActor + `","title":"copied secret"}]}`, `{"content":"a","embeds":[{"type":"location","wkt":"POINT(200 3)"}]}`, `{"content":"a","embeds":[{"type":"location","wkt":"LINESTRING(1 2,3 4)"}]}`, `{"content":"a","embeds":[{"type":"fund","id":"https://private/"}]}`, `{"content":"a","thumbnail_id":"https://private/"}`, `{"content":"a","embeds":[],"survey_id":"` + testActor + `"}`} {
		repo := &fakeRepo{}
		w := request(NewHandler(repo, &fakeAuth{actor: testActor}), "POST", "/api/posts", body)
		if w.Code != 400 && w.Code != 501 || repo.calls != 0 {
			t.Fatal(body, w.Code, w.Body.String())
		}
	}
}
func TestComposerOmissionClearingAndMetadataRoundTrip(t *testing.T) {
	old := []Embed{{Type: "survey", ID: testActor}}
	got, e := ResolveEmbeds(old, Patch{})
	if e != nil || len(got) != 1 {
		t.Fatal(got, e)
	}
	got, e = ResolveEmbeds(old, Patch{EmbedUpdates: map[string]*Embed{"survey": nil}})
	if e != nil || len(got) != 0 {
		t.Fatal(got, e)
	}
	blank := []Embed{}
	got, e = ResolveEmbeds(old, Patch{Embeds: &blank})
	if e != nil || len(got) != 0 {
		t.Fatal(got, e)
	}
	m := Metadata{Composed: old, Thumbnail: &old[0].ID}
	b, e := json.Marshal(m)
	if e != nil || strings.Contains(string(b), "url") {
		t.Fatal(string(b), e)
	}
	var restored Metadata
	if e = json.Unmarshal(b, &restored); e != nil || len(restored.Composed) != 1 || restored.Composed[0].ID != testActor {
		t.Fatal(restored, e)
	}
	blog := BlogMetadata(reader.Link{URL: "https://example.com/", Title: "Example"})
	b, e = json.Marshal(blog)
	if e != nil || !strings.Contains(string(b), `"type":"link"`) || strings.Contains(string(b), "thumbnail") {
		t.Fatal(string(b), e)
	}
}
