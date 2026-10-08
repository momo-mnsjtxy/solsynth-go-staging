// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/mediastore"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/ticketmediapg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/tickets"
)

type ticketMediaFixture struct {
	ticketFixture
	app     *application.Backend
	objects string
}

func newTicketMediaFixture(t *testing.T) *ticketMediaFixture {
	t.Helper()
	f := &ticketMediaFixture{ticketFixture: newTicketFixture(t), objects: t.TempDir()}
	for _, actor := range []string{f.actor, f.other, f.admin} {
		rules := []authority.Rule{{Pattern: "tickets.create"}, {Pattern: "tickets.messages.create"}, {Pattern: "media.files.*"}}
		if actor == f.admin {
			rules = []authority.Rule{{Pattern: "*"}}
		}
		if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), actor, rules, nil); e != nil {
			t.Fatal(e)
		}
		query(t, f.db, `INSERT INTO owned_media_quotas(account_id,limit_bytes)VALUES($1::uuid,1048576)`, actor)
	}
	f.open(t)
	return f
}
func (f *ticketMediaFixture) open(t *testing.T) {
	t.Helper()
	if e := os.Chmod(f.objects, 0700); e != nil {
		t.Fatal(e)
	}
	store, e := mediastore.NewFilesystem(f.objects, 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { store.Close() })
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	f.app, e = application.NewPostgres(f.db, nil, application.Config{Authority: f.authorityConfig, Tickets: &f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, MediaStore: store, MaxMediaBytes: 1 << 20})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	f.repo.WithMedia(ticketmediapg.New(f.db, f.repo))
}
func (f *ticketMediaFixture) upload(t *testing.T, token, body string) string {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="file"; filename="synthetic-ticket.txt"`)
	header.Set("Content-Type", "text/plain")
	p, e := w.CreatePart(header)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = p.Write([]byte(body)); e != nil {
		t.Fatal(e)
	}
	if e = w.WriteField("usage", "ticket"); e != nil {
		t.Fatal(e)
	}
	if e = w.Close(); e != nil {
		t.Fatal(e)
	}
	req := httptest.NewRequest("POST", "/api/files/upload/direct", &b)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", w.FormDataContentType())
	out := httptest.NewRecorder()
	f.app.ServeHTTP(out, req)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	v := backendDecode[map[string]any](t, out)
	if v["usage"] != "ticket" || v["is_public"] != false {
		t.Fatal(v)
	}
	return v["id"].(string)
}
func (f *ticketMediaFixture) create(t *testing.T, file string) tickets.Ticket {
	t.Helper()
	return backendDecode[tickets.Ticket](t, backendJSONCall(t, f.app, "POST", "/passport/tickets", f.token, map[string]any{"title": "Synthetic media support", "content": "Private supporting file", "type": 0, "file_ids": []string{file}}, 200))
}
func ticketFileID(t *testing.T, m tickets.Message) string {
	t.Helper()
	if len(m.Files) != 1 {
		t.Fatal("expected one projected file", m.Files)
	}
	return m.Files[0].(map[string]any)["id"].(string)
}
func TestPGTicketMediaComposedLifecycleAndPrivateAudience(t *testing.T) {
	f := newTicketMediaFixture(t)
	file := f.upload(t, f.token, "private-ticket-evidence")
	for _, token := range []string{"", f.otherToken, f.adminToken} {
		backendCall(t, f.app, "GET", "/api/files/"+file, token, "", 404)
	}
	v := f.create(t, file)
	if len(v.Messages) != 1 || ticketFileID(t, v.Messages[0]) != file {
		t.Fatal(v)
	}
	for _, token := range []string{f.token, f.adminToken} {
		view := backendDecode[tickets.Ticket](t, backendCall(t, f.app, "GET", "/api/tickets/"+v.ID, token, "", 200))
		if ticketFileID(t, view.Messages[0]) != file {
			t.Fatal(view)
		}
		body := backendCall(t, f.app, "GET", "/api/files/"+file, token, "", 200)
		if body.Body.String() != "private-ticket-evidence" || body.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal(body.Header(), body.Body.String())
		}
	}
	backendCall(t, f.app, "GET", "/api/files/"+file, "", "", 404)
	backendCall(t, f.app, "GET", "/api/tickets/"+v.ID, f.otherToken, "", 404)
	backendJSONCall(t, f.app, "POST", "/api/tickets/"+v.ID+"/assign", f.adminToken, map[string]any{"assignee_id": f.other}, 200)
	backendCall(t, f.app, "GET", "/api/files/"+file, f.otherToken, "", 200)
	backendJSONCall(t, f.app, "POST", "/api/tickets/"+v.ID+"/messages", f.otherToken, map[string]any{"content": "Assigned reader cannot reply", "file_ids": []string{file}}, 403)
	staffFile := f.upload(t, f.adminToken, "private-staff-response")
	message := backendDecode[tickets.Message](t, backendJSONCall(t, f.app, "POST", "/api/tickets/"+v.ID+"/messages", f.adminToken, map[string]any{"content": "Staff reply", "file_ids": []string{staffFile}}, 200))
	if ticketFileID(t, message) != staffFile {
		t.Fatal(message)
	}
	backendCall(t, f.app, "GET", "/api/files/"+staffFile, f.token, "", 200)
	backendJSONCall(t, f.app, "POST", "/api/tickets", f.token, map[string]any{"title": "Forbidden redistribution", "content": "Cannot redistribute staff file", "file_ids": []string{staffFile}}, 403)
	backendJSONCall(t, f.app, "POST", "/api/tickets/"+v.ID+"/assign", f.adminToken, map[string]any{"assignee_id": nil}, 200)
	backendCall(t, f.app, "GET", "/api/files/"+file, f.otherToken, "", 404)
	if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.admin, []authority.Rule{{Pattern: "media.files.*"}, {Pattern: "tickets.messages.create"}}, nil); e != nil {
		t.Fatal(e)
	}
	backendCall(t, f.app, "GET", "/api/tickets/"+v.ID, f.adminToken, "", 404)
	backendCall(t, f.app, "GET", "/api/files/"+file, f.adminToken, "", 404)
	backendCall(t, f.app, "GET", "/api/files/"+staffFile, f.adminToken, "", 200)
	if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.admin, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind)VALUES($1::uuid,'disable_account')`, f.actor)
	backendCall(t, f.app, "GET", "/api/tickets/"+v.ID, f.adminToken, "", 404)
	backendCall(t, f.app, "GET", "/api/files/"+file, f.adminToken, "", 404)
	query(t, f.db, `DELETE FROM owned_authority_punishments WHERE account_id=$1::uuid AND kind='disable_account'`, f.actor)
	backendCall(t, f.app, "DELETE", "/api/files/"+staffFile, f.adminToken, "", 200)
	view := backendDecode[tickets.Ticket](t, backendCall(t, f.app, "GET", "/api/tickets/"+v.ID, f.token, "", 200))
	for _, m := range view.Messages {
		if m.ID == message.ID && len(m.Files) != 0 {
			t.Fatal("deleted file still projected", view)
		}
	}
	backendCall(t, f.app, "DELETE", "/api/tickets/"+v.ID, f.adminToken, "", 204)
	backendCall(t, f.app, "GET", "/api/files/"+file, f.adminToken, "", 404)
	backendCall(t, f.app, "GET", "/api/files/"+file, f.token, "", 200)
	if n := ticketCount(t, f.ticketFixture, `SELECT count(*) FROM owned_ticket_media l JOIN owned_ticket_messages m ON m.id=l.message_id WHERE m.ticket_id=$1::uuid`, v.ID); n != 0 {
		t.Fatal("ticket deletion retained grants", n)
	}
}

func TestPGTicketMediaOwnershipIdempotencyAndAtomicRollback(t *testing.T) {
	f := newTicketMediaFixture(t)
	file := f.upload(t, f.token, "owned file")
	foreign := f.upload(t, f.otherToken, "foreign file")
	query(t, f.db, `UPDATE owned_media_files SET is_public=true WHERE id=$1::uuid`, foreign)
	backendJSONCall(t, f.app, "POST", "/api/tickets", f.token, map[string]any{"title": "Foreign source", "content": "Must roll back", "file_ids": []string{foreign}}, 403)
	v := f.create(t, file)
	for _, ids := range [][]string{{file, file}, {file, strings.ToUpper(file)}, {"https://example.invalid/file"}} {
		backendJSONCall(t, f.app, "POST", "/api/tickets/"+v.ID+"/messages", f.token, map[string]any{"content": "invalid IDs", "file_ids": ids}, 400)
	}
	body, _ := json.Marshal(map[string]any{"content": "Idempotent attachment", "file_ids": []string{file}})
	first := backendDecode[tickets.Message](t, ticketRequest(t, f.app, "POST", "/api/tickets/"+v.ID+"/messages", f.token, "message-once", string(body), 200))
	replay := backendDecode[tickets.Message](t, ticketRequest(t, f.app, "POST", "/api/tickets/"+v.ID+"/messages", f.token, "message-once", string(body), 200))
	if replay.ID != first.ID || ticketFileID(t, replay) != file {
		t.Fatal(replay)
	}
	ticketRequest(t, f.app, "POST", "/api/tickets/"+v.ID+"/messages", f.token, "message-once", `{"content":"Idempotent attachment","file_ids":[]}`, 409)
	constraint := "ticket_media_rollback_" + strings.ReplaceAll(f.actor, "-", "")
	query(t, f.db, `ALTER TABLE owned_ticket_exam_audit ADD CONSTRAINT `+constraint+` CHECK(actor_id<>'`+f.actor+`'::uuid) NOT VALID`)
	t.Cleanup(func() { f.db.Exec(`ALTER TABLE owned_ticket_exam_audit DROP CONSTRAINT IF EXISTS ` + constraint) })
	backendJSONCall(t, f.app, "POST", "/api/tickets/"+v.ID+"/messages", f.token, map[string]any{"content": "Failed reference", "file_ids": []string{file}}, 503)
	query(t, f.db, `ALTER TABLE owned_ticket_exam_audit DROP CONSTRAINT `+constraint)
	if n := ticketCount(t, f.ticketFixture, `SELECT count(*) FROM owned_ticket_messages WHERE ticket_id=$1::uuid`, v.ID); n != 2 {
		t.Fatal("message escaped rollback", n)
	}
	if n := ticketCount(t, f.ticketFixture, `SELECT count(*) FROM owned_ticket_media l JOIN owned_ticket_messages m ON m.id=l.message_id WHERE m.ticket_id=$1::uuid`, v.ID); n != 2 {
		t.Fatal("link escaped rollback", n)
	}
	backendJSONCall(t, f.app, "POST", "/api/tickets/"+v.ID+"/messages", f.token, map[string]any{"content": "", "file_ids": []string{file}}, 400)
	backendJSONCall(t, f.app, "POST", "/api/tickets/"+v.ID+"/status", f.adminToken, map[string]any{"status": 4}, 501)
}

func TestPGTicketMediaFileWaitRevalidatesStateAndAuthority(t *testing.T) {
	for _, change := range []string{"deleted", "expired"} {
		t.Run(change, func(t *testing.T) {
			f := newTicketMediaFixture(t)
			file := f.upload(t, f.token, "wait fixture")
			v := f.create(t, file)
			ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			blocker, pid := atomicBlocker(t, locks, "owned_media_files", file)
			if change == "deleted" {
				if _, e := blocker.Exec(`UPDATE owned_media_files SET state='deleting' WHERE id=$1::uuid`, file); e != nil {
					t.Fatal(e)
				}
			}
			done := make(chan error, 1)
			go func() {
				_, e := f.repo.Execute(ctx, f.actor, tickets.Command{Operation: "ticket.message", ID: v.ID, Message: &tickets.MessageInput{Content: "Never commits", FileIDs: []string{file}}})
				done <- e
			}()
			// Use the composed repository with the same verified transaction guard.
			waitAtomicBlocked(t, locks, pid, "owned_media_files")
			if change == "expired" {
				f.clock.Add(601)
			}
			if e := blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			want := error(tickets.ErrNotFound)
			if change == "expired" {
				want = authority.ErrExpired
			}
			if e := <-done; !errors.Is(e, want) {
				t.Fatal(change, e)
			}
			if n := ticketCount(t, f.ticketFixture, `SELECT count(*) FROM owned_ticket_messages WHERE ticket_id=$1::uuid`, v.ID); n != 1 {
				t.Fatal("waiter committed", n)
			}
		})
	}
}

func TestPGTicketMediaReadWaitObservesAssignmentWithdrawal(t *testing.T) {
	f := newTicketMediaFixture(t)
	file := f.upload(t, f.token, "private waiting read")
	v := f.create(t, file)
	backendJSONCall(t, f.app, "POST", "/api/tickets/"+v.ID+"/assign", f.adminToken, map[string]any{"assignee_id": f.other}, 200)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
	blocker, pid := atomicBlocker(t, locks, "owned_media_files", file)
	done := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		f.app.ServeHTTP(w, fixtureNoBodyRequest("GET", "/api/files/"+file, f.otherToken).WithContext(ctx))
		done <- w.Code
	}()
	waitAtomicBlocked(t, locks, pid, "owned_media_files")
	backendJSONCall(t, f.app, "POST", "/api/tickets/"+v.ID+"/assign", f.adminToken, map[string]any{"assignee_id": nil}, 200)
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if code := <-done; code != 404 {
		t.Fatal("stale assignment granted bytes", code)
	}
}

func TestPGTicketMediaRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	_ = database(t)
	root := os.Getenv("SOLSYNTH_DISPOSABLE_PG")
	objects := filepath.Join(root, "ticket-media-objects")
	record := filepath.Join(root, "ticket-media.json")
	type saved struct{ Actor, Ticket, File string }
	if phase == "before" {
		f := newTicketMediaFixture(t)
		if e := os.Mkdir(objects, 0700); e != nil {
			t.Fatal(e)
		}
		f.objects = objects
		f.open(t)
		file := f.upload(t, f.token, "durable ticket evidence")
		v := f.create(t, file)
		b, e := json.Marshal(saved{f.actor, v.ID, file})
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
	f := newTicketMediaFixture(t)
	f.objects = objects
	f.open(t)
	v := backendDecode[tickets.Ticket](t, backendCall(t, f.app, "GET", "/api/tickets/"+state.Ticket, f.adminToken, "", 200))
	if len(v.Messages) != 1 || ticketFileID(t, v.Messages[0]) != state.File {
		t.Fatal(v)
	}
	if w := backendCall(t, f.app, "GET", "/api/files/"+state.File, f.adminToken, "", 200); w.Body.String() != "durable ticket evidence" {
		t.Fatal(w.Body.String())
	}
	backendCall(t, f.app, "GET", "/api/files/"+state.File, "", "", 404)
}
