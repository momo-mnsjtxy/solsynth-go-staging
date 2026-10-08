// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/ports/authority"
	profiles "solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/board"
	"solsynth.local/go-migration/ports/sphere/posts"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
)

// Every setup starts with database(t)'s marked disposable-cluster check. These
// tests never discover an arbitrary DB, migrate production, or persist real keys.
type backendFixture struct {
	db                              *sql.DB
	app                             *application.Backend
	issuer                          *authority.Service
	config                          authority.Config
	keys                            string
	actor, other, token, otherToken string
}

func backendApp(t *testing.T, db *sql.DB, config authority.Config) *application.Backend {
	t.Helper()
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, err := application.NewPostgres(db, nil, application.Config{Authority: config, PublicVisibility: &visibility, MessagerPolicy: &policy})
	if err != nil {
		t.Fatal(err)
	}
	if err = app.Ready(context.Background()); err != nil {
		t.Fatal("explicit backend schemas are required in disposable runner", err)
	}
	return app
}

func newBackendFixture(t *testing.T) backendFixture {
	t.Helper()
	db := database(t)
	keys := authorityKeys(t)
	now := &atomic.Int64{}
	now.Store(time.Now().UTC().Unix())
	config := authorityConfig(t, keys, now)
	f := backendFixture{db: db, keys: keys, config: config, actor: authorityUUID(t), other: authorityUUID(t)}
	for _, id := range []string{f.actor, f.other} {
		if err := authoritypg.SeedSynthetic(context.Background(), db, id, "synthetic_backend_"+strings.ReplaceAll(id, "-", ""), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		if err := authoritypg.New(db).ReplacePolicy(context.Background(), id, []authority.Rule{{Pattern: "*"}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	f.issuer, err = authority.New(authoritypg.New(db), config)
	if err != nil {
		t.Fatal(err)
	}
	f.token = pgIssue(t, f.issuer, f.actor).Token
	f.otherToken = pgIssue(t, f.issuer, f.other).Token
	f.app = backendApp(t, db, config)
	return f
}

func backendCall(t *testing.T, app http.Handler, method, path, token, body string, status int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s: status=%d body=%s", method, path, w.Code, w.Body.String())
	}
	return w
}

func backendDecode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestPGBackendDomainComposition(t *testing.T) {
	f := newBackendFixture(t)
	backendCall(t, f.app, "GET", "/readyz", "", "", 200)
	backendCall(t, f.app, "GET", "/.well-known/badges", "", "", 200)
	backendCall(t, f.app, "GET", "/wallet/wallets/exchange", f.token, "", 200)
	backendCall(t, f.app, "GET", "/wallet/wallet-products/catalog", "", "", 503)
	backendCall(t, f.app, "GET", "/wallet/wallets/exchange", "invalid", "", 401)
	backendCall(t, f.app, "GET", "/stargate/accounts/me", "", "", 401)
	me := backendDecode[profiles.Account](t, backendCall(t, f.app, "GET", "/stargate/accounts/me", f.token, "", 200))
	if me.ID != f.actor {
		t.Fatal("wrong account projection")
	}
	backendCall(t, f.app, "PATCH", "/passport/accounts/me", f.token, `{"bio":"composed-profile"}`, 200)
	profile := backendCall(t, f.app, "GET", "/api/accounts/"+f.actor, "", "", 200)
	if !bytes.Contains(profile.Body.Bytes(), []byte("composed-profile")) {
		t.Fatal(profile.Body.String())
	}

	item := authorityUUID(t)
	body := fmt.Sprintf(`[{"id":%q,"widget_key":"memo","payload":{"content":"composed-board"}}]`, item)
	backendCall(t, f.app, "PUT", "/passport/accounts/me/board", f.token, body, 200)
	rows := backendDecode[[]board.Item](t, backendCall(t, f.app, "GET", "/api/accounts/me/board", f.token, "", 200))
	if len(rows) != 1 || rows[0].ID != item {
		t.Fatal(rows)
	}
	otherRows := backendDecode[[]board.Item](t, backendCall(t, f.app, "GET", "/passport/accounts/me/board", f.otherToken, "", 200))
	if len(otherRows) != 0 {
		t.Fatal("cross-account board exposure")
	}

	query(t, f.db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at) VALUES($1,2,now())`, f.actor)
	pubName := "pub_" + strings.ReplaceAll(f.actor, "-", "")
	pub := backendDecode[profiles.Publisher](t, backendCall(t, f.app, "POST", "/sphere/publishers/individual", f.token, fmt.Sprintf(`{"name":%q,"nick":"Synthetic publisher"}`, pubName), 200))
	if pub.ID == "" || pub.ID == f.actor {
		t.Fatal("publisher identity collapsed into account")
	}
	tagName := "tag_" + strings.ReplaceAll(f.actor, "-", "")
	tag := backendCall(t, f.app, "POST", "/sphere/posts/tags?pub="+pub.ID, f.token, fmt.Sprintf(`{"slug":%q,"name":"Composed tag"}`, tagName), 201)
	if tag.Header().Get("Location") != "/sphere/posts/tags/"+tagName {
		t.Fatal(tag.Header())
	}
	var owner string
	if err := f.db.QueryRow(`SELECT owner_publisher_id::text FROM owned_taxonomy.post_tags WHERE slug=$1`, tagName).Scan(&owner); err != nil || owner != pub.ID {
		t.Fatal("taxonomy did not use canonical publisher", owner, err)
	}
	backendCall(t, f.app, "POST", "/api/posts/tags?pub="+pub.ID, f.otherToken, fmt.Sprintf(`{"slug":%q}`, tagName+"_other"), 403)

	draftBody := fmt.Sprintf(`{"content":"Composed draft","visibility":0,"type":0,"drafted_at":%q}`, time.Now().UTC().Format(time.RFC3339Nano))
	draft := backendDecode[posts.Post](t, backendCall(t, f.app, "POST", "/sphere/posts?pub="+pub.ID, f.token, draftBody, 200))
	if draft.PublisherID != pub.ID || draft.DraftedAt == nil {
		t.Fatal("post draft identity/state mismatch")
	}
	backendCall(t, f.app, "GET", "/api/posts/"+draft.ID, "", "", 404)
	backendCall(t, f.app, "GET", "/sphere/posts/"+draft.ID, f.otherToken, "", 404)
	backendCall(t, f.app, "POST", "/sphere/posts/"+draft.ID+"/publish", f.token, "", 200)
	backendCall(t, f.app, "GET", "/api/posts/"+draft.ID, "", "", 200)
	backendCall(t, f.app, "PATCH", "/sphere/posts/"+draft.ID, f.otherToken, `{"content":"wrong publisher"}`, 403)

	backendCall(t, f.app, "POST", "/stargate/relationships/"+f.other+"/friends", f.token, "", 200)
	backendCall(t, f.app, "POST", "/stargate/relationships/"+f.actor+"/friends/accept", f.otherToken, "", 200)
	backendCall(t, f.app, "PATCH", "/sphere/posts/"+draft.ID, f.token, `{"visibility":1}`, 200)
	backendCall(t, f.app, "GET", "/sphere/posts/"+draft.ID, f.otherToken, "", 200)
	backendCall(t, f.app, "GET", "/sphere/posts/"+draft.ID, "", "", 404)
	backendCall(t, f.app, "POST", "/sphere/posts/"+draft.ID+"/reactions", f.otherToken, `{"symbol":"heart","attitude":0}`, 200)
	backendCall(t, f.app, "POST", "/sphere/posts/"+draft.ID+"/bookmark", f.otherToken, "", 200)
	backendCall(t, f.app, "POST", "/sphere/publishers/"+pub.Name+"/collections", f.token, `{"slug":"composed"}`, 201)
	backendCall(t, f.app, "POST", "/sphere/publishers/"+pub.Name+"/collections/composed/posts", f.token, fmt.Sprintf(`{"post_id":%q}`, draft.ID), 204)
	backendCall(t, f.app, "GET", "/sphere/publishers/"+pub.Name+"/collections/composed/posts", f.otherToken, "", 200)
	query(t, f.db, `INSERT INTO owned_realm_quota(account_id,total,starts_at) VALUES($1,2,now())`, f.actor)
	realmRaw := backendCall(t, f.app, "POST", "/passport/realms", f.token, fmt.Sprintf(`{"slug":%q,"name":"Composed realm"}`, "realm_"+strings.ReplaceAll(f.actor, "-", "")), 200)
	var realm struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(realmRaw.Body.Bytes(), &realm); err != nil || realm.ID == "" {
		t.Fatal("realm projection", err)
	}
	realmWallet := backendDecode[wallet.Wallet](t, backendCall(t, f.app, "POST", "/wallet/wallets", f.token, fmt.Sprintf(`{"name":"Canonical realm wallet","realm_id":%q}`, realm.ID), 200))
	if realmWallet.RealmID == nil || *realmWallet.RealmID != realm.ID {
		t.Fatal("realm wallet owner")
	}
	backendCall(t, f.app, "GET", "/wallet/wallets/"+realmWallet.ID, f.otherToken, "", 403)

	w := backendDecode[wallet.Wallet](t, backendCall(t, f.app, "POST", "/wallet/wallets", f.token, `{"name":"Composed wallet"}`, 200))
	if w.AccountID == nil || *w.AccountID != f.actor {
		t.Fatal("wallet account mismatch")
	}
	backendCall(t, f.app, "GET", "/api/wallets/"+w.ID, f.otherToken, "", 403)
	backendCall(t, f.app, "GET", "/stargate/accounts/me/pin-status", f.token, "", 503)

	room, member := authorityUUID(t), authorityUUID(t)
	query(t, f.db, `INSERT INTO chat_rooms(id,is_public,encryption_mode) VALUES($1,false,77)`, room)
	query(t, f.db, `INSERT INTO chat_members(id,chat_room_id,account_id,notify,joined_at) VALUES($1,$2,$3,10,now())`, member, room, f.actor)
	backendCall(t, f.app, "POST", "/messager/chat/"+room+"/sync", f.token, `{"last_sync_timestamp":0}`, 200)
	backendCall(t, f.app, "POST", "/api/chat/"+room+"/sync", f.otherToken, `{"last_sync_timestamp":0}`, 403)
	backendCall(t, f.app, "POST", "/api/chat/"+room+"/sync", "", `{"last_sync_timestamp":0}`, 401)
	backendCall(t, f.app, "POST", "/develop/private/apps/"+authorityUUID(t)+"/board/payload", f.token, `{}`, 501)
	backendCall(t, f.app, "POST", "/stargate/auth/token", "", `{}`, 501)
}

func TestPGBackendFreshAuthorityAndIndependentPools(t *testing.T) {
	f := newBackendFixture(t)
	second := backendApp(t, database(t), f.config)
	backendCall(t, second, "GET", "/api/accounts/me/board", f.token, "", 200)
	repo := authoritypg.New(f.db)
	if err := repo.ReplacePolicy(context.Background(), f.actor, nil, nil); err != nil {
		t.Fatal(err)
	}
	backendCall(t, f.app, "PUT", "/passport/accounts/me/board", f.token, `[]`, 403)
	backendCall(t, second, "GET", "/api/accounts/me/board", f.token, "", 403)
	// Identity-only routes do not invent a profile-read or wallet-read grant.
	backendCall(t, second, "GET", "/stargate/accounts/me", f.token, "", 200)
	backendCall(t, second, "GET", "/wallet/wallets/all", f.token, "", 200)
	backendCall(t, f.app, "GET", "/api/accounts/"+f.actor, "invalid", "", 401)
	if err := repo.ReplacePolicy(context.Background(), f.actor, []authority.Rule{{Pattern: board.SelfManage}}, nil); err != nil {
		t.Fatal(err)
	}
	backendCall(t, second, "GET", "/api/accounts/me/board", f.token, "", 200)
	if err := f.issuer.InvalidateAccount(context.Background(), f.actor); err != nil {
		t.Fatal(err)
	}
	backendCall(t, f.app, "GET", "/stargate/accounts/me", f.token, "", 401)
	backendCall(t, second, "GET", "/wallet/wallets/all", f.token, "", 401)
	backendCall(t, second, "GET", "/api/accounts/me/board", f.otherToken, "", 200)
}

func TestPGBackendHTTPProcessRestart(t *testing.T) {
	f := newBackendFixture(t)
	public := filepath.Join(t.TempDir(), "synthetic-public.pem")
	if err := os.WriteFile(public, pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&f.config.SigningKey.PublicKey)}), 0600); err != nil {
		t.Fatal(err)
	}
	u := &url.URL{Scheme: "postgres", User: url.User("synthetic"), Host: "localhost:55432", Path: "/postgres"}
	q := url.Values{"host": {filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "socket")}, "sslmode": {"disable"}}
	u.RawQuery = q.Encode()
	t.Setenv("BACKEND_PROCESS_TEST_DSN", u.String())
	args := []string{"--serve", "--dsn-env=BACKEND_PROCESS_TEST_DSN", "--issuer=" + f.config.Issuer, "--audience=" + f.config.Audience, "--key-id=synthetic", "--trusted-public-key-file=" + public, "--taxonomy-public-visibility=0", "--messager-none-encryption=77", "--messager-notify-all=10", "--messager-notify-mentions=20", "--messager-notify-none=30"}
	mediaRoot := t.TempDir()
	args = append(args, "--media-root="+mediaRoot, "--media-max-bytes=1048576")
	query(t, f.db, `INSERT INTO owned_media_quotas(account_id,limit_bytes) VALUES($1,1048576)`, f.actor)
	headers := map[string]string{"Authorization": "Bearer " + f.token}
	p := startDomainProcess(t, "BACKEND_BINARY", "Authenticated backend listening on ", args...)
	p.call(t, "GET", "/readyz", "", 200, nil)
	p.call(t, "GET", "/.well-known/badges", "", 200, nil)
	p.call(t, "GET", "/wallet/wallets/exchange", "", 200, headers)
	p.call(t, "GET", "/passport/accounts/me/board", "", 401, nil)
	body := fmt.Sprintf(`[{"id":%q,"widget_key":"memo","payload":{"content":"backend-process-restart"}}]`, authorityUUID(t))
	p.call(t, "PUT", "/passport/accounts/me/board", body, 200, headers)
	p.call(t, "PATCH", "/passport/accounts/me", `{"bio":"process-profile-restart"}`, 200, headers)
	walletRaw, _ := p.call(t, "POST", "/wallet/wallets", `{"name":"Process wallet"}`, 200, headers)
	var savedWallet wallet.Wallet
	if err := json.Unmarshal(walletRaw, &savedWallet); err != nil || savedWallet.ID == "" {
		t.Fatal("wallet process response", err)
	}
	query(t, f.db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at) VALUES($1,2,now())`, f.actor)
	pubRaw, _ := p.call(t, "POST", "/sphere/publishers/individual", fmt.Sprintf(`{"name":%q,"nick":"Process publisher"}`, "process_"+strings.ReplaceAll(f.actor, "-", "")), 200, headers)
	var savedPublisher profiles.Publisher
	if err := json.Unmarshal(pubRaw, &savedPublisher); err != nil || savedPublisher.ID == "" {
		t.Fatal("publisher process response", err)
	}
	postRaw, _ := p.call(t, "POST", "/sphere/posts?pub="+savedPublisher.ID, `{"content":"process-post-restart","visibility":0,"type":0}`, 200, headers)
	var savedPost posts.Post
	if err := json.Unmarshal(postRaw, &savedPost); err != nil || savedPost.ID == "" {
		t.Fatal("post process response", err)
	}
	roomRaw, _ := p.call(t, "POST", "/messager/chat", `{"name":"Process room"}`, 200, headers)
	var savedRoom struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(roomRaw, &savedRoom); err != nil || savedRoom.ID == "" {
		t.Fatal("room process response", err)
	}
	p.call(t, "POST", "/messager/chat/"+savedRoom.ID+"/messages", `{"content":"process-chat-restart","client_message_id":"process-message"}`, 200, headers)
	otherHeaders := map[string]string{"Authorization": "Bearer " + f.otherToken}
	p.call(t, "POST", "/stargate/relationships/"+f.other+"/friends", "", 200, headers)
	p.call(t, "POST", "/stargate/relationships/"+f.actor+"/friends/accept", "", 200, otherHeaders)
	p.call(t, "POST", "/sphere/posts/"+savedPost.ID+"/bookmark", "", 200, otherHeaders)
	mediaBody := "process-media-restart"
	digest := sha256.Sum256([]byte(mediaBody))
	mediaHeaders := map[string]string{"Authorization": "Bearer " + f.token, "Content-Type": "text/plain", "X-File-Name": "fixture.txt", "X-Content-SHA256": fmt.Sprintf("%x", digest), "Idempotency-Key": "process-media"}
	fileRaw, _ := p.call(t, "POST", "/drive/media/files", mediaBody, 201, mediaHeaders)
	var savedFile struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(fileRaw, &savedFile); err != nil || savedFile.ID == "" {
		t.Fatal("media process projection", err)
	}
	p.stop()
	p = startDomainProcess(t, "BACKEND_BINARY", "Authenticated backend listening on ", args...)
	raw, _ := p.call(t, "GET", "/api/accounts/me/board", "", 200, headers)
	if !bytes.Contains(raw, []byte("backend-process-restart")) {
		t.Fatal("committed data lost across process restart")
	}
	fileAfter, _ := p.call(t, "GET", "/drive/files/"+savedFile.ID, "", 200, headers)
	if string(fileAfter) != mediaBody {
		t.Fatal("media bytes changed across process restart")
	}
	p.call(t, "GET", "/drive/files/"+savedFile.ID, "", 404, nil)
	bookmarkRaw, _ := p.call(t, "GET", "/sphere/posts/"+savedPost.ID+"/bookmark", "", 200, otherHeaders)
	if !bytes.Contains(bookmarkRaw, []byte(savedPost.ID)) {
		t.Fatal("bookmark lost across process restart")
	}
	p.call(t, "GET", "/stargate/relationships/"+f.actor, "", 200, otherHeaders)
	chatRaw, _ := p.call(t, "GET", "/messager/chat/"+savedRoom.ID+"/messages", "", 200, headers)
	if !bytes.Contains(chatRaw, []byte("process-chat-restart")) {
		t.Fatal("chat message lost across process restart")
	}
	p.call(t, "GET", "/messager/chat/"+savedRoom.ID+"/messages", "", 403, map[string]string{"Authorization": "Bearer " + f.otherToken})
	profileRaw, _ := p.call(t, "GET", "/api/accounts/"+f.actor, "", 200, nil)
	if !bytes.Contains(profileRaw, []byte("process-profile-restart")) {
		t.Fatal("profile lost across process restart")
	}
	walletAfter, _ := p.call(t, "GET", "/wallet/wallets/"+savedWallet.ID, "", 200, headers)
	if !bytes.Contains(walletAfter, []byte("Process wallet")) {
		t.Fatal("wallet lost across process restart")
	}
	p.call(t, "GET", "/wallet/wallets/"+savedWallet.ID, "", 403, map[string]string{"Authorization": "Bearer " + f.otherToken})
	postAfter, _ := p.call(t, "GET", "/sphere/posts/"+savedPost.ID, "", 200, nil)
	if !bytes.Contains(postAfter, []byte("process-post-restart")) {
		t.Fatal("post lost across process restart")
	}
	if err := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.actor, nil, nil); err != nil {
		t.Fatal(err)
	}
	p.call(t, "GET", "/passport/accounts/me/board", "", 403, headers)
	p.stop()
}

func TestPGBackendRestartPersistence(t *testing.T) {
	db := database(t)
	dir := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "backend-restart")
	if os.Getenv("PG_TEST_PHASE") == "before" {
		f := newBackendFixture(t)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"synthetic-signing.pem", "synthetic-hash.bin", "synthetic-seal.bin"} {
			b, err := os.ReadFile(filepath.Join(f.keys, name))
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, name), b, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "synthetic-token.txt"), []byte(f.token), 0600); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf(`[{"id":%q,"widget_key":"memo","payload":{"content":"backend-database-restart"}}]`, authorityUUID(t))
		backendCall(t, f.app, "PUT", "/passport/accounts/me/board", f.token, body, 200)
		backendCall(t, f.app, "POST", "/wallet/wallets", f.token, `{"name":"backend-database-restart"}`, 200)
		return
	}
	if os.Getenv("PG_TEST_PHASE") != "after" {
		t.Fatal("missing restart phase")
	}
	now := &atomic.Int64{}
	now.Store(time.Now().UTC().Unix())
	app := backendApp(t, db, authorityConfig(t, dir, now))
	token, err := os.ReadFile(filepath.Join(dir, "synthetic-token.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/accounts/me/board", "/wallet/wallets"} {
		result := backendCall(t, app, "GET", path, string(token), "", 200)
		if !bytes.Contains(result.Body.Bytes(), []byte("backend-database-restart")) {
			t.Fatal("state lost across PostgreSQL restart", path)
		}
	}
}
