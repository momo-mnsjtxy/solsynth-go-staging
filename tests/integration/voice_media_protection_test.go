// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"testing"

	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/ports/messager/rooms"
)

func TestPGBackendVoiceReadProtectionDriveEndpoints(t *testing.T) {
	for _, scenario := range []string{"expired", "left", "rejoined", "message_deleted", "voice_disabled"} {
		t.Run(scenario, func(t *testing.T) {
			f := newChatTransient(t)
			app := transientApp(t, f)
			room := chatMediaRoom(t, f, false)
			actor, ctx, token := f.actor, f.ctx, f.token
			if scenario == "left" || scenario == "rejoined" {
				chatMediaJoin(t, f, room.ID)
				actor, ctx, token = f.other, f.otherCtx, f.otherToken
			}
			m, e := f.chat.SendVoice(ctx, actor, room.ID, transientVoice("drive-protection"))
			if e != nil {
				t.Fatal(e)
			}
			clip := transientClip(t, m)
			path := "/api/media/files/" + clip
			backendCall(t, app, "GET", path+"/info", token, "", 200)
			backendCall(t, app, "GET", path, token, "", 200)
			// Even a public bit cannot grant access outside the current private room.
			query(t, f.db, `UPDATE owned_media_files SET is_public=true WHERE id=$1`, clip)
			backendCall(t, app, "GET", path+"/info", "", "", 404)
			backendCall(t, app, "GET", path, "", "", 404)
			switch scenario {
			case "expired":
				query(t, f.db, `UPDATE owned_chat_voice_clips SET created_at=clock_timestamp()-interval '1 hour',expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, clip)
			case "left", "rejoined":
				if _, e = f.chat.ChangeMember(ctx, actor, room.ID, rooms.Leave, ""); e != nil {
					t.Fatal(e)
				}
				if scenario == "rejoined" {
					if _, e = f.chat.ChangeMember(f.ctx, f.actor, room.ID, rooms.Invite, actor); e != nil {
						t.Fatal(e)
					}
					if _, e = f.chat.ChangeMember(ctx, actor, room.ID, rooms.Accept, ""); e != nil {
						t.Fatal(e)
					}
					backendCall(t, app, "GET", "/api/chat/"+room.ID+"/voice/"+clip, token, "", 404)
				}
			case "message_deleted":
				if e = f.chat.DeleteMessage(f.ctx, f.actor, room.ID, m.ID); e != nil {
					t.Fatal(e)
				}
			case "voice_disabled":
				visibility := int32(0)
				policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
				app, e = application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, MediaStore: f.store, MaxMediaBytes: 65536})
				if e != nil {
					t.Fatal(e)
				}
				if e = app.Ready(context.Background()); e != nil {
					t.Fatal(e)
				}
			}
			// Ownership and generic media permissions cannot revive the clip.
			for _, suffix := range []string{"", "/info"} {
				backendCall(t, app, "GET", path+suffix, token, "", 404)
				backendCall(t, app, "HEAD", path+suffix, token, "", 404)
				backendCall(t, app, "GET", path+suffix, "", "", 404)
			}
			var n int
			if e = f.db.QueryRow(`SELECT count(*) FROM owned_media_files WHERE id=$1 AND state='ready'`, clip).Scan(&n); e != nil || n != 1 {
				t.Fatal("denial must precede physical cleanup", n, e)
			}
		})
	}
}
