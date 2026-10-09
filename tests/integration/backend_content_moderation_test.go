// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"solsynth.local/go-migration/ports/sphere/posts"
	"testing"
)

func TestPGBackendContentModerationComposition(t *testing.T) {
	f := newModerationFixture(t)
	app := backendApp(t, f.db, f.config)
	p := f.post(t, f.individual, "Synthetic composed moderation", posts.Public)
	backendCall(t, app, "GET", "/api/posts/"+p.ID, "", "", 200)
	backendCall(t, app, "GET", "/sphere/admin/posts/"+p.ID, f.token, "", 403)
	backendCall(t, app, "GET", "/sphere/admin/posts/"+p.ID, f.outsideToken, "", 200)
	backendJSONCall(t, app, "POST", "/api/admin/posts/"+p.ID+"/shadowban", f.outsideToken, map[string]any{"reason": "spam"}, 200)
	backendCall(t, app, "GET", "/api/posts/"+p.ID, "", "", 404)
	// Global authority permits only admin inspection, not ordinary hidden reads.
	backendCall(t, app, "GET", "/api/posts/"+p.ID, f.outsideToken, "", 404)
	backendCall(t, app, "GET", "/api/posts/"+p.ID, f.token, "", 200)
	backendCall(t, app, "DELETE", "/sphere/admin/posts/"+p.ID+"/shadowban", f.outsideToken, "", 200)
	backendCall(t, app, "GET", "/api/posts/"+p.ID, "", "", 200)
	query(t, f.db, `UPDATE owned_authority_accounts SET is_superuser=false,version=version+1 WHERE id=$1::uuid`, f.outside)
	backendCall(t, app, "GET", "/api/admin/posts/"+p.ID, f.outsideToken, "", 401)
}
