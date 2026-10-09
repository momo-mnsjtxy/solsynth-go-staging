// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/passport/social"
	"strings"
	"testing"
	"time"
)

type directRecoveryMemberSaved struct {
	ID, Account             string
	Joined, Left, Confirmed *time.Time
}
type directRecoverySaved struct {
	Owner, Peer, Room, Block, Message, Group string
	Deadline                                 time.Time
	OwnerState, PeerState                    directRecoveryMemberSaved
}

func sameRecoveryTime(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}
func assertRecoveryProjection(t *testing.T, v rooms.Room, actor, target, room string) {
	t.Helper()
	raw, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	if !v.RecoveryOnly || v.ID != room || len(v.Members) != 1 || v.Members[0].AccountID != actor || len(v.DirectMembers) != 1 || v.DirectMembers[0].AccountID != target {
		t.Fatal("recovery projection changed established pair", v)
	}
	if strings.Contains(string(raw), "synthetic-private-") || v.Members[0].Account != nil || v.DirectMembers[0].Account != nil || v.Members[0].Room != nil || v.Realm != nil || v.RealmID != nil || v.MLSGroupID != nil || v.MLSEpoch != 0 || v.MLSStateVersion != 0 || v.Name != "" || v.Description != "" || v.Members[0].LastReadAt != nil || v.Members[0].TimeoutCause != nil || v.Members[0].TimeoutUntil != nil {
		t.Fatal("private recovery metadata escaped", string(raw))
	}
	if v.AccountID != actor && v.AccountID != target && v.AccountID != "" {
		t.Fatal("unrelated owner escaped", v.AccountID)
	}
}
func assertDirectRecoveryPrivacy(t *testing.T, chat *chatpg.Repository, reader *messagerpg.Repository, state directRecoverySaved, contexts map[string]context.Context) {
	t.Helper()
	for _, pair := range [][2]string{{state.Owner, state.Peer}, {state.Peer, state.Owner}} {
		ctx := context.Background()
		if contexts != nil {
			ctx = contexts[pair[0]]
		}
		v, e := chat.GetDirect(ctx, pair[0], pair[1])
		if e != nil {
			t.Fatal(e)
		}
		assertRecoveryProjection(t, v, pair[0], pair[1], state.Room)
		want := state.OwnerState
		if pair[0] == state.Peer {
			want = state.PeerState
		}
		own := v.Members[0]
		if want.ID != "" && (own.ID != want.ID || !sameRecoveryTime(own.JoinedAt, want.Joined) || !sameRecoveryTime(own.LeaveAt, want.Left) || !sameRecoveryTime(own.ConfirmedAt, want.Confirmed)) {
			t.Fatal("own recovery state changed", own, want)
		}
		if _, e = reader.ListMessages(ctx, messager.MessageListQuery{RoomID: state.Room, AccountID: pair[0], Take: 20}); !errors.Is(e, messager.ErrSyncForbidden) {
			t.Fatal("recovery metadata granted message history", e)
		}
		if _, e = reader.Sync(ctx, messager.SyncQuery{RoomID: state.Room, AccountID: pair[0]}); !errors.Is(e, messager.ErrSyncForbidden) {
			t.Fatal("recovery metadata granted sync", e)
		}
	}
}
func directRecoveryRepositories(t *testing.T, db *sql.DB) (*chatpg.Repository, *messagerpg.Repository) {
	t.Helper()
	// The restart read is an explicitly synthetic fixture, not an unguarded host.
	chat, e := chatpg.NewForCompatibilityTests(db, chatPolicy)
	if e != nil {
		t.Fatal(e)
	}
	chat.WithSocial(socialpg.New(db)).WithMLSDelivery()
	read, e := messagerpg.NewWithIndependentPolicy(db, messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30})
	if e != nil {
		t.Fatal(e)
	}
	read.WithRoomAccess(chat)
	return chat, read
}
func seedDirectRecoveryPrivacy(t *testing.T, f chatRelationsFixture, room rooms.Room) (string, string) {
	t.Helper()
	for _, actor := range []string{f.owner, f.other} {
		query(t, f.db, `INSERT INTO owned_accounts_profiles(account_id,id,bio,location,updated_at)VALUES($1,$2,$3,$4,clock_timestamp()) ON CONFLICT(account_id)DO UPDATE SET bio=EXCLUDED.bio,location=EXCLUDED.location`, actor, authorityUUID(t), "synthetic-private-bio-"+actor, "synthetic-private-location-"+actor)
		query(t, f.db, `INSERT INTO owned_authority_contacts(id,account_id,type,content,is_public,created_at,updated_at)VALUES($1,$2,2,$3,true,clock_timestamp(),clock_timestamp())`, authorityUUID(t), actor, "synthetic-private-contact-"+actor)
	}
	message := relationSend(t, f, f.owner, room.ID, "synthetic-private-message-"+room.ID)
	group := "synthetic-private-group-" + room.ID
	// Disposable opaque MLS storage, never real key material or provider traffic.
	query(t, f.db, `INSERT INTO owned_mls_groups(id,room_id,epoch,state_version,group_info,ratchet_tree,info_epoch)VALUES($1,$2,17,19,'synthetic-private-key-info','synthetic-private-key-tree',17)`, group, room.ID)
	query(t, f.db, `UPDATE chat_rooms SET owned_name='synthetic-private-room',owned_description='synthetic-private-description',encryption_mode=3 WHERE id=$1`, room.ID)
	f.chats.WithMLSDelivery()
	return message.ID, group
}
func directRecoveryWaitControls(t *testing.T) {
	for _, mode := range []string{"block_commits_during_account_wait", "session_revoked_during_account_wait"} {
		t.Run(mode, func(t *testing.T) {
			f := newChatRelationsFixture(t)
			room := relationDirect(t, f)
			block, e := f.social.ChangeRelationship(f.contexts[f.owner], f.owner, f.other, social.RelationshipChange{Action: social.Block, ExpiresIn: expiryString("1h")})
			if e != nil {
				t.Fatal(e)
			}
			query(t, f.db, `UPDATE owned_social_relationships SET expired_at=clock_timestamp()-interval '1 second' WHERE id=$1`, block.ID)
			proof, e := f.verifier.Authenticate(f.contexts[f.other], f.otherToken)
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(f.contexts[f.other], 20*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			low := f.owner
			if f.other < low {
				low = f.other
			}
			blocker, pid := atomicBlocker(t, locks, "owned_authority_accounts", low)
			type response struct {
				room rooms.Room
				err  error
			}
			done := make(chan response, 1)
			go func() { v, e := f.chats.GetDirect(ctx, f.other, f.owner); done <- response{v, e} }()
			waitAtomicBlocked(t, locks, pid, "owned_authority_accounts")
			if mode == "block_commits_during_account_wait" {
				_, e = blocker.ExecContext(ctx, `UPDATE owned_social_relationships SET expired_at=clock_timestamp()+interval '1 hour' WHERE id=$1`, block.ID)
			} else {
				_, e = blocker.ExecContext(ctx, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE id=$1`, proof.Session.ID)
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			got := <-done
			if mode == "block_commits_during_account_wait" {
				if got.err != nil {
					t.Fatal(got.err)
				}
				assertRecoveryProjection(t, got.room, f.other, f.owner, room.ID)
			} else if got.err == nil || got.room.ID != "" {
				t.Fatal("revoked bound proof survived wait", got)
			}
		})
	}
}
func directRecoveryMembershipControls(t *testing.T) {
	f := newChatRelationsFixture(t)
	room := relationDirect(t, f)
	seedDirectRecoveryPrivacy(t, f, room)
	// An unconfirmed recipient can change its own recovery state, but a remaining
	// canonical block must still redact the mutation response.
	if _, e := f.chats.ChangeDirect(f.contexts[f.other], f.other, room.ID, "block"); e != nil {
		t.Fatal(e)
	}
	if _, e := f.social.ChangeRelationship(f.contexts[f.owner], f.owner, f.other, social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	restored, e := f.chats.ChangeDirect(f.contexts[f.other], f.other, room.ID, "unblock")
	if e != nil {
		t.Fatal(e)
	}
	assertRecoveryProjection(t, restored, f.other, f.owner, room.ID)
	if restored.Members[0].ConfirmedAt != nil {
		t.Fatal("unblock invented confirmation")
	}
	query(t, f.db, `INSERT INTO chat_members(id,chat_room_id,account_id,notify,joined_at)VALUES($1,$2,$3,10,clock_timestamp())`, authorityUUID(t), room.ID, f.third)
	query(t, f.db, `UPDATE chat_members SET deleted_at=clock_timestamp() WHERE chat_room_id=$1 AND account_id=$2`, room.ID, f.other)
	deleted, e := f.chats.GetDirect(f.contexts[f.other], f.other, f.owner)
	if e != nil {
		t.Fatal(e)
	}
	assertRecoveryProjection(t, deleted, f.other, f.owner, room.ID)
	if own := deleted.Members[0]; own.ID != "" || own.JoinedAt != nil || own.LeaveAt != nil || own.ConfirmedAt != nil {
		t.Fatal("deleted membership was revived", own)
	}
	peer, e := f.chats.GetDirect(f.contexts[f.owner], f.owner, f.other)
	if e != nil {
		t.Fatal(e)
	}
	assertRecoveryProjection(t, peer, f.owner, f.other, room.ID)
	if _, e = f.chats.GetDirect(f.contexts[f.third], f.third, f.owner); !errors.Is(e, rooms.ErrNotFound) {
		t.Fatal("unrelated member acquired pair recovery", e)
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.other)
	peer, e = f.chats.GetDirect(f.contexts[f.owner], f.owner, f.other)
	if e != nil {
		t.Fatal(e)
	}
	assertRecoveryProjection(t, peer, f.owner, f.other, room.ID)
	if v, e := f.chats.GetDirect(f.contexts[f.other], f.other, f.owner); e == nil || v.ID != "" {
		t.Fatal("disabled actor recovered room", v, e)
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=false WHERE id=$1`, f.other)
	query(t, f.db, `UPDATE chat_rooms SET owned_owner_id=$2 WHERE id=$1`, room.ID, f.third)
	if v, e := f.chats.GetDirect(f.contexts[f.owner], f.owner, f.other); !errors.Is(e, rooms.ErrConflict) || v.ID != "" {
		t.Fatal("corrupt owner escaped pair", v, e)
	}
	query(t, f.db, `UPDATE chat_rooms SET owned_owner_id=$2,deleted_at=clock_timestamp() WHERE id=$1`, room.ID, f.owner)
	if _, e = f.chats.GetDirect(f.contexts[f.owner], f.owner, f.other); !errors.Is(e, rooms.ErrNotFound) {
		t.Fatal("deleted room recovered", e)
	}
}
func TestPGDirectRecoveryPrivacyRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	const marker = "direct-recovery-privacy.json"
	if phase == "before" {
		directRecoveryWaitControls(t)
		t.Run("membership_and_mutation_recovery", directRecoveryMembershipControls)
		f := newChatRelationsFixture(t)
		room := relationDirect(t, f)
		if _, e := f.chats.ChangeDirect(f.contexts[f.other], f.other, room.ID, "confirm"); e != nil {
			t.Fatal(e)
		}
		message, group := seedDirectRecoveryPrivacy(t, f, room)
		visible, e := f.chats.GetDirect(f.contexts[f.other], f.other, f.owner)
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := json.Marshal(visible)
		if !strings.Contains(string(raw), "synthetic-private-bio-"+f.owner) || !strings.Contains(string(raw), "synthetic-private-location-"+f.owner) || visible.MLSGroupID == nil || *visible.MLSGroupID != group {
			t.Fatal("positive private profile/MLS fixture missing")
		}
		positive, e := f.read.Sync(f.contexts[f.other], messager.SyncQuery{RoomID: room.ID, AccountID: f.other})
		if e != nil || len(positive.Messages) != 1 || positive.Messages[0].ID != message {
			t.Fatal("positive message sync fixture missing", positive, e)
		}
		block, e := f.social.ChangeRelationship(f.contexts[f.owner], f.owner, f.other, social.RelationshipChange{Action: social.Block, ExpiresIn: expiryString("30d")})
		if e != nil || block.ExpiredAt == nil {
			t.Fatal(block, e)
		}
		state := directRecoverySaved{Owner: f.owner, Peer: f.other, Room: room.ID, Block: block.ID, Message: message, Group: group}
		// Save the actual persisted microsecond value, not the pre-write Go timestamp.
		if e = f.db.QueryRow(`SELECT expired_at FROM owned_social_relationships WHERE id=$1`, block.ID).Scan(&state.Deadline); e != nil {
			t.Fatal(e)
		}
		for _, m := range visible.Members {
			saved := directRecoveryMemberSaved{m.ID, m.AccountID, m.JoinedAt, m.LeaveAt, m.ConfirmedAt}
			if m.AccountID == f.owner {
				state.OwnerState = saved
			} else if m.AccountID == f.other {
				state.PeerState = saved
			}
		}
		if state.OwnerState.ID == "" || state.PeerState.ID == "" || state.OwnerState.Account != f.owner || state.PeerState.Account != f.other {
			t.Fatal("persisted self-state controls missing", state)
		}
		assertDirectRecoveryPrivacy(t, f.chats, f.read, state, f.contexts)
		for _, p := range []struct{ token, target string }{{f.token, f.other}, {f.otherToken, f.owner}} {
			response := chatCall(t, f.roomHTTP, "GET", "/api/chat/direct/"+p.target, p.token, "", 200)
			if strings.Contains(response.Body.String(), "synthetic-private-") {
				t.Fatal("HTTP recovery disclosure", response.Body.String())
			}
			chatCall(t, f.roomHTTP, "GET", "/api/chat/"+room.ID+"/messages", p.token, "", 403)
			chatCall(t, f.roomHTTP, "POST", "/api/chat/"+room.ID+"/sync", p.token, `{"last_sync_timestamp":0}`, 403)
		}
		saveRestartFixture(t, marker, state)
		return
	}
	var state directRecoverySaved
	loadRestartFixture(t, marker, &state)
	var deadline time.Time
	if e := db.QueryRow(`SELECT expired_at FROM owned_social_relationships WHERE id=$1`, state.Block).Scan(&deadline); e != nil || !deadline.Equal(state.Deadline) {
		t.Fatal("persisted timed block deadline changed", deadline, state.Deadline, e)
	}
	var epoch, version int
	var info, tree string
	if e := db.QueryRow(`SELECT epoch,state_version,group_info,ratchet_tree FROM owned_mls_groups WHERE id=$1 AND room_id=$2`, state.Group, state.Room).Scan(&epoch, &version, &info, &tree); e != nil || epoch != 17 || version != 19 || info != "synthetic-private-key-info" || tree != "synthetic-private-key-tree" {
		t.Fatal("opaque private fixture changed across restart", e)
	}
	if n := commerceCount(t, db, `SELECT count(*) FROM owned_authority_contacts WHERE account_id IN($1,$2) AND content LIKE 'synthetic-private-contact-%'`, state.Owner, state.Peer); n != 2 {
		t.Fatal("private contact controls missing after restart", n)
	}
	chat, read := directRecoveryRepositories(t, db)
	assertDirectRecoveryPrivacy(t, chat, read, state, nil)
	query(t, db, `UPDATE owned_social_relationships SET expired_at=clock_timestamp()-interval '1 second' WHERE id=$1`, state.Block)
	for _, pair := range [][2]string{{state.Owner, state.Peer}, {state.Peer, state.Owner}} {
		v, e := chat.GetDirect(context.Background(), pair[0], pair[1])
		if e != nil || v.RecoveryOnly || v.MLSGroupID == nil || *v.MLSGroupID != state.Group {
			t.Fatal("expired block retained recovery-only projection", v, e)
		}
		page, e := read.ListMessages(context.Background(), messager.MessageListQuery{RoomID: state.Room, AccountID: pair[0], Take: 20})
		if e != nil || len(page.Messages) != 1 || page.Messages[0].ID != state.Message {
			t.Fatal("expired block retained message denial", page, e)
		}
		synced, e := read.Sync(context.Background(), messager.SyncQuery{RoomID: state.Room, AccountID: pair[0]})
		if e != nil || len(synced.Messages) != 1 || synced.Messages[0].ID != state.Message {
			t.Fatal("expired block retained sync denial", synced, e)
		}
	}
	var friends bool
	if e := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM owned_social_effective_relationships WHERE account_id IN($1,$2) AND related_id IN($1,$2) AND status>=100)`, state.Owner, state.Peer).Scan(&friends); e != nil || friends {
		t.Fatal("expiry invented friendship", friends, e)
	}
}
