// SPDX-License-Identifier: AGPL-3.0-only
package watchpg

import (
	"context"
	"errors"
	"testing"

	"solsynth.local/go-migration/ports/sphere"
)

const testAccount = "11111111-1111-4111-8111-111111111111"

func TestCanonicalAccountValidation(t *testing.T) {
	for _, id := range []string{testAccount, "00000000-0000-0000-0000-000000000000", "01234567-89ab-cdef-0123-456789abcdef"} {
		if !validAccount(id) {
			t.Fatalf("rejected %q", id)
		}
	}
	for _, id := range []string{"", "abc", "01234567-89AB-cdef-0123-456789abcdef", "{11111111-1111-4111-8111-111111111111}", "111111111111-4111-8111-111111111111", "11111111-1111-4111-8111-11111111111g"} {
		if validAccount(id) {
			t.Fatalf("accepted %q", id)
		}
	}
}
func TestSourceNumbersPinnedEnum(t *testing.T) {
	for i, source := range []sphere.WatchSource{sphere.WatchBookmark, sphere.WatchReaction, sphere.WatchReply} {
		n, err := sourceNumber(source)
		if err != nil || n != i {
			t.Fatal(n, err)
		}
		got, err := sourceName(i)
		if err != nil || got != source {
			t.Fatal(got, err)
		}
	}
	if _, err := sourceName(3); !errors.Is(err, ErrInvalidSource) {
		t.Fatal(err)
	}
	if _, err := sourceNumber("other"); !errors.Is(err, ErrInvalidSource) {
		t.Fatal(err)
	}
}
func TestUUIDGenerator(t *testing.T) {
	first, err := newUUID()
	if err != nil || !validAccount(first) || first[14] != '4' {
		t.Fatal(first, err)
	}
	second, err := newUUID()
	if err != nil || second == first {
		t.Fatal(first, second, err)
	}
}
func TestValidationPrecedesDatabaseMutation(t *testing.T) {
	repo := New(nil)
	ctx := context.Background()
	if _, err := repo.PatchPreferences(ctx, "bad", nil); !errors.Is(err, ErrInvalidAccountID) {
		t.Fatal(err)
	}
	if _, err := repo.PatchPreferences(ctx, testAccount, []sphere.WatchPreferenceUpdate{{Source: "invalid"}}); !errors.Is(err, ErrInvalidSource) {
		t.Fatal(err)
	}
	if _, err := repo.PatchPreferences(ctx, testAccount, []sphere.WatchPreferenceUpdate{{Source: sphere.WatchReply}, {Source: sphere.WatchReply}}); !errors.Is(err, ErrDuplicateLivePreference) {
		t.Fatal(err)
	}
	if err := repo.SavePreferences(ctx, testAccount, []sphere.WatchPreference{sphere.DefaultWatchPreference("other", sphere.WatchReply)}); !errors.Is(err, ErrAccountMismatch) {
		t.Fatal(err)
	}
	rows := []sphere.WatchPreference{sphere.DefaultWatchPreference(testAccount, sphere.WatchReply), sphere.DefaultWatchPreference(testAccount, sphere.WatchReply)}
	if err := repo.SavePreferences(ctx, testAccount, rows); !errors.Is(err, ErrDuplicateLivePreference) {
		t.Fatal(err)
	}
	if _, err := repo.LoadPreferences(ctx, nil); err == nil {
		t.Fatal("nil db accepted")
	}
}
