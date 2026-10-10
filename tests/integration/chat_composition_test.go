// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
	profiles "solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/surveys"
)

func TestPGBackendChatTypedFundSurveyComposition(t *testing.T) {
	f := newBackendFixture(t)
	for _, actor := range []string{f.actor, f.other} {
		if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
			t.Fatal(e)
		}
	}
	query(t, f.db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at)VALUES($1,2,clock_timestamp())`, f.actor)
	pub := backendDecode[profiles.Publisher](t, backendCall(t, f.app, "POST", "/sphere/publishers/individual", f.token, fmt.Sprintf(`{"name":%q,"nick":"Chat composition"}`, "chat_compose_"+strings.ReplaceAll(f.actor, "-", "")), 200))
	questions := surveyQuestions()
	raw, _ := json.Marshal(surveys.Patch{Questions: &questions})
	survey := backendDecode[surveys.Survey](t, backendCall(t, f.app, "POST", "/sphere/surveys?pub="+pub.Name, f.token, string(raw), 200))
	backendCall(t, f.app, "POST", "/api/surveys/"+survey.ID+"/publish", f.token, "", 200)
	wallet := walletCreate(t, walletpg.New(f.db), f.actor)
	fund := authorityUUID(t)
	query(t, f.db, `INSERT INTO wallet_v1.funds(id,creator_account_id,beneficiary_wallet_id,currency,total_amount,amount_of_splits,split_type,message,status,is_open,is_raising,target_amount,contribution_type,contribution_amount,expires_at,created_at,updated_at,fingerprint)VALUES($1,$2,$3,'chat-compose',1,1,0,'not copied to chat metadata',0,false,false,0,0,0,clock_timestamp()+interval '1 day',clock_timestamp(),clock_timestamp(),repeat('0',64))`, fund, f.actor, wallet.ID)
	room := backendDecode[rooms.Room](t, backendCall(t, f.app, "POST", "/messager/chat", f.token, `{"name":"Typed composition"}`, 200))
	target := "/messager/chat/" + room.ID + "/messages"
	meet, day, event := authorityUUID(t), authorityUUID(t), authorityUUID(t)
	query(t, f.db, `INSERT INTO owned_meets(id,host_id,status,visibility,metadata,expires_at,created_at,updated_at)VALUES($1,$2,0,1,'{}',clock_timestamp()+interval '1 day',clock_timestamp(),clock_timestamp())`, meet, f.actor)
	query(t, f.db, `INSERT INTO owned_notable_days(id,definition,region,start_date,end_date,is_recurring,created_at,updated_at)VALUES($1,'{"name":"Canonical chat day"}','US',clock_timestamp(),clock_timestamp()+interval '1 day',false,clock_timestamp(),clock_timestamp())`, day)
	query(t, f.db, `INSERT INTO owned_calendar_events(id,account_id,definition,start_time,end_time,visibility,is_recurring,created_at,updated_at)VALUES($1,$2,'{"title":"Canonical private event","time_zone":"UTC","tags":[]}',clock_timestamp(),clock_timestamp()+interval '1 day',0,false,clock_timestamp(),clock_timestamp())`, event, f.actor)
	message := backendDecode[messager.MessageProjection](t, backendCall(t, f.app, "POST", target, f.token, fmt.Sprintf(`{"content":"","fund_id":%q,"survey_id":%q,"meet_id":%q,"notable_day_id":%q,"calendar_event_id":%q,"location_name":"Venue","location_address":"One Street","location_wkt":"POINT(1.5 2.5)","client_message_id":"typed-one"}`, fund, survey.ID, meet, day, event), 200))
	for _, fragment := range []string{`"type":"fund"`, fund, `"type":"survey"`, survey.ID, `"type":"meet"`, meet, `"type":"notable_day"`, day, `"type":"calendar_event"`, event, `"type":"location"`, `"wkt":"POINT(1.5 2.5)"`} {
		if !bytes.Contains(message.Meta, []byte(fragment)) {
			t.Fatal("missing typed embed", fragment, string(message.Meta))
		}
	}
	if bytes.Contains(message.Meta, []byte("not copied")) {
		t.Fatal("typed embeds", string(message.Meta))
	}
	backendCall(t, f.app, "POST", target, f.otherToken, fmt.Sprintf(`{"fund_id":%q}`, fund), 403)
	edited := backendDecode[messager.MessageProjection](t, backendCall(t, f.app, "PATCH", target+"/"+message.ID, f.token, `{"content":"plain","fund_id":null,"survey_id":null,"meet_id":null,"notable_day_id":null,"calendar_event_id":null,"location_name":null,"location_address":null,"location_wkt":null}`, 200))
	if bytes.Contains(edited.Meta, []byte(`"type":`)) {
		t.Fatal("typed embeds not removed", string(edited.Meta))
	}
	rawMessage := backendDecode[messager.MessageProjection](t, backendCall(t, f.app, "POST", target, f.token, fmt.Sprintf(`{"content":"canonical raw locators","embeds":[{"type":"meet","id":%q},{"type":"notable_day","id":%q},{"type":"calendar_event","id":%q}]}`, meet, day, event), 200))
	for _, id := range []string{meet, day, event} {
		if !bytes.Contains(rawMessage.Meta, []byte(id)) {
			t.Fatal("canonical raw locator missing", id, string(rawMessage.Meta))
		}
	}
	missing := authorityUUID(t)
	backendCall(t, f.app, "POST", target, f.token, fmt.Sprintf(`{"content":"missing typed source","meet_id":%q}`, missing), 400)
	backendCall(t, f.app, "POST", target, f.token, fmt.Sprintf(`{"content":"missing raw source","embeds":[{"type":"calendar_event","id":%q}]}`, missing), 400)
	backendCall(t, f.app, "POST", target, f.token, fmt.Sprintf(`{"content":"copied projection","embeds":[{"type":"meet","id":%q,"notes":"must not be copied"}]}`, meet), 400)
	backendCall(t, f.app, "POST", target, f.token, fmt.Sprintf(`{"content":"nested bypass","meta":{"embeds":[{"type":"meet","id":%q}]}}`, meet), 400)
	foreignMeet, foreignEvent := authorityUUID(t), authorityUUID(t)
	query(t, f.db, `INSERT INTO owned_meets(id,host_id,status,visibility,metadata,expires_at,created_at,updated_at)VALUES($1,$2,0,0,'{}',clock_timestamp()+interval '1 day',clock_timestamp(),clock_timestamp())`, foreignMeet, f.other)
	query(t, f.db, `INSERT INTO owned_calendar_events(id,account_id,definition,start_time,end_time,visibility,is_recurring,created_at,updated_at)VALUES($1,$2,'{"title":"Foreign public event","time_zone":"UTC","tags":[]}',clock_timestamp(),clock_timestamp()+interval '1 day',200,false,clock_timestamp(),clock_timestamp())`, foreignEvent, f.other)
	backendCall(t, f.app, "POST", target, f.token, fmt.Sprintf(`{"content":"foreign meet","meet_id":%q}`, foreignMeet), 400)
	backendCall(t, f.app, "POST", target, f.token, fmt.Sprintf(`{"content":"foreign calendar","calendar_event_id":%q}`, foreignEvent), 400)
	query(t, f.db, `UPDATE owned_notable_days SET deleted_at=clock_timestamp() WHERE id=$1`, day)
	backendCall(t, f.app, "POST", target, f.token, fmt.Sprintf(`{"content":"deleted day","notable_day_id":%q}`, day), 400)
}

func TestPGBackendChatCanonicalCompositionRechecksAfterSourceLockWait(t *testing.T) {
	f := newBackendFixture(t)
	room := backendDecode[rooms.Room](t, backendCall(t, f.app, "POST", "/messager/chat", f.token, `{"name":"Canonical wait"}`, 200))
	meet, day := authorityUUID(t), authorityUUID(t)
	var expires time.Time
	if e := f.db.QueryRow(`INSERT INTO owned_meets(id,host_id,status,visibility,metadata,expires_at,created_at,updated_at)VALUES($1,$2,0,1,'{}',clock_timestamp()+interval '1 second',clock_timestamp(),clock_timestamp()) RETURNING expires_at`, meet, f.actor).Scan(&expires); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `INSERT INTO owned_notable_days(id,definition,region,start_date,end_date,is_recurring,created_at,updated_at)VALUES($1,'{"name":"Wait boundary"}','US',clock_timestamp(),clock_timestamp()+interval '1 day',false,clock_timestamp(),clock_timestamp())`, day)
	blocker, e := f.db.BeginTx(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer blocker.Rollback()
	var blockerPID int
	if e = blocker.QueryRow(`SELECT pg_backend_pid()`).Scan(&blockerPID); e != nil {
		t.Fatal(e)
	}
	if _, e = blocker.Exec(`SELECT id FROM owned_notable_days WHERE id=$1 FOR UPDATE`, day); e != nil {
		t.Fatal(e)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := fixtureRawJSONRequest("POST", "/messager/chat/"+room.ID+"/messages", f.token, fmt.Sprintf(`{"content":"must cross wait","meet_id":%q,"notable_day_id":%q}`, meet, day))
		w := httptest.NewRecorder()
		f.app.ServeHTTP(w, r)
		done <- w
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		var waiting bool
		if e = f.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND position('owned_notable_days' in query)>0)`, blockerPID).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		select {
		case w := <-done:
			t.Fatal("composition did not reach canonical source lock", w.Code, w.Body.String())
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	organizationWaitExpired(t, ctx, f.db, expires)
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	select {
	case w := <-done:
		if w.Code != 400 {
			t.Fatal("expired earlier source survived later lock wait", w.Code, w.Body.String())
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var messages int
	if e = f.db.QueryRow(`SELECT count(*) FROM chat_messages WHERE chat_room_id=$1`, room.ID).Scan(&messages); e != nil || messages != 0 {
		t.Fatal("failed canonical composition committed", messages, e)
	}
}

func TestPGChatCanonicalCompositionRechecksAfterAttachmentWait(t *testing.T) {
	for _, mode := range []string{"send", "edit-changed", "edit-unchanged"} {
		t.Run(mode, func(t *testing.T) {
			f := newChatMediaFixture(t)
			f.chat.WithMessageComposer(application.NewChatMessageComposer(nil, nil))
			room := chatMediaRoom(t, f, false)
			file := chatMediaFile(t, f, "canonical-"+mode, "canonical wait bytes")
			meet := authorityUUID(t)
			var expires time.Time
			if e := f.db.QueryRow(`INSERT INTO owned_meets(id,host_id,status,visibility,metadata,expires_at,created_at,updated_at)VALUES($1,$2,0,1,'{}',clock_timestamp()+interval '2 seconds',clock_timestamp(),clock_timestamp()) RETURNING expires_at`, meet, f.actor).Scan(&expires); e != nil {
				t.Fatal(e)
			}
			initialLinks := 0
			var original messager.MessageProjection
			if mode != "send" {
				request := rooms.TextRequest{Content: "original", MeetID: &meet}
				if mode == "edit-unchanged" {
					request.AttachmentsID = mediaIDs(file.ID)
					initialLinks = 1
				}
				var e error
				original, e = f.chat.Send(f.ctx, f.actor, room.ID, request)
				if e != nil {
					t.Fatal(e)
				}
			}
			var sequenceBefore int64
			if e := f.db.QueryRow(`SELECT owned_next_sequence FROM chat_rooms WHERE id=$1`, room.ID).Scan(&sequenceBefore); e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			blocker, pid := atomicBlocker(t, locks, "owned_media_files", file.ID)
			done := make(chan error, 1)
			go func() {
				var e error
				switch mode {
				case "send":
					_, e = f.chat.Send(ctx, f.actor, room.ID, rooms.TextRequest{Content: "must roll back", MeetID: &meet, AttachmentsID: mediaIDs(file.ID)})
				case "edit-changed":
					_, e = f.chat.Edit(ctx, f.actor, room.ID, original.ID, rooms.TextRequest{Content: "changed", AttachmentsID: mediaIDs(file.ID)})
				case "edit-unchanged":
					_, e = f.chat.Edit(ctx, f.actor, room.ID, original.ID, rooms.TextRequest{AttachmentsID: mediaIDs(file.ID)})
				}
				done <- e
			}()
			waitAtomicBlocked(t, locks, pid, "owned_media_files")
			organizationWaitExpired(t, ctx, f.db, expires)
			if e := blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e := <-done; !errors.Is(e, rooms.ErrInvalid) {
				t.Fatal("expired canonical meet committed after attachment wait", e)
			}
			var messages, updates, links int
			if e := f.db.QueryRow(`SELECT count(*),count(*)FILTER(WHERE type='messages.update') FROM chat_messages WHERE chat_room_id=$1`, room.ID).Scan(&messages, &updates); e != nil {
				t.Fatal(e)
			}
			if e := f.db.QueryRow(`SELECT count(*) FROM owned_chat_media link JOIN chat_messages message ON message.id=link.message_id WHERE message.chat_room_id=$1`, room.ID).Scan(&links); e != nil {
				t.Fatal(e)
			}
			wantMessages := 0
			if mode != "send" {
				wantMessages = 1
				var content, meta string
				if e := f.db.QueryRow(`SELECT content,meta::text FROM chat_messages WHERE id=$1`, original.ID).Scan(&content, &meta); e != nil || content != "original" || meta != string(original.Meta) {
					t.Fatal("failed edit changed original", content, meta, string(original.Meta), e)
				}
			}
			var sequenceAfter int64
			if e := f.db.QueryRow(`SELECT owned_next_sequence FROM chat_rooms WHERE id=$1`, room.ID).Scan(&sequenceAfter); e != nil || sequenceAfter != sequenceBefore {
				t.Fatal("failed canonical operation advanced room sequence", sequenceBefore, sequenceAfter, e)
			}
			if messages != wantMessages || updates != 0 || links != initialLinks {
				t.Fatal("canonical rollback incomplete", messages, updates, links, wantMessages, initialLinks)
			}
		})
	}
}
