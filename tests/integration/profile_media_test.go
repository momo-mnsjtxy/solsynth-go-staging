// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/mediastore"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/postmediapg"
	"solsynth.local/go-migration/internal/profilemediapg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/posts"
	"strings"
	"testing"
	"time"
)

type profileMediaFixture struct {
	settingsFixture
	app         *application.Backend
	objects     string
	objectStore *mediastore.Filesystem
	other       authority.Account
	otherToken  string
}

func newProfileMediaFixture(t *testing.T) *profileMediaFixture {
	t.Helper()
	f := &profileMediaFixture{settingsFixture: newSettingsFixture(t), objects: t.TempDir()}
	f.other, _ = f.register(t, true)
	for _, id := range []string{f.actor.ID, f.other.ID} {
		if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, id, []authority.Rule{{Pattern: "accounts.manage"}, {Pattern: "account.contacts.manage"}, {Pattern: "media.files.*"}}, nil); e != nil {
			t.Fatal(e)
		}
		query(t, f.db, `INSERT INTO owned_media_quotas(account_id,limit_bytes)VALUES($1::uuid,1048576)`, id)
	}
	f.otherToken = f.login(t, f.other.Name, syntheticPassword).Token
	profileMediaApp(t, f)
	return f
}
func profileMediaApp(t *testing.T, f *profileMediaFixture) {
	t.Helper()
	var e error
	if e = os.Chmod(f.objects, 0700); e != nil {
		t.Fatal(e)
	}
	f.objectStore, e = mediastore.NewFilesystem(f.objects, 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	store := f.objectStore
	t.Cleanup(func() { store.Close() })
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	f.app, e = application.NewPostgres(f.db, nil, application.Config{Authority: authorityConfig(t, f.keys, f.now), SessionService: f.issuer, Signin: &f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, MediaStore: f.objectStore, MaxMediaBytes: 1 << 20})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.app.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
}
func profilePNG(t *testing.T) []byte {
	t.Helper()
	b, e := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func profileUpload(t *testing.T, h http.Handler, token, typ string, body []byte) string {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="file"; filename="synthetic-profile.png"`)
	header.Set("Content-Type", typ)
	part, e := w.CreatePart(header)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = part.Write(body); e != nil {
		t.Fatal(e)
	}
	if e = w.WriteField("usage", "profile_picture"); e != nil {
		t.Fatal(e)
	}
	if e = w.Close(); e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("POST", "/api/files/upload/direct", &b)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", w.FormDataContentType())
	out := httptest.NewRecorder()
	h.ServeHTTP(out, r)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	v := backendDecode[map[string]any](t, out)
	id, ok := v["id"].(string)
	if !ok || v["is_public"] != false || v["usage"] != "profile_picture" {
		t.Fatal(v)
	}
	return id
}
func profileView(t *testing.T, f *profileMediaFixture, token string) accounts.Account {
	t.Helper()
	return backendDecode[accounts.Account](t, backendCall(t, f.app, "GET", "/stargate/accounts/"+f.actor.Name, token, "", 200))
}
func TestPGProfileMediaComposedUploadReferenceAndRemoval(t *testing.T) {
	f := newProfileMediaFixture(t)
	png := profilePNG(t)
	picture := profileUpload(t, f.app, f.token, "image/png", png)
	background := profileUpload(t, f.app, f.token, "image/png", png)
	backendCall(t, f.app, "GET", "/api/files/"+picture, "", "", 404)
	own := backendDecode[accounts.Account](t, backendJSONCall(t, f.app, "PATCH", "/stargate/accounts/me/profile", f.token, map[string]any{"picture_id": picture, "background_id": background, "bio": "Synthetic public profile", "birthday": "2000-01-02T00:00:00Z"}, 200))
	if own.Profile == nil || own.Profile.Picture["id"] != picture || own.Profile.Background["id"] != background || own.Profile.Birthday == nil {
		t.Fatal(own)
	}
	public := profileView(t, f, "")
	if public.Profile.Picture["id"] != picture || public.Profile.Background["id"] != background || public.Profile.Birthday != nil || len(public.Contacts) != 0 {
		t.Fatal("public projection", public)
	}
	for _, token := range []string{"", f.otherToken} {
		w := backendCall(t, f.app, "GET", "/api/files/"+picture+".png?original=true", token, "", 200)
		if !bytes.Equal(w.Body.Bytes(), png) || w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("profile bytes", w.Header(), w.Body.Len())
		}
	}
	contact := secondaryEmail(t, f.settingsFixture, "profile-public")
	verifySecondary(t, f.settingsFixture, contact)
	if len(profileView(t, f, "").Contacts) != 0 {
		t.Fatal("private contact disclosed")
	}
	backendCall(t, f.app, "POST", "/stargate/contacts/"+contact.ID+"/public", f.token, "", 200)
	if len(profileView(t, f, "").Contacts) != 1 {
		t.Fatal("public verified contact missing")
	}
	backendCall(t, f.app, "DELETE", "/stargate/contacts/"+contact.ID+"/public", f.token, "", 200)
	if len(profileView(t, f, "").Contacts) != 0 {
		t.Fatal("contact opt-out ignored")
	}
	// Omission retains the reference; explicit null is the owned removal extension.
	backendJSONCall(t, f.app, "PATCH", "/stargate/accounts/me/profile", f.token, map[string]any{"bio": "updated"}, 200)
	if profileView(t, f, "").Profile.Picture["id"] != picture {
		t.Fatal("omission removed reference")
	}
	// Replacing the picture withdraws its former profile grant. A file used by
	// both slots remains visible until the final reference is removed.
	backendJSONCall(t, f.app, "PATCH", "/stargate/accounts/me/profile", f.token, map[string]any{"picture_id": background}, 200)
	if a := profileView(t, f, ""); a.Profile.Picture["id"] != background || a.Profile.Background["id"] != background {
		t.Fatal("replacement did not persist", a)
	}
	backendCall(t, f.app, "GET", "/api/files/"+picture, "", "", 404)
	backendJSONCall(t, f.app, "PATCH", "/stargate/accounts/me/profile", f.token, map[string]any{"picture_id": nil}, 200)
	if a := profileView(t, f, ""); a.Profile.Picture != nil || a.Profile.Background["id"] != background {
		t.Fatal(a)
	}
	backendCall(t, f.app, "GET", "/api/files/"+picture, "", "", 404)
	backendCall(t, f.app, "GET", "/api/files/"+picture, f.token, "", 200)
	backendCall(t, f.app, "GET", "/api/files/"+background, "", "", 200)
	var visible bool
	if e := f.db.QueryRow(`SELECT is_public FROM owned_media_files WHERE id=$1::uuid`, background).Scan(&visible); e != nil || visible {
		t.Fatal("reference changed file public flag", visible, e)
	}
	backendCall(t, f.app, "DELETE", "/api/files/"+background, f.token, "", 200)
	if profileView(t, f, "").Profile.Background != nil {
		t.Fatal("deleted source projected")
	}
	backendCall(t, f.app, "GET", "/api/files/"+background, "", "", 404)
}

func TestPGProfileMediaOwnershipAtomicityAndCurrentPrivacy(t *testing.T) {
	f := newProfileMediaFixture(t)
	png := profilePNG(t)
	mine := profileUpload(t, f.app, f.token, "image/png", png)
	foreign := profileUpload(t, f.app, f.otherToken, "image/png", png)
	backendJSONCall(t, f.app, "PATCH", "/stargate/accounts/me/profile", f.token, map[string]any{"picture_id": mine, "bio": "original"}, 200)
	backendJSONCall(t, f.app, "PATCH", "/stargate/accounts/me/profile", f.token, map[string]any{"picture_id": foreign, "bio": "must roll back"}, 403)
	if a := profileView(t, f, ""); a.Profile.Bio != "original" || a.Profile.Picture["id"] != mine {
		t.Fatal("partial write", a)
	}
	query(t, f.db, `UPDATE owned_media_files SET is_public=true WHERE id=$1::uuid`, foreign)
	backendJSONCall(t, f.app, "PATCH", "/stargate/accounts/me/profile", f.token, map[string]any{"background_id": foreign}, 403)
	text := profileUpload(t, f.app, f.token, "text/plain", []byte("synthetic text"))
	backendJSONCall(t, f.app, "PATCH", "/stargate/accounts/me/profile", f.token, map[string]any{"picture_id": text}, 400)
	backendJSONCall(t, f.app, "PATCH", "/stargate/accounts/me/profile", f.token, map[string]any{"picture_id": authorityUUID(t)}, 404)
	for _, payload := range []string{`{"picture_id":"https://example.invalid/image.png"}`, `{"picture_id":""}`, `{"picture_id":null,"picture_id":null}`} {
		backendCall(t, f.app, "PATCH", "/stargate/accounts/me/profile", f.token, payload, 400)
	}
	// A constraint failure after reference mutation must roll back profile and link.
	constraint := "profile_media_rollback_" + strings.ReplaceAll(f.actor.ID, "-", "")
	query(t, f.db, `ALTER TABLE owned_accounts_audit ADD CONSTRAINT `+constraint+` CHECK(actor_id<>'`+f.actor.ID+`'::uuid)NOT VALID`)
	t.Cleanup(func() { f.db.Exec(`ALTER TABLE owned_accounts_audit DROP CONSTRAINT IF EXISTS ` + constraint) })
	backendJSONCall(t, f.app, "PATCH", "/stargate/accounts/me/profile", f.token, map[string]any{"picture_id": nil, "bio": "failed"}, 503)
	query(t, f.db, `ALTER TABLE owned_accounts_audit DROP CONSTRAINT `+constraint)
	if a := profileView(t, f, ""); a.Profile.Bio != "original" || a.Profile.Picture["id"] != mine {
		t.Fatal("rollback lost link", a)
	}
	relation := socialpg.New(f.db)
	if _, e := relation.ChangeRelationship(f.ctx, f.actor.ID, f.other.ID, social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	backendCall(t, f.app, "GET", "/stargate/accounts/"+f.actor.Name, f.otherToken, "", 404)
	backendCall(t, f.app, "GET", "/api/files/"+mine, f.otherToken, "", 404)
	// Public profiles remain public to anonymous requests; blocks are enforced for
	// a supplied verified viewer rather than inventing an account-private mode.
	backendCall(t, f.app, "GET", "/api/files/"+mine, "", "", 200)
	backendCall(t, f.app, "GET", "/stargate/accounts/"+f.actor.Name, "invalid", "", 401)
	// An independent public flag cannot bypass canonical owner disable punishment.
	query(t, f.db, `UPDATE owned_media_files SET is_public=true WHERE id=$1::uuid`, mine)
	query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind)VALUES($1::uuid,'disable_account')`, f.actor.ID)
	backendCall(t, f.app, "GET", "/stargate/accounts/"+f.actor.Name, "", "", 404)
	backendCall(t, f.app, "GET", "/api/files/"+mine, "", "", 404)
	query(t, f.db, `DELETE FROM owned_authority_punishments WHERE account_id=$1::uuid`, f.actor.ID)
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1::uuid`, f.actor.ID)
	backendCall(t, f.app, "GET", "/stargate/accounts/"+f.actor.Name, "", "", 404)
	backendCall(t, f.app, "GET", "/api/files/"+mine, "", "", 404)
}

func TestPGProfileMediaAuthorityAndFileLockWait(t *testing.T) {
	f := newProfileMediaFixture(t)
	id := profileUpload(t, f.app, f.token, "image/png", profilePNG(t))
	p, e := f.verifier.Authenticate(f.ctx, f.token)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(f.db), f.verifier)
	repo := accountpg.NewWithAuthority(f.db, guard).WithProfileMedia(profilemediapg.New(f.db, nil))
	ctx, cancel := context.WithTimeout(guard.Bind(f.ctx, p), 10*time.Second)
	defer cancel()
	locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
	blocker, pid := atomicBlocker(t, locks, "owned_authority_accounts", f.actor.ID)
	if _, e = blocker.ExecContext(ctx, `DELETE FROM owned_authority_grants WHERE account_id=$1::uuid`, f.actor.ID); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := repo.UpdateProfile(ctx, f.actor.ID, accounts.ProfilePatch{PictureID: &id}); done <- e }()
	waitAtomicBlocked(t, locks, pid, "owned_authority_accounts")
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, authority.ErrForbidden) {
		t.Fatal("stale authority", e)
	}
	if e = authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor.ID, []authority.Rule{{Pattern: "accounts.manage"}, {Pattern: "media.files.*"}}, nil); e != nil {
		t.Fatal(e)
	}
	blocker, pid = atomicBlocker(t, locks, "owned_media_files", id)
	if _, e = blocker.ExecContext(ctx, `UPDATE owned_media_files SET state='deleting' WHERE id=$1::uuid`, id); e != nil {
		t.Fatal(e)
	}
	go func() { _, e := repo.UpdateProfile(ctx, f.actor.ID, accounts.ProfilePatch{PictureID: &id}); done <- e }()
	waitAtomicBlocked(t, locks, pid, "owned_media_files")
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, accounts.ErrNotFound) {
		t.Fatal("stale source state", e)
	}
	var count int
	if e = f.db.QueryRow(`SELECT count(*) FROM owned_account_profile_media WHERE account_id=$1::uuid`, f.actor.ID).Scan(&count); e != nil || count != 0 {
		t.Fatal(count, e)
	}
	query(t, f.db, `UPDATE owned_media_files SET state='ready' WHERE id=$1::uuid`, id)
	blocker, pid = atomicBlocker(t, locks, "owned_media_files", id)
	go func() { _, e := repo.UpdateProfile(ctx, f.actor.ID, accounts.ProfilePatch{PictureID: &id}); done <- e }()
	waitAtomicBlocked(t, locks, pid, "owned_media_files")
	f.now.Add(int64((11 * time.Minute) / time.Second))
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, authority.ErrExpired) {
		t.Fatal("expired proof committed after media wait", e)
	}
	if e = f.db.QueryRow(`SELECT count(*) FROM owned_account_profile_media WHERE account_id=$1::uuid`, f.actor.ID).Scan(&count); e != nil || count != 0 {
		t.Fatal("expired proof retained a reference", count, e)
	}
}

func TestPGProfileMediaReadWaitObservesCurrentVisibility(t *testing.T) {
	for _, change := range []string{"remove", "disable", "punish"} {
		t.Run(change, func(t *testing.T) {
			f := newProfileMediaFixture(t)
			id := profileUpload(t, f.app, f.token, "image/png", profilePNG(t))
			backendJSONCall(t, f.app, "PATCH", "/stargate/accounts/me/profile", f.token, map[string]any{"picture_id": id}, 200)
			ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			blocker, pid := atomicBlocker(t, locks, "owned_media_files", id)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				w := httptest.NewRecorder()
				f.app.ServeHTTP(w, httptest.NewRequest("GET", "/api/files/"+id, nil).WithContext(ctx))
				done <- w
			}()
			waitAtomicBlocked(t, locks, pid, "owned_media_files")
			switch change {
			case "remove":
				backendJSONCall(t, f.app, "PATCH", "/stargate/accounts/me/profile", f.token, map[string]any{"picture_id": nil}, 200)
			case "disable":
				query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1::uuid`, f.actor.ID)
			case "punish":
				query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind)VALUES($1::uuid,'disable_account')`, f.actor.ID)
			}
			if e := blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if w := <-done; w.Code != 404 {
				t.Fatal("stale profile audience after media wait", w.Code, w.Body.String())
			}
		})
	}
}

func TestPGProfileMediaMembershipCannotRedistribute(t *testing.T) {
	f := newRealmPublishingFixture(t)
	upload := mediaUpload("profile-realm-source", string(profilePNG(t)), false)
	upload.Name = "synthetic.png"
	upload.MIMEType = "image/png"
	file, _, e := f.repo.Upload(f.ctx, f.actor, upload, bytes.NewReader(profilePNG(t)))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.posts.Create(f.ctx, f.actor, f.privatePub.ID, posts.Patch{Content: postString("synthetic realm picture"), Attachments: mediaIDs(file.ID)}); e != nil {
		t.Fatal(e)
	}
	links := profilemediapg.New(f.db, postmediapg.New(f.db, f.interactions).WithVisibility(f.social))
	f.repo.WithReadVisibility(links)
	mediaRead(t, f.repo, f.otherCtx, f.other, file.ID, string(profilePNG(t)))
	repo := accountpg.NewWithAuthority(f.db, f.guard).WithProfileMedia(links)
	if _, e = repo.UpdateProfile(f.otherCtx, f.other, accounts.ProfilePatch{PictureID: &file.ID}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("membership became ownership", e)
	}
	if _, e = f.social.ChangeMember(f.ctx, f.actor, f.private.ID, social.MemberChange{Action: social.Remove, AccountID: f.other}); e != nil {
		t.Fatal(e)
	}
	if _, body, e := f.repo.Open(f.otherCtx, f.other, file.ID); !errors.Is(e, media.ErrNotFound) || body != nil {
		if body != nil {
			body.Close()
		}
		t.Fatal("revoked membership retained bytes", e)
	}
}

func TestPGProfileMediaRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	root := os.Getenv("SOLSYNTH_DISPOSABLE_PG")
	objects := filepath.Join(root, "profile-media-objects")
	record := filepath.Join(root, "profile-media-record.json")
	type saved struct{ Account, File string }
	if phase == "before" {
		f := newProfileMediaFixture(t)
		if e := os.Mkdir(objects, 0700); e != nil {
			t.Fatal(e)
		}
		f.objects = objects
		profileMediaApp(t, f)
		id := profileUpload(t, f.app, f.token, "image/png", profilePNG(t))
		backendJSONCall(t, f.app, "PATCH", "/stargate/accounts/me/profile", f.token, map[string]any{"picture_id": id, "birthday": "2000-01-02T00:00:00Z"}, 200)
		b, e := json.Marshal(saved{f.actor.ID, id})
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(record, b, 0600); e != nil {
			t.Fatal(e)
		}
		return
	}
	if phase != "after" {
		t.Fatal("explicit restart phase required")
	}
	b, e := os.ReadFile(record)
	if e != nil {
		t.Fatal(e)
	}
	var state saved
	if e = json.Unmarshal(b, &state); e != nil {
		t.Fatal(e)
	}
	repo := accountpg.New(db).WithProfileMedia(profilemediapg.New(db, nil))
	account, e := repo.LookupAccount(context.Background(), state.Account)
	if e != nil || account.Profile == nil || account.Profile.Picture["id"] != state.File || account.Profile.Birthday != nil {
		t.Fatal(account, e)
	}
	contents, e := os.ReadFile(filepath.Join(objects, state.File))
	if e != nil || !bytes.Equal(contents, profilePNG(t)) {
		t.Fatal("durable profile bytes", e)
	}
	// Re-open the same canonical file through the media service after restart,
	// using the existing synthetic fixture's fresh verifier and filesystem.
	mediaBase := newMediaFixture(t)
	mediaBase.root = objects
	mediaBase = mediaPool(t, mediaBase, db)
	mediaBase.repo.WithReadVisibility(profilemediapg.New(db, nil))
	mediaRead(t, mediaBase.repo, context.Background(), "", state.File, string(profilePNG(t)))
}
