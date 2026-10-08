// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/apppg"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/passport/accounts"
	"strings"
	"testing"
)

func boardDiscover(t *testing.T, f developerFixture, slug string) []apps.BoardDiscoveryApp {
	t.Helper()
	return backendDecode[[]apps.BoardDiscoveryApp](t, backendCall(t, f.handler, "GET", "/api/apps/board?slug="+slug, "", "", 200))
}
func TestPGBoardDiscoveryCurrentVisibilityAndDTO(t *testing.T) {
	f := newDeveloperFixture(t)
	_, a, q := f.setup(t)
	root := "/api/private/apps/" + a.ID
	widget := map[string]any{"key": "counter", "name": "Counter", "description": "Synthetic widget", "is_enabled": true, "renderer_type": "data", "payload_type": "object", "field_types": []any{}, "required_fields": []string{}, "allow_multiple": true}
	backendJSONCall(t, f.handler, "POST", root+"/board"+q, f.token, widget, 200)
	if got := boardDiscover(t, f, a.Slug); len(got) != 0 {
		t.Fatal("developing app public")
	}
	backendJSONCall(t, f.handler, "PATCH", root+q, f.token, map[string]int{"status": 1}, 200)
	got := boardDiscover(t, f, strings.ToUpper(a.Slug))
	if len(got) != 1 || len(got[0].BoardWidgets) != 1 || got[0].PublisherName != f.dev || got[0].BoardWidgets[0].Slug != f.dev+"."+a.Slug+".counter" {
		t.Fatal(got)
	}
	raw, _ := json.Marshal(got)
	for _, field := range []string{"oauth_config", "payload_type", "secret_hash", "project_id", "account_id"} {
		if strings.Contains(string(raw), field) {
			t.Fatal("private/uncontracted field", field)
		}
	}
	widget["is_enabled"] = false
	backendJSONCall(t, f.handler, "PUT", root+"/board/counter"+q, f.token, widget, 200)
	if len(boardDiscover(t, f, a.Slug)) != 0 {
		t.Fatal("disabled widget public")
	}
	widget["is_enabled"] = true
	backendJSONCall(t, f.handler, "PUT", root+"/board/counter"+q, f.token, widget, 200)
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1::uuid`, f.actor)
	if len(boardDiscover(t, f, a.Slug)) != 0 {
		t.Fatal("disabled owner public")
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=false WHERE id=$1::uuid`, f.actor)
	query(t, f.db, `UPDATE owned_publishers SET shadowbanned_at=clock_timestamp() WHERE name=$1::text`, f.dev)
	if len(boardDiscover(t, f, a.Slug)) != 0 {
		t.Fatal("shadowbanned publisher public")
	}
	query(t, f.db, `UPDATE owned_publishers SET shadowbanned_at=NULL WHERE name=$1::text`, f.dev)
	backendJSONCall(t, f.handler, "PATCH", root+q, f.token, map[string]any{"oauth_config": map[string]any{"allowed_scopes": []string{"orders.create"}}}, 200)
	if len(boardDiscover(t, f, a.Slug)) != 0 {
		t.Fatal("scope removal not current")
	}
	backendJSONCall(t, f.handler, "PATCH", root+q, f.token, map[string]any{"oauth_config": map[string]any{"allowed_scopes": []string{apps.BoardScope}}}, 200)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DELETE FROM owned_app_widgets WHERE app_id=$1::uuid AND key LIKE 'synthetic-overflow-%'`, a.ID)
	})
	query(t, f.db, `INSERT INTO owned_app_widgets(app_id,key,manifest,created_at,updated_at)SELECT $1::uuid,'synthetic-overflow-'||n::text,jsonb_build_object('key','synthetic-overflow-'||n::text,'name','Synthetic bound','is_enabled',true),clock_timestamp(),clock_timestamp() FROM generate_series(1,32) n`, a.ID)
	backendCall(t, f.handler, "GET", "/api/apps/board?slug="+a.Slug, "", "", 503)
	query(t, f.db, `DELETE FROM owned_app_widgets WHERE app_id=$1::uuid AND key LIKE 'synthetic-overflow-%'`, a.ID)
	if len(boardDiscover(t, f, "missing-"+a.ID)) != 0 {
		t.Fatal("missing app not empty")
	}
}
func TestPGBackendBoardDiscoveryComposition(t *testing.T) {
	f := newBackendFixture(t)
	query(t, f.db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at)VALUES($1::uuid,2,$2::timestamptz)`, f.actor, f.config.Now())
	pub := backendDecode[accounts.Publisher](t, backendJSONCall(t, f.app, "POST", "/sphere/publishers/individual", f.token, map[string]string{"name": "board-" + f.actor, "nick": "Synthetic board"}, 200))
	backendCall(t, f.app, "POST", "/develop/developers/"+pub.Name+"/enroll", f.token, "", 200)
	project := backendDecode[apps.Project](t, backendJSONCall(t, f.app, "POST", "/develop/private/projects?dev="+pub.Name, f.token, map[string]string{"slug": "board", "name": "Board"}, 201))
	q := "?dev=" + pub.Name + "&proj=" + project.ID
	a := backendDecode[apps.App](t, backendJSONCall(t, f.app, "POST", "/develop/private/apps"+q, f.token, map[string]any{"slug": "board-" + project.ID, "name": "Synthetic board app", "status": 1, "oauth_config": map[string]any{"allowed_scopes": []string{apps.BoardScope}}}, 201))
	backendJSONCall(t, f.app, "POST", "/develop/private/apps/"+a.ID+"/board"+q, f.token, map[string]any{"key": "status", "name": "Status", "is_enabled": true, "renderer_type": "data", "payload_type": "object"}, 200)
	for _, prefix := range []string{"/api", "/develop"} {
		got := backendDecode[[]apps.BoardDiscoveryApp](t, backendCall(t, f.app, "GET", prefix+"/apps/board?slug="+a.Slug+"&take=0&offset=99", "", "", 200))
		if len(got) != 1 || got[0].ID != a.ID {
			t.Fatal("board discovery route or slug pagination", got)
		}
	}
	backendCall(t, f.app, "GET", "/api/apps/board?offset=100001", "", "", 400)
}
func TestPGBoardDiscoveryRestartPersistence(t *testing.T) {
	p := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "board-discovery.json")
	if os.Getenv("PG_TEST_PHASE") == "after" {
		var saved apps.BoardDiscoveryApp
		raw, e := os.ReadFile(p)
		if e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(raw, &saved); e != nil {
			t.Fatal(e)
		}
		got, e := apppg.NewWithAuthority(database(t), nil).DiscoverBoard(context.Background(), apps.BoardDiscoveryQuery{Take: 20, Slug: saved.Slug})
		if e != nil || len(got) != 1 || got[0].ID != saved.ID || got[0].BoardWidgets[0].Slug != saved.BoardWidgets[0].Slug {
			t.Fatal(got, e)
		}
		return
	}
	f := newDeveloperFixture(t)
	_, a, q := f.setup(t)
	root := "/api/private/apps/" + a.ID
	backendJSONCall(t, f.handler, "PATCH", root+q, f.token, map[string]int{"status": 1}, 200)
	backendJSONCall(t, f.handler, "POST", root+"/board"+q, f.token, map[string]any{"key": "persisted", "name": "Persisted", "is_enabled": true, "payload_type": "object"}, 200)
	got := boardDiscover(t, f, a.Slug)
	if len(got) != 1 {
		t.Fatal(got)
	}
	raw, e := json.Marshal(got[0])
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(p, raw, 0600); e != nil {
		t.Fatal(e)
	}
}
