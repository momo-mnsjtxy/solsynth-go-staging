// SPDX-License-Identifier: AGPL-3.0-only
package messager

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

func placeholderPtr[T any](v T) *T { return &v }
func placeholderFixture(t *testing.T, kind PlaceholderKind) PlaceholderSnapshot {
	t.Helper()
	result, err := SendPlaceholder("room", "sender", kind, nil, time.UnixMilli(100))
	if err != nil {
		t.Fatal(err)
	}
	return result.Created
}
func TestPlaceholderSendAndReplacement(t *testing.T) {
	now := time.UnixMilli(100)
	for _, kind := range []PlaceholderKind{PlaceholderStreaming, PlaceholderUploading} {
		m := placeholderFixture(t, kind)
		if m.Type != "placeholder" || *m.Placeholder.ExpiresAtMS != 3600100 || m.Attachments == nil || m.DeletedAt != nil {
			t.Fatal(m)
		}
		if kind == PlaceholderStreaming && (m.Placeholder.Content == nil || *m.Placeholder.Content != "" || m.Placeholder.Progress != nil) {
			t.Fatal(m)
		}
		if kind == PlaceholderUploading && (m.Placeholder.Progress == nil || *m.Placeholder.Progress != 0 || m.Placeholder.Content != nil) {
			t.Fatal(m)
		}
	}
	for _, content := range []string{"", " \t\n\u0085\u00a0", "retained\n"} {
		m := placeholderFixture(t, PlaceholderStreaming)
		m.Placeholder.Content = &content
		before := clonePlaceholder(m)
		result, err := SendPlaceholder("room", "sender", PlaceholderUploading, &m, now)
		if err != nil {
			t.Fatal(err)
		}
		if content == "retained\n" {
			if result.Previous.Type != "text" || *result.Previous.Content != content || result.Previous.Placeholder != nil {
				t.Fatal(result)
			}
		} else if result.Previous.DeletedAt == nil || !result.Previous.DeletedAt.Equal(now) {
			t.Fatal(result)
		}
		if !reflect.DeepEqual(m, before) {
			t.Fatal("mutated prior")
		}
	}
}
func TestPlaceholderErrors(t *testing.T) {
	now := time.UnixMilli(1)
	if _, err := SendPlaceholder("", "", PlaceholderUnknown, nil, now); !errors.Is(err, ErrPlaceholderKind) {
		t.Fatal(err)
	}
	live := placeholderFixture(t, PlaceholderStreaming)
	if _, err := SendPlaceholder("other", "sender", PlaceholderStreaming, &live, now); !errors.Is(err, ErrPlaceholderScope) {
		t.Fatal(err)
	}
	deleted := ExpirePlaceholder(live, now)
	for _, tc := range []struct {
		m   PlaceholderSnapshot
		err error
	}{{PlaceholderSnapshot{}, ErrNotPlaceholder}, {deleted, ErrPlaceholderDeleted}} {
		if _, err := UpdatePlaceholder(tc.m, nil, nil, now); !errors.Is(err, tc.err) {
			t.Fatal(err)
		}
		if _, err := FinalizePlaceholder(tc.m, nil, nil, nil); !errors.Is(err, tc.err) {
			t.Fatal(err)
		}
		tc.m.RoomID = "room"
		tc.m.SenderID = "sender"
		if _, err := SendPlaceholder("room", "sender", PlaceholderStreaming, &tc.m, now); !errors.Is(err, tc.err) {
			t.Fatal(err)
		}
	}
}
func TestPlaceholderUpdates(t *testing.T) {
	now := time.UnixMilli(7200100)
	m := placeholderFixture(t, PlaceholderStreaming)
	m.Placeholder.Content = nil
	got, err := UpdatePlaceholder(m, placeholderPtr("hello"), placeholderPtr(0.9), now)
	if err != nil || *got.Placeholder.Content != "hello" || got.Placeholder.Progress != nil || *got.Placeholder.ExpiresAtMS != 10800100 {
		t.Fatal(got, err)
	}
	got, err = UpdatePlaceholder(got, placeholderPtr("世界"), nil, now)
	if err != nil || *got.Placeholder.Content != "hello世界" {
		t.Fatal(got, err)
	}
	for _, value := range []float64{-1, 0, 0.4, 1, 2, math.Inf(-1), math.Inf(1), math.NaN()} {
		upload := placeholderFixture(t, PlaceholderUploading)
		got, err := UpdatePlaceholder(upload, placeholderPtr("ignored"), &value, now)
		if err != nil || got.Placeholder.Content != nil {
			t.Fatal(got, err)
		}
		want := value
		if want < 0 {
			want = 0
		}
		if want > 1 {
			want = 1
		}
		if *got.Placeholder.Progress != want && !(math.IsNaN(want) && math.IsNaN(*got.Placeholder.Progress)) {
			t.Fatal(value, got)
		}
	}
	m.Placeholder.Kind = PlaceholderUnknown
	got, err = UpdatePlaceholder(m, placeholderPtr("ignored"), placeholderPtr(0.8), now)
	if err != nil || got.Placeholder.Content != nil || got.Placeholder.Progress != nil || *got.Placeholder.ExpiresAtMS != 10800100 {
		t.Fatal(got, err)
	}
}
func TestPlaceholderFinalizeAndCopies(t *testing.T) {
	m := placeholderFixture(t, PlaceholderStreaming)
	m.Placeholder.Content = placeholderPtr("accumulated")
	m.Attachments = []json.RawMessage{json.RawMessage(`{"opaque":1}`)}
	m.OtherMeta = map[string]json.RawMessage{"retained": json.RawMessage(`{"nested":[1]}`), "placeholder_kind": json.RawMessage(`"old"`)}
	before := clonePlaceholder(m)
	extras := map[string]json.RawMessage{"placeholder_kind": json.RawMessage(`"reintroduced"`), "new": json.RawMessage(`[1]`)}
	got, err := FinalizePlaceholder(m, nil, nil, extras)
	if err != nil || got.Type != "text" || *got.Content != "accumulated" || got.Placeholder != nil || len(got.Attachments) != 1 || string(got.OtherMeta["placeholder_kind"]) != `"reintroduced"` {
		t.Fatal(got, err)
	}
	got.Attachments[0][0] = ' '
	got.OtherMeta["retained"][0] = ' '
	got.OtherMeta["new"][0] = ' '
	if !reflect.DeepEqual(m, before) || string(extras["new"]) != `[1]` {
		t.Fatal("alias")
	}
	got, err = FinalizePlaceholder(m, placeholderPtr(""), []json.RawMessage{}, nil)
	if err != nil || *got.Content != "" || len(got.Attachments) != 0 || got.Attachments == nil {
		t.Fatal(got, err)
	}
	if _, ok := got.OtherMeta["placeholder_kind"]; ok {
		t.Fatal("placeholder metadata retained")
	}
	empty := placeholderFixture(t, PlaceholderUploading)
	got, err = FinalizePlaceholder(empty, nil, nil, nil)
	if err != nil || *got.Content != "" || got.OtherMeta != nil {
		t.Fatal(got, err)
	}
	if _, err = UpdatePlaceholder(got, nil, nil, time.Now()); !errors.Is(err, ErrNotPlaceholder) {
		t.Fatal(err)
	}
}
func TestPlaceholderStalenessAndExpiration(t *testing.T) {
	m := placeholderFixture(t, PlaceholderStreaming)
	for _, tc := range []struct {
		ms   int64
		want bool
	}{{3600099, false}, {3600100, true}, {3600101, true}} {
		if got := PlaceholderIsStale(m, time.UnixMilli(tc.ms)); got != tc.want {
			t.Fatal(tc, got)
		}
	}
	m.Placeholder.ExpiresAtMS = nil
	if PlaceholderIsStale(m, time.Now()) {
		t.Fatal("missing expiry")
	}
	m = placeholderFixture(t, PlaceholderStreaming)
	deleted := ExpirePlaceholder(m, time.UnixMilli(4000000))
	if PlaceholderIsStale(deleted, time.UnixMilli(5000000)) || m.DeletedAt != nil {
		t.Fatal("deleted selected/input mutation")
	}
	deleted = ExpirePlaceholder(deleted, time.UnixMilli(5000000))
	if deleted.DeletedAt.UnixMilli() != 5000000 || deleted.UpdatedAt.UnixMilli() != 5000000 {
		t.Fatal(deleted)
	}
	plain := ExpirePlaceholder(PlaceholderSnapshot{Type: "text"}, time.UnixMilli(10))
	if plain.DeletedAt.UnixMilli() != 10 || PlaceholderIsStale(plain, time.Now()) {
		t.Fatal(plain)
	}
}
func TestPlaceholderCloneIsolation(t *testing.T) {
	m := placeholderFixture(t, PlaceholderUploading)
	m.Content = placeholderPtr("x")
	m.DeletedAt = placeholderPtr(time.UnixMilli(1))
	copy := clonePlaceholder(m)
	*copy.Placeholder.Progress = 0.9
	*copy.Placeholder.ExpiresAtMS = -1
	*copy.Content = "y"
	*copy.DeletedAt = time.UnixMilli(2)
	if *m.Placeholder.Progress != 0 || *m.Placeholder.ExpiresAtMS != 3600100 || *m.Content != "x" || m.DeletedAt.UnixMilli() != 1 {
		t.Fatal("alias")
	}
}

func TestPlaceholderSelectionRequiresType(t *testing.T) {
	m := placeholderFixture(t, PlaceholderStreaming)
	m.Type = "text"
	if PlaceholderIsStale(m, time.UnixMilli(4000000)) {
		t.Fatal("selected non-placeholder type")
	}
	if _, err := SendPlaceholder("room", "sender", PlaceholderStreaming, &m, time.UnixMilli(1)); !errors.Is(err, ErrNotPlaceholder) {
		t.Fatal(err)
	}
}
