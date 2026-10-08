// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/ports/messager/rooms"
)

func mlsDeviceName(f chatFixture, name string) string { return name + "-" + f.owner }
func mlsInt(n int64) *int64                           { return &n }
func mlsBytes(s string) string {
	return base64.StdEncoding.EncodeToString([]byte("SYNTHETIC-OPAQUE:" + s))
}
func mlsFixture(t *testing.T) (chatFixture, rooms.Room, string) {
	t.Helper()
	f := newChatFixture(t)
	query(t, f.db, chatpg.MLSSchema)
	f.repo.WithMLSDelivery()
	room := createChat(t, f, true)
	chatJoin(t, f, room.ID, f.other)
	mlsDo(t, f, f.owner, mlsDeviceName(f, "device-owner"), "publish", "", rooms.MLSRequest{DeviceID: mlsDeviceName(f, "device-owner"), KeyPackage: mlsBytes("owner-package")})
	mlsDo(t, f, f.other, mlsDeviceName(f, "device-other"), "publish", "", rooms.MLSRequest{DeviceID: mlsDeviceName(f, "device-other"), KeyPackage: mlsBytes("other-package")})
	mlsDo(t, f, f.third, mlsDeviceName(f, "device-stranger"), "publish", "", rooms.MLSRequest{DeviceID: mlsDeviceName(f, "device-stranger"), KeyPackage: mlsBytes("stranger-package")})
	enabled := mlsDo(t, f, f.owner, "", "enable", room.ID, rooms.MLSRequest{}).(rooms.Room)
	var storedTime time.Time
	if e := f.db.QueryRow(`SELECT owned_updated_at FROM chat_rooms WHERE id=$1`, room.ID).Scan(&storedTime); e != nil || !storedTime.Equal(enabled.UpdatedAt) {
		t.Fatal("enable timestamp projection", storedTime, enabled.UpdatedAt, e)
	}
	gid := "room:" + room.ID
	mlsDo(t, f, f.owner, mlsDeviceName(f, "device-owner"), "bootstrap", gid, rooms.MLSRequest{Epoch: mlsInt(0), StateVersion: mlsInt(1), Meta: map[string]string{"bootstrap_device_id": mlsDeviceName(f, "device-owner")}})
	return f, room, gid
}
func mlsDo(t *testing.T, f chatFixture, actor, device, op, target string, p rooms.MLSRequest) any {
	t.Helper()
	v, e := f.repo.MLS(context.Background(), actor, device, op, target, p)
	if e != nil {
		t.Fatalf("MLS %s: %v", op, e)
	}
	return v
}
func mlsFail(t *testing.T, f chatFixture, actor, device, op, target string, p rooms.MLSRequest, want error) {
	t.Helper()
	_, e := f.repo.MLS(context.Background(), actor, device, op, target, p)
	if !errors.Is(e, want) {
		t.Fatalf("MLS %s: got %v want %v", op, e, want)
	}
}
func mlsCommitReq(epoch int64, key string) rooms.MLSRequest {
	return rooms.MLSRequest{Epoch: mlsInt(epoch), Ciphertext: mlsBytes("commit-" + key), Header: mlsBytes("header"), ClientMessageID: key}
}
func mlsExternalJoin(t *testing.T, f chatFixture, room rooms.Room, gid string) {
	t.Helper()
	mlsDo(t, f, f.owner, mlsDeviceName(f, "device-owner"), "groupinfo-put", gid, rooms.MLSRequest{Epoch: mlsInt(0), GroupInfo: mlsBytes("group-info"), RatchetTree: mlsBytes("tree")})
	mlsDo(t, f, f.other, mlsDeviceName(f, "device-other"), "groupinfo", gid, rooms.MLSRequest{})
	mlsDo(t, f, f.other, mlsDeviceName(f, "device-other"), "commit-fanout", gid, mlsCommitReq(1, "external-join"))
	mlsDo(t, f, f.other, mlsDeviceName(f, "device-other"), "joined", room.ID, rooms.MLSRequest{Epoch: mlsInt(1)})
}
func TestPGMLSOpaqueLifecycleIsolationAndRestart(t *testing.T) {
	f, room, gid := mlsFixture(t)
	ctx := context.Background()
	mlsFail(t, f, f.third, mlsDeviceName(f, "device-stranger"), "groupinfo", gid, rooms.MLSRequest{}, rooms.ErrForbidden)
	mlsFail(t, f, f.other, mlsDeviceName(f, "device-other"), "joined", room.ID, rooms.MLSRequest{Epoch: mlsInt(0)}, rooms.ErrForbidden)
	mlsFail(t, f, f.other, mlsDeviceName(f, "device-owner"), "publish", "", rooms.MLSRequest{DeviceID: mlsDeviceName(f, "device-owner"), KeyPackage: mlsBytes("stolen")}, rooms.ErrForbidden)
	// A racing bootstrap returns the durable winner without replacing its epoch.
	s := mlsDo(t, f, f.other, mlsDeviceName(f, "device-other"), "bootstrap", gid, rooms.MLSRequest{Epoch: mlsInt(0), StateVersion: mlsInt(1), Meta: map[string]string{"bootstrap_device_id": mlsDeviceName(f, "device-other")}}).(rooms.MLSState)
	if s.Meta["bootstrap_device_id"] != mlsDeviceName(f, "device-owner") {
		t.Fatal(s)
	}
	mlsExternalJoin(t, f, room, gid)
	pending := mlsDo(t, f, f.owner, mlsDeviceName(f, "device-owner"), "pending", "", rooms.MLSRequest{}).([]rooms.MLSEnvelope)
	if len(pending) != 1 || pending[0].Type != rooms.MLSCommit || pending[0].Epoch != 1 {
		t.Fatal(pending)
	}
	f.repo = chatRepo(t, f.db).WithMLSDelivery()
	again := mlsDo(t, f, f.owner, mlsDeviceName(f, "device-owner"), "pending", "", rooms.MLSRequest{}).([]rooms.MLSEnvelope)
	if len(again) != 1 || again[0].ID != pending[0].ID || again[0].Sequence != pending[0].Sequence {
		t.Fatal("restart changed pending queue", again)
	}
	mlsFail(t, f, f.other, mlsDeviceName(f, "device-other"), "ack", pending[0].ID, rooms.MLSRequest{}, rooms.ErrNotFound)
	mlsDo(t, f, f.owner, mlsDeviceName(f, "device-owner"), "ack", pending[0].ID, rooms.MLSRequest{})
	if n := len(mlsDo(t, f, f.owner, mlsDeviceName(f, "device-owner"), "pending", "", rooms.MLSRequest{}).([]rooms.MLSEnvelope)); n != 0 {
		t.Fatal(n)
	}
	req := mlsCommitReq(2, "next-epoch")
	mlsDo(t, f, f.owner, mlsDeviceName(f, "device-owner"), "commit-fanout", gid, req)
	mlsDo(t, f, f.owner, mlsDeviceName(f, "device-owner"), "commit-fanout", gid, req)
	req.Ciphertext = mlsBytes("changed-retry")
	mlsFail(t, f, f.owner, mlsDeviceName(f, "device-owner"), "commit-fanout", gid, req, rooms.ErrConflict)
	pending = mlsDo(t, f, f.other, mlsDeviceName(f, "device-other"), "pending", "", rooms.MLSRequest{}).([]rooms.MLSEnvelope)
	if len(pending) != 1 || pending[0].Epoch != 2 {
		t.Fatal("commit retry duplicated delivery", pending)
	}
	mlsFail(t, f, f.owner, mlsDeviceName(f, "device-owner"), "groupinfo-put", gid, rooms.MLSRequest{Epoch: mlsInt(1), GroupInfo: mlsBytes("stale"), RatchetTree: mlsBytes("tree")}, rooms.ErrInvalid)
	mlsFail(t, f, f.owner, mlsDeviceName(f, "device-owner"), "welcome", gid, rooms.MLSRequest{RecipientAccountID: f.third, Payloads: []rooms.MLSPayload{{RecipientDeviceID: mlsDeviceName(f, "device-stranger"), Ciphertext: mlsBytes("welcome"), Header: mlsBytes("h"), ClientMessageID: "outsider"}}}, rooms.ErrForbidden)
	if _, e := f.repo.ChangeMember(ctx, f.owner, room.ID, rooms.Remove, f.other); e != nil {
		t.Fatal(e)
	}
	if n := len(mlsDo(t, f, f.other, mlsDeviceName(f, "device-other"), "pending", "", rooms.MLSRequest{}).([]rooms.MLSEnvelope)); n != 0 {
		t.Fatal("removed member retained queue", n)
	}
	// A new membership incarnation cannot recover old pending ciphertext.
	query(t, f.db, `UPDATE chat_members SET owned_banned=false WHERE chat_room_id=$1 AND account_id=$2`, room.ID, f.other)
	chatJoin(t, f, room.ID, f.other)
	if n := len(mlsDo(t, f, f.other, mlsDeviceName(f, "device-other"), "pending", "", rooms.MLSRequest{}).([]rooms.MLSEnvelope)); n != 0 {
		t.Fatal("rejoin revived queue", n)
	}
}
func TestPGMLSKeyClaimsAreOneUseAndRevocationDurable(t *testing.T) {
	f, _, _ := mlsFixture(t)
	var wg sync.WaitGroup
	counts := make(chan int, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := f.repo.MLS(context.Background(), f.owner, mlsDeviceName(f, "device-owner"), "claim", f.other, rooms.MLSRequest{})
			if e != nil {
				errs <- e
				return
			}
			counts <- len(v.([]map[string]any))
		}()
	}
	wg.Wait()
	close(counts)
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	sum := 0
	for n := range counts {
		sum += n
	}
	if sum != 1 {
		t.Fatalf("concurrent claims returned %d copies", sum)
	}
	var audited int
	if e := f.db.QueryRow(`SELECT count(*) FROM owned_mls_key_packages WHERE device_id=$1 AND consumed_at IS NOT NULL AND consumed_by=$2`, mlsDeviceName(f, "device-other"), f.owner).Scan(&audited); e != nil || audited != 1 {
		t.Fatal("claim audit missing", audited, e)
	}
	mlsDo(t, f, f.other, mlsDeviceName(f, "device-other"), "publish", "", rooms.MLSRequest{DeviceID: mlsDeviceName(f, "device-other"), KeyPackage: mlsBytes("other-package")})
	if len(mlsDo(t, f, f.owner, mlsDeviceName(f, "device-owner"), "claim", f.other, rooms.MLSRequest{}).([]map[string]any)) != 0 {
		t.Fatal("reupload revived consumed package")
	}
	mlsFail(t, f, f.owner, mlsDeviceName(f, "device-owner"), "claim", f.third, rooms.MLSRequest{}, rooms.ErrForbidden)
	mlsDo(t, f, f.other, mlsDeviceName(f, "device-other"), "revoke", mlsDeviceName(f, "device-other"), rooms.MLSRequest{})
	mlsFail(t, f, f.other, mlsDeviceName(f, "device-other"), "publish", "", rooms.MLSRequest{DeviceID: mlsDeviceName(f, "device-other"), KeyPackage: mlsBytes("revival")}, rooms.ErrForbidden)
	mlsFail(t, f, f.other, mlsDeviceName(f, "device-other"), "pending", "", rooms.MLSRequest{}, rooms.ErrForbidden)
}
func TestPGMLSResetRequiresExplicitGenerationAndRejectsReplay(t *testing.T) {
	f, room, gid := mlsFixture(t)
	mlsExternalJoin(t, f, room, gid)
	reset := rooms.MLSRequest{NewEpoch: mlsInt(0), StateVersion: mlsInt(2), Reason: "synthetic-reset"}
	mlsFail(t, f, f.owner, mlsDeviceName(f, "device-owner"), "reset", gid, reset, rooms.ErrUnsupported)
	reset.Generation = mlsInt(1)
	reset.ResetCapable = true
	s := mlsDo(t, f, f.owner, mlsDeviceName(f, "device-owner"), "reset", gid, reset).(rooms.MLSState)
	if s.Epoch != 0 || s.StateVersion != 2 {
		t.Fatal(s)
	}
	legacy := rooms.MLSRequest{Epoch: mlsInt(0), StateVersion: mlsInt(1), Meta: map[string]string{"bootstrap_device_id": mlsDeviceName(f, "device-owner")}}
	mlsFail(t, f, f.owner, mlsDeviceName(f, "device-owner"), "bootstrap", gid, legacy, rooms.ErrConflict)
	legacy.StateVersion = mlsInt(2)
	legacy.Generation = mlsInt(2)
	mlsDo(t, f, f.owner, mlsDeviceName(f, "device-owner"), "bootstrap", gid, legacy)
	replay := mlsCommitReq(1, "old-generation-epoch-one")
	replay.Generation = mlsInt(1)
	mlsFail(t, f, f.owner, mlsDeviceName(f, "device-owner"), "commit-fanout", gid, replay, rooms.ErrConflict)
	replay.Generation = nil
	mlsFail(t, f, f.owner, mlsDeviceName(f, "device-owner"), "commit-fanout", gid, replay, rooms.ErrConflict)
	replay.Generation = mlsInt(2)
	mlsDo(t, f, f.owner, mlsDeviceName(f, "device-owner"), "commit-fanout", gid, replay)
	if n := len(mlsDo(t, f, f.other, mlsDeviceName(f, "device-other"), "pending", "", rooms.MLSRequest{}).([]rooms.MLSEnvelope)); n != 0 {
		t.Fatal("reset retained earlier membership/delivery", n)
	}
	mlsFail(t, f, f.owner, mlsDeviceName(f, "device-owner"), "reset", gid, reset, rooms.ErrConflict)
}
func TestPGMLSCanonicalCiphertextAndAuthoredHTTP(t *testing.T) {
	f, room, gid := mlsFixture(t)
	ctx := context.Background()
	req := rooms.TextRequest{EncryptionMeta: &rooms.EncryptionMeta{Scheme: "chat.mls.v2", Ciphertext: mlsBytes("message"), Header: mlsBytes("header"), Epoch: 0}, ClientMessageID: func() *string { s := "opaque-message"; return &s }()}
	v, e := f.repo.Send(ctx, f.owner, room.ID, req)
	if e != nil {
		t.Fatal(e)
	}
	if v.Content != nil || !strings.Contains(string(v.EncryptionMeta), "state_version") {
		t.Fatal(v)
	}
	var content *string
	var meta []byte
	if e = f.db.QueryRow(`SELECT content,encryption_meta FROM chat_messages WHERE id=$1`, v.ID).Scan(&content, &meta); e != nil || content != nil || !strings.Contains(string(meta), req.EncryptionMeta.Ciphertext) {
		t.Fatal(content, string(meta), e)
	}
	if _, e = f.repo.Send(ctx, f.owner, room.ID, rooms.TextRequest{Content: "MUST NOT STORE"}); !errors.Is(e, rooms.ErrInvalid) {
		t.Fatal(e)
	}
	stale := req
	stale.EncryptionMeta = &rooms.EncryptionMeta{Scheme: "chat.mls.v2", Ciphertext: mlsBytes("stale"), Header: mlsBytes("header"), Epoch: 1}
	if _, e = f.repo.Send(ctx, f.owner, room.ID, stale); !errors.Is(e, rooms.ErrConflict) {
		t.Fatal(e)
	}
	req.EncryptionMeta = &rooms.EncryptionMeta{Scheme: "chat.mls.v2", Ciphertext: mlsBytes("edit"), Header: mlsBytes("header"), Epoch: 0}
	if _, e = f.repo.Edit(ctx, f.owner, room.ID, v.ID, req); e != nil {
		t.Fatal(e)
	}
	var eventsBefore, eventsAfter int64
	if e = f.db.QueryRow(`SELECT count(*) FROM chat_messages WHERE chat_room_id=$1 AND type='messages.update'`, room.ID).Scan(&eventsBefore); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.Edit(ctx, f.owner, room.ID, v.ID, req); e != nil {
		t.Fatal(e)
	}
	if e = f.db.QueryRow(`SELECT count(*) FROM chat_messages WHERE chat_room_id=$1 AND type='messages.update'`, room.ID).Scan(&eventsAfter); e != nil || eventsBefore != eventsAfter {
		t.Fatal("repeated encrypted edit appended another event", eventsBefore, eventsAfter, e)
	}
	if e = f.repo.DeleteMessage(ctx, f.owner, room.ID, v.ID); !errors.Is(e, rooms.ErrConflict) {
		t.Fatal("bodyless encrypted delete lacked epoch", e)
	}
	deleteCtx := rooms.WithMLSWrite(ctx, rooms.MLSWriteContext{Encryption: &rooms.EncryptionMeta{Scheme: "chat.mls.v2", Epoch: 0, Ciphertext: mlsBytes("delete"), Header: mlsBytes("header")}})
	if e = f.repo.DeleteMessage(deleteCtx, f.owner, room.ID, v.ID); e != nil {
		t.Fatal(e)
	}
	// Actual joined caller sends only its body device selector, without a header.
	chatCall(t, f.handler, "POST", "/api/chat/"+room.ID+"/devices/me/joined", f.token, `{"mls_device_id":"`+mlsDeviceName(f, "device-owner")+`","epoch":0}`, 200)
	call := func(method, path, token, device, body string, want int) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		if device != "" {
			r.Header.Set("X-Device-Id", device)
		}
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s got %d: %s", method, path, w.Code, w.Body.String())
		}
	}
	call("DELETE", "/api/chat/"+room.ID+"/messages/"+v.ID, f.token, "", "", 409)
	deletionBody, _ := json.Marshal(map[string]any{"encryption_meta": req.EncryptionMeta})
	call("DELETE", "/api/chat/"+room.ID+"/messages/"+v.ID, f.token, "", string(deletionBody), 204)
	call("GET", "/e2ee/mls/kp/status", f.token, mlsDeviceName(f, "device-owner"), "", 200)
	call("GET", "/e2ee/mls/kp/status", f.otherToken, mlsDeviceName(f, "device-owner"), "", 403)
	call("POST", "/e2ee/mls/groups/"+gid+"/commit", f.token, mlsDeviceName(f, "device-owner"), `{"epoch":0,"reason":"client_commit"}`, 200)
	call("POST", "/e2ee/mls/groups/"+gid+"/commit", f.token, mlsDeviceName(f, "device-owner"), `{"epoch":1,"reason":"client_commit"}`, 409)
	call("POST", "/e2ee/mls/envelopes/"+authorityUUID(t)+"/ack?deviceId="+mlsDeviceName(f, "device-other"), f.token, mlsDeviceName(f, "device-owner"), "", 400)
	raw, _ := json.Marshal(map[string]any{"room_id": gid, "payload": map[string]any{"type": "text", "client_message_id": "bad-local-envelope", "plaintext_envelope": map[string]string{"content": "SECRET"}, "encryption_meta": req.EncryptionMeta}})
	call("POST", "/e2ee/mls/messages/fanout", f.token, mlsDeviceName(f, "device-owner"), string(raw), 400)
}

func TestPGMLSAtomicFanoutRollbackAndJoiningExpiry(t *testing.T) {
	f, room, gid := mlsFixture(t)
	mlsDo(t, f, f.other, mlsDeviceName(f, "device-other-two"), "publish", "", rooms.MLSRequest{DeviceID: mlsDeviceName(f, "device-other-two"), KeyPackage: mlsBytes("second-device-package")})
	mlsDo(t, f, f.owner, mlsDeviceName(f, "device-owner"), "groupinfo-put", gid, rooms.MLSRequest{Epoch: mlsInt(0), GroupInfo: mlsBytes("info"), RatchetTree: mlsBytes("tree")})
	mlsDo(t, f, f.other, mlsDeviceName(f, "device-other"), "groupinfo", gid, rooms.MLSRequest{})
	query(t, f.db, `UPDATE owned_mls_memberships SET expires_at=clock_timestamp()-interval '1 second' WHERE group_id=$1 AND device_id=$2`, gid, mlsDeviceName(f, "device-other"))
	mlsFail(t, f, f.other, mlsDeviceName(f, "device-other"), "commit-fanout", gid, mlsCommitReq(1, "expired-join"), rooms.ErrForbidden)
	query(t, f.db, `UPDATE owned_mls_devices SET next_sequence=9223372036854775807 WHERE id=$1`, mlsDeviceName(f, "device-other-two"))
	request := rooms.MLSRequest{RecipientAccountID: f.other, Payloads: []rooms.MLSPayload{
		{RecipientDeviceID: mlsDeviceName(f, "device-other"), Ciphertext: mlsBytes("welcome-one"), Header: mlsBytes("h"), ClientMessageID: "atomic-welcome"},
		{RecipientDeviceID: mlsDeviceName(f, "device-other-two"), Ciphertext: mlsBytes("welcome-two"), Header: mlsBytes("h"), ClientMessageID: "atomic-welcome"},
	}}
	mlsFail(t, f, f.owner, mlsDeviceName(f, "device-owner"), "welcome", gid, request, rooms.ErrConflict)
	var n, sequence int64
	if e := f.db.QueryRow(`SELECT count(*) FROM owned_mls_envelopes WHERE group_id=$1`, gid).Scan(&n); e != nil || n != 0 {
		t.Fatal("partial envelope write survived rollback", n, e)
	}
	if e := f.db.QueryRow(`SELECT next_sequence FROM owned_mls_devices WHERE id=$1`, mlsDeviceName(f, "device-other")).Scan(&sequence); e != nil || sequence != 0 {
		t.Fatal("allocation survived rollback", sequence, e)
	}
	query(t, f.db, `UPDATE owned_mls_devices SET next_sequence=0 WHERE id=$1`, mlsDeviceName(f, "device-other-two"))
	mlsDo(t, f, f.owner, mlsDeviceName(f, "device-owner"), "welcome", gid, request)
	mlsDo(t, f, f.other, mlsDeviceName(f, "device-other"), "joined", room.ID, rooms.MLSRequest{Epoch: mlsInt(0)})
	mlsDo(t, f, f.other, mlsDeviceName(f, "device-other-two"), "joined", room.ID, rooms.MLSRequest{Epoch: mlsInt(0)})
	// Competing transitions from one delivery epoch have exactly one winner.
	var wg sync.WaitGroup
	success := make(chan bool, 2)
	errs := make(chan error, 2)
	for i, device := range []string{mlsDeviceName(f, "device-owner"), mlsDeviceName(f, "device-other")} {
		wg.Add(1)
		go func(i int, device string) {
			defer wg.Done()
			actor := f.owner
			if i == 1 {
				actor = f.other
			}
			_, e := f.repo.MLS(context.Background(), actor, device, "commit-fanout", gid, mlsCommitReq(1, device+"-race"))
			success <- e == nil
			if e != nil && !errors.Is(e, rooms.ErrConflict) {
				errs <- e
			}
		}(i, device)
	}
	wg.Wait()
	close(success)
	close(errs)
	wins := 0
	for ok := range success {
		if ok {
			wins++
		}
	}
	for e := range errs {
		t.Fatal(e)
	}
	if wins != 1 {
		t.Fatalf("%d commit race winners", wins)
	}
}

func TestPGMLSBackendAuthorityAndGatewayComposition(t *testing.T) {
	f := newBackendFixture(t)
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, EnableMLSDelivery: true})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	call := func(method, path, token, device, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.Header.Set("Content-Type", "application/json")
		if device != "" {
			r.Header.Set("X-Device-Id", device)
		}
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s status %d: %s", method, path, w.Code, w.Body.String())
		}
		return w
	}
	call("GET", "/e2ee/mls/kp/status", "", "backend-owner", "", 401)
	body, _ := json.Marshal(rooms.MLSRequest{DeviceID: "backend-owner-" + f.actor, KeyPackage: mlsBytes("backend-owner-package")})
	device := "backend-owner-" + f.actor
	call("PUT", "/stargate/e2ee/mls/devices/me/kps", f.token, device, string(body), 200)
	call("GET", "/api/e2ee/mls/kp/status", f.token, device, "", 200)
	call("GET", "/e2ee/mls/kp/status", f.otherToken, device, "", 403)
	dm := backendDecode[rooms.Room](t, backendJSONCall(t, app, "POST", "/messager/chat/direct", f.token, rooms.DirectRequest{RelatedUserID: f.other}, 200))
	backendCall(t, app, "POST", "/messager/chat/"+dm.ID+"/confirm", f.otherToken, "", 200)
	enabled := backendDecode[rooms.Room](t, call("POST", "/messager/chat/"+dm.ID+"/mls/enable", f.token, "", "{}", 200))
	if enabled.EncryptionMode != 3 || enabled.MLSGroupID == nil {
		t.Fatal(enabled)
	}
	body, _ = json.Marshal(rooms.MLSRequest{Epoch: mlsInt(0), StateVersion: mlsInt(1), Meta: map[string]string{"bootstrap_device_id": device}})
	call("POST", "/stargate/e2ee/mls/groups/"+*enabled.MLSGroupID+"/bootstrap", f.token, device, string(body), 200)
	body, _ = json.Marshal(rooms.MLSRequest{MLSDeviceID: device, Epoch: mlsInt(0)})
	call("POST", "/messager/chat/"+dm.ID+"/devices/me/joined", f.token, "", string(body), 200)
	body = fixtureMLSMessageBody(t, rooms.EncryptionMeta{Scheme: "chat.mls.v2", Epoch: 0, Ciphertext: mlsBytes("backend-message"), Header: mlsBytes("header")})
	call("POST", "/messager/chat/"+dm.ID+"/messages", f.token, "", string(body), 200)
	backendCall(t, app, "POST", "/messager/chat/"+dm.ID+"/block", f.otherToken, "", 204)
	call("GET", "/e2ee/mls/groups/"+*enabled.MLSGroupID+"/groupinfo", f.token, device, "", 403)
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
	call("GET", "/e2ee/mls/kp/status", f.token, device, "", 423)
}

// The authored encrypted transport omits content entirely. TextRequest is also
// the plain-text request DTO and deliberately serializes its content field.
func fixtureMLSMessageBody(t *testing.T, meta rooms.EncryptionMeta) []byte {
	t.Helper()
	body, e := json.Marshal(struct {
		Encryption rooms.EncryptionMeta `json:"encryption_meta"`
	}{meta})
	if e != nil {
		t.Fatal(e)
	}
	return body
}
func TestMLSEncryptedFixtureRequestShape(t *testing.T) {
	meta := rooms.EncryptionMeta{Scheme: "chat.mls.v2", Epoch: 0, Ciphertext: mlsBytes("synthetic-message"), Header: mlsBytes("header")}
	var request rooms.TextRequest
	if e := json.Unmarshal(fixtureMLSMessageBody(t, meta), &request); e != nil {
		t.Fatal(e)
	}
	if _, e := rooms.ValidateText(request, false); e != nil {
		t.Fatal("authored encrypted fixture rejected", e)
	}
	wrong, e := json.Marshal(rooms.TextRequest{EncryptionMeta: &meta})
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(wrong, &request); e != nil {
		t.Fatal(e)
	}
	if _, e = rooms.ValidateText(request, false); e == nil {
		t.Fatal("plaintext field in encrypted fixture accepted")
	}
}
