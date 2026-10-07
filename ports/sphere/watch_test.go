// SPDX-License-Identifier: AGPL-3.0-only
package sphere

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type memoryWatchRepository struct {
	rows             []WatchPreference
	loadErr, saveErr error
	saves            int
}

func (m *memoryWatchRepository) LoadPreferences(ctx context.Context, ids []string) ([]WatchPreference, error) {
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	rows := []WatchPreference{}
	for _, p := range m.rows {
		for _, id := range ids {
			if p.AccountID == id {
				rows = append(rows, p)
				break
			}
		}
	}
	return rows, nil
}
func (m *memoryWatchRepository) SavePreferences(ctx context.Context, id string, rows []WatchPreference) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.saves++
	for _, p := range rows {
		found := false
		for i, stored := range m.rows {
			if stored.AccountID == id && stored.Source == p.Source {
				m.rows[i] = p
				found = true
				break
			}
		}
		if !found {
			m.rows = append(m.rows, p)
		}
	}
	return nil
}

type fixtureWatcherRepository struct {
	accounts WatchAccounts
	err      error
}

func (m fixtureWatcherRepository) WatchAccounts(context.Context, []string) (WatchAccounts, error) {
	return m.accounts, m.err
}
func (m fixtureWatcherRepository) ChainPostIDs(context.Context, string) ([]string, error) {
	return []string{"head", "child"}, m.err
}

type fixtureWatchAuth struct {
	account                string
	authErr, permissionErr error
}

func (a fixtureWatchAuth) AccountID(*http.Request) (string, error) { return a.account, a.authErr }
func (a fixtureWatchAuth) CanManagePostSubscriptions(*http.Request, string) error {
	return a.permissionErr
}

func TestWatchDefaultEventMatrix(t *testing.T) {
	for _, source := range watchSources {
		p := DefaultWatchPreference("account", source)
		for _, event := range []WatchEvent{WatchReactions, WatchReplies, WatchChains, WatchForwards, WatchEdits, "invalid"} {
			want := event != "invalid" && (source == WatchBookmark || event == WatchChains || event == WatchEdits)
			if p.Allows(event) != want {
				t.Fatalf("%s %s", source, event)
			}
		}
	}
}

func TestWatchPreferencePartialUpdate(t *testing.T) {
	repo := &memoryWatchRepository{}
	service := &WatchService{Repository: repo}
	ctx := context.Background()
	got, err := service.GetEffectivePreferences(ctx, "a")
	if err != nil || len(got) != 3 {
		t.Fatal(got, err)
	}
	if repo.saves != 0 {
		t.Fatal("reading defaults persisted data")
	}
	f := false
	tr := true
	got, err = service.UpdatePreferences(ctx, "a", []WatchPreferenceUpdate{{Source: WatchBookmark, NotifyReactions: &f, NotifyReplies: &f, NotifyChains: &f, NotifyForwards: &f, NotifyEdits: &f}, {Source: WatchReaction, NotifyReplies: &tr}})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].NotifyReactions || got[0].NotifyReplies || got[0].NotifyChains || got[0].NotifyForwards || got[0].NotifyEdits {
		t.Fatal(got[0])
	}
	if !got[1].NotifyReplies || !got[1].NotifyChains || !got[1].NotifyEdits || got[1].NotifyForwards {
		t.Fatal(got[1])
	}
	other, err := service.GetEffectivePreferences(ctx, "b")
	if err != nil || !other[0].NotifyReactions {
		t.Fatal(other, err)
	}
	_, err = service.UpdatePreferences(ctx, "a", nil)
	if err != nil || repo.saves != 1 {
		t.Fatal(err, repo.saves)
	}
	if _, err = service.UpdatePreferences(ctx, "a", []WatchPreferenceUpdate{{Source: WatchReply}, {Source: WatchReply}}); err == nil {
		t.Fatal("accepted duplicate source")
	}
	if _, err = service.UpdatePreferences(ctx, "a", []WatchPreferenceUpdate{{Source: "other"}}); err == nil {
		t.Fatal("accepted unknown source")
	}
}

func TestWatchResolutionUnionAndDefaults(t *testing.T) {
	repo := &memoryWatchRepository{rows: []WatchPreference{DefaultWatchPreference("a", WatchBookmark)}}
	repo.rows[0].NotifyReactions = false
	service := &WatchService{Repository: repo}
	watchers := fixtureWatcherRepository{accounts: WatchAccounts{Bookmarkers: []string{"a", "b", "b"}, Reactors: []string{"b", "c"}, Repliers: []string{"a", "d"}}}
	got, err := service.ResolveWatcherAccountIDs(context.Background(), watchers, []string{"post"}, WatchReactions)
	if err != nil || !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatal(got, err)
	}
	got, err = service.ResolveWatcherAccountIDs(context.Background(), watchers, []string{"post"}, WatchChains)
	if err != nil || !reflect.DeepEqual(got, []string{"a", "b", "c", "d"}) {
		t.Fatal(got, err)
	}
	got, err = service.ResolveWatcherAccountIDs(context.Background(), nil, nil, WatchChains)
	if err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	got, err = service.GetChainPostIDs(context.Background(), watchers, "head")
	if err != nil || len(got) != 2 {
		t.Fatal(got, err)
	}
}

func TestWatchRepositoryErrors(t *testing.T) {
	ctx := context.Background()
	bad := errors.New("repository failed")
	repo := &memoryWatchRepository{loadErr: bad}
	service := &WatchService{Repository: repo}
	if _, err := service.GetEffectivePreferences(ctx, "a"); !errors.Is(err, bad) {
		t.Fatal(err)
	}
	repo.loadErr = nil
	repo.saveErr = bad
	if _, err := service.UpdatePreferences(ctx, "a", []WatchPreferenceUpdate{{Source: WatchReply}}); !errors.Is(err, bad) {
		t.Fatal(err)
	}
	repo.saveErr = nil
	repo.rows = []WatchPreference{DefaultWatchPreference("a", WatchReply), DefaultWatchPreference("a", WatchReply)}
	if _, err := service.GetEffectivePreferences(ctx, "a"); err == nil {
		t.Fatal("duplicate rows accepted")
	}
	if _, err := (&WatchService{}).GetEffectivePreferences(ctx, "a"); err == nil {
		t.Fatal("nil repo accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := service.GetEffectivePreferences(canceled, "a"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestWatchHTTPReadWriteAndAuth(t *testing.T) {
	repo := &memoryWatchRepository{}
	service := &WatchService{Repository: repo}
	for _, tc := range []struct {
		method, path, body string
		auth               WatchAuth
		want               int
	}{
		{"GET", "/api/posts/watch", "", fixtureWatchAuth{account: "a"}, 200},
		{"PUT", "/api/posts/watch", `{"bookmark":{"reactions":false},"reaction":{"replies":true},"reply":{"edits":false}}`, fixtureWatchAuth{account: "a"}, 200},
		{"PUT", "/api/posts/watch", `{}`, fixtureWatchAuth{account: "a", permissionErr: errors.New("denied")}, 403},
		{"GET", "/api/posts/watch", "", fixtureWatchAuth{}, 401},
		{"GET", "/api/posts/watch", "", nil, 503},
		{"POST", "/api/posts/watch", `{}`, fixtureWatchAuth{account: "a"}, 405},
		{"GET", "/elsewhere", "", fixtureWatchAuth{account: "a"}, 404},
		{"PUT", "/api/posts/watch", `null`, fixtureWatchAuth{account: "a"}, 400},
		{"PUT", "/api/posts/watch", `{} {}`, fixtureWatchAuth{account: "a"}, 400},
		{"PUT", "/api/posts/watch", `{"bookmark":{"edits":"bad"}}`, fixtureWatchAuth{account: "a"}, 400},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		w := httptest.NewRecorder()
		NewWatchHandler(service, tc.auth).ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s %s: %d %s", tc.method, tc.body, w.Code, w.Body.String())
		}
		if w.Code == 200 {
			var rows []map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil || len(rows) != 3 {
				t.Fatal(rows, err)
			}
			if _, exists := rows[0]["AccountID"]; exists {
				t.Fatal("leaked account ID")
			}
			if rows[0]["source"] != "bookmark" {
				t.Fatal(rows)
			}
		}
	}
	if repo.saves != 1 {
		t.Fatalf("unauthorized or malformed requests changed storage: saves=%d", repo.saves)
	}
	got, _ := service.GetEffectivePreferences(context.Background(), "a")
	if got[0].NotifyReactions || !got[1].NotifyReplies || got[2].NotifyEdits {
		t.Fatal(got)
	}
}

func TestConcurrentWatchPatchesPreserveDistinctFields(t *testing.T) {
	for i := 0; i < 100; i++ {
		repo := &memoryWatchRepository{}
		service := &WatchService{Repository: repo}
		f := false
		var wg sync.WaitGroup
		for _, update := range []WatchPreferenceUpdate{{Source: WatchBookmark, NotifyReactions: &f}, {Source: WatchBookmark, NotifyReplies: &f}} {
			wg.Add(1)
			go func(u WatchPreferenceUpdate) {
				defer wg.Done()
				if _, err := service.UpdatePreferences(context.Background(), "a", []WatchPreferenceUpdate{u}); err != nil {
					t.Error(err)
				}
			}(update)
		}
		wg.Wait()
		got, err := service.GetEffectivePreferences(context.Background(), "a")
		if err != nil || got[0].NotifyReactions || got[0].NotifyReplies {
			t.Fatal(got, err)
		}
	}
}

type transactionalWatchRepository struct{ called bool }

func (r *transactionalWatchRepository) LoadPreferences(context.Context, []string) ([]WatchPreference, error) {
	return nil, errors.New("non-transactional load should not be called")
}
func (r *transactionalWatchRepository) SavePreferences(context.Context, string, []WatchPreference) error {
	return errors.New("non-transactional save should not be called")
}
func (r *transactionalWatchRepository) PatchPreferences(_ context.Context, account string, updates []WatchPreferenceUpdate) ([]WatchPreference, error) {
	r.called = true
	return []WatchPreference{DefaultWatchPreference(account, updates[0].Source)}, nil
}
func TestWatchUsesTransactionalRepositoryPatch(t *testing.T) {
	repo := &transactionalWatchRepository{}
	service := &WatchService{Repository: repo}
	got, err := service.UpdatePreferences(context.Background(), "a", []WatchPreferenceUpdate{{Source: WatchReply}})
	if err != nil || !repo.called || len(got) != 1 || got[0].AccountID != "a" {
		t.Fatal(got, err, repo.called)
	}
	repo.called = false
	if _, err := service.UpdatePreferences(context.Background(), "a", []WatchPreferenceUpdate{{Source: "invalid"}}); err == nil || repo.called {
		t.Fatal("invalid source sent to transaction")
	}
}
