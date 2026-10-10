// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"context"
	"testing"
	"time"
)

type publisherCompletionFake struct {
	publisherRepo
	op, actor, ref, value string
	enabled               bool
	offset, take          int
}

func (f *publisherCompletionFake) PublisherQuota(context.Context, string) (PublisherQuota, error) {
	return PublisherQuota{Used: 1, Total: 2, Remaining: 1}, nil
}
func (f *publisherCompletionFake) SearchPublishers(_ context.Context, v, q string, t int) ([]Publisher, error) {
	f.op, f.actor, f.value, f.take = "search", v, q, t
	return []Publisher{}, nil
}
func (f *publisherCompletionFake) PublisherHeatmap(_ context.Context, v, r string) ([]HeatmapDay, error) {
	f.op, f.actor, f.ref = "heatmap", v, r
	return []HeatmapDay{}, nil
}
func (f *publisherCompletionFake) PublisherStats(_ context.Context, v, r string) (PublisherStats, error) {
	f.op, f.actor, f.ref = "stats", v, r
	return PublisherStats{}, nil
}
func (f *publisherCompletionFake) PublisherContentStats(_ context.Context, v, r string) (PublisherContentStats, error) {
	f.op, f.actor, f.ref = "content-stats", v, r
	return PublisherContentStats{}, nil
}
func (f *publisherCompletionFake) PublisherFeatures(_ context.Context, a, r string) (map[string]bool, error) {
	f.op, f.actor, f.ref = "features", a, r
	return map[string]bool{}, nil
}
func (f *publisherCompletionFake) SetPublisherFeature(_ context.Context, a, r, v string, x *time.Time, e bool) (PublisherFeature, error) {
	f.op, f.actor, f.ref, f.value, f.enabled = "feature", a, r, v, e
	return PublisherFeature{PublisherID: "canonical", Flag: v, ExpiredAt: x}, nil
}
func (f *publisherCompletionFake) PublisherRewards(_ context.Context, a, r string) (PublisherRewardPreview, error) {
	f.op, f.actor, f.ref = "rewards", a, r
	return PublisherRewardPreview{}, nil
}
func (f *publisherCompletionFake) PublisherRelationships(_ context.Context, a, k string) ([]PublisherRelationship, error) {
	f.op, f.actor, f.value = "relationships", a, k
	return []PublisherRelationship{}, nil
}
func (f *publisherCompletionFake) PublisherRelationshipState(_ context.Context, a, r string) (PublisherRelationship, error) {
	f.op, f.actor, f.ref = "relationship-state", a, r
	return PublisherRelationship{PublisherID: r}, nil
}
func (f *publisherCompletionFake) SetPublisherRelationship(_ context.Context, a, r, k string, e bool) (PublisherRelationship, error) {
	f.op, f.actor, f.ref, f.value, f.enabled = "relationship", a, r, k, e
	return PublisherRelationship{PublisherID: r}, nil
}

func TestPublisherCompletionRoutesBindPrivacyAndAuthority(t *testing.T) {
	f := &publisherCompletionFake{}
	auth := &testAuth{}
	s := &Service{Repository: f, Authority: auth}
	w := request(s, "GET", "/api/publishers/search?query=%25_hidden&take=9", "", false)
	if w.Code != 200 || f.op != "search" || f.actor != "" || f.value != "%_hidden" || f.take != 9 {
		t.Fatal(w.Code, w.Body.String(), f)
	}
	w = request(s, "GET", "/api/publishers/publication/stats", "", false)
	if w.Code != 200 || f.op != "stats" || f.ref != "publication" {
		t.Fatal(w.Code, w.Body.String(), f)
	}
	w = request(s, "GET", "/api/publishers/blocked", "", false)
	if w.Code != 401 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(s, "GET", "/api/publishers/blocked", "", true)
	if w.Code != 200 || f.op != "relationships" || f.actor != actorID || f.value != "blocked" {
		t.Fatal(w.Code, w.Body.String(), f)
	}
	w = request(s, "POST", "/api/publishers/publication/features", `{"flag":"comments.featured"}`, true)
	if w.Code != 200 || f.op != "feature" || !f.enabled || f.value != "comments.featured" || auth.permissions[len(auth.permissions)-1] != "publishers.features.manage" {
		t.Fatal(w.Code, w.Body.String(), f, auth.permissions)
	}
	w = request(s, "POST", "/api/publishers/publication/block", "", true)
	if w.Code != 200 || f.op != "relationship" || !f.enabled || f.value != "block" || auth.permissions[len(auth.permissions)-1] != "publishers.subscriptions.manage" {
		t.Fatal(w.Code, w.Body.String(), f, auth.permissions)
	}
	w = request(s, "GET", "/api/publishers/publication/relationship", "", true)
	if w.Code != 200 || f.op != "relationship-state" {
		t.Fatal(w.Code, w.Body.String(), f.op)
	}
	w = request(s, "DELETE", "/api/publishers/publication/features?flag=comments.featured", "", true)
	if w.Code != 204 || f.op != "feature" || f.enabled || f.value != "comments.featured" {
		t.Fatal(w.Code, w.Body.String(), f)
	}
}

func TestPublisherCompletionRejectsAmbiguousInputsBeforeRepository(t *testing.T) {
	for _, path := range []string{"/api/publishers/search?query=x&query=y", "/api/publishers/search?query=x&take=0", "/api/publishers/publication/stats?private=1"} {
		f := &publisherCompletionFake{}
		w := request(&Service{Repository: f, Authority: &testAuth{}}, "GET", path, "", true)
		if w.Code < 400 || f.op != "" {
			t.Fatal(path, w.Code, f.op)
		}
	}
	f := &publisherCompletionFake{}
	w := request(&Service{Repository: f, Authority: &testAuth{}}, "POST", "/api/publishers/publication/features", `{"flag":"x","actor":"forged"}`, true)
	if w.Code < 400 || f.op != "" {
		t.Fatal(w.Code, w.Body.String(), f.op)
	}
}
