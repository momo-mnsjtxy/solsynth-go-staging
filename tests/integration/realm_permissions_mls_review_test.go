// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/passport/social"
)

// Exercise the new realm policy through the parent continuation's opaque MLS
// paths, including a room creator who loses the delegated realm permission.
func TestPGRealmPermissionMLSOpaqueControlAndSendRevocation(t *testing.T) {
	f := newRealmPublishingFixture(t)
	query(t, f.db, chatpg.MLSSchema)
	query(t, f.db, `UPDATE owned_realms SET boost_level=2 WHERE id=$1`, f.private.ID)
	if _, e := f.social.SetRolePermission(f.ctx, f.actor, f.private.ID, realmRoleRequest(21, 47)); e != nil {
		t.Fatal(e)
	}
	if _, e := f.social.ChangeMember(f.ctx, f.actor, f.private.ID, social.MemberChange{Action: social.ChangeRole, AccountID: f.other, Role: 21}); e != nil {
		t.Fatal(e)
	}
	chat, e := chatpg.NewWithAuthority(f.db, chatPolicy, f.guard)
	if e != nil {
		t.Fatal(e)
	}
	chat.WithSocial(f.social).WithMLSDelivery()
	room, e := chat.Create(f.otherCtx, f.other, rooms.RoomPatch{Name: postString("Realm MLS permission"), RealmID: &f.private.ID, IsPublic: boolean(true), IsCommunity: boolean(true)})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = chat.ChangeMember(f.ctx, f.actor, room.ID, rooms.Join, ""); e != nil {
		t.Fatal(e)
	}
	ownerDevice, memberDevice := "permission-owner-"+f.actor, "permission-member-"+f.other
	call := func(ctx context.Context, actor, device, op, target string, p rooms.MLSRequest) any {
		t.Helper()
		out, e := chat.MLS(ctx, actor, device, op, target, p)
		if e != nil {
			t.Fatalf("MLS %s: %v", op, e)
		}
		return out
	}
	call(f.ctx, f.actor, ownerDevice, "publish", "", rooms.MLSRequest{DeviceID: ownerDevice, KeyPackage: mlsBytes("permission-owner")})
	call(f.otherCtx, f.other, memberDevice, "publish", "", rooms.MLSRequest{DeviceID: memberDevice, KeyPackage: mlsBytes("permission-member")})
	call(f.otherCtx, f.other, "", "enable", room.ID, rooms.MLSRequest{})
	group := "room:" + room.ID
	call(f.otherCtx, f.other, memberDevice, "bootstrap", group, rooms.MLSRequest{Epoch: mlsInt(0), StateVersion: mlsInt(1), Meta: map[string]string{"bootstrap_device_id": memberDevice}})
	call(f.otherCtx, f.other, memberDevice, "groupinfo-put", group, rooms.MLSRequest{Epoch: mlsInt(0), GroupInfo: mlsBytes("info"), RatchetTree: mlsBytes("tree")})
	call(f.ctx, f.actor, ownerDevice, "groupinfo", group, rooms.MLSRequest{})
	call(f.ctx, f.actor, ownerDevice, "commit-fanout", group, mlsCommitReq(1, "permission-owner-join"))
	call(f.ctx, f.actor, ownerDevice, "joined", room.ID, rooms.MLSRequest{Epoch: mlsInt(1)})
	call(f.ctx, f.actor, ownerDevice, "groupinfo-put", group, rooms.MLSRequest{Epoch: mlsInt(1), GroupInfo: mlsBytes("current-info"), RatchetTree: mlsBytes("current-tree")})
	prior := call(f.otherCtx, f.other, memberDevice, "pending", "", rooms.MLSRequest{}).([]rooms.MLSEnvelope)
	if len(prior) != 1 {
		t.Fatal("expected persisted owner commit", prior)
	}
	deny := false
	if _, e = f.social.SetUserPermission(f.ctx, f.actor, f.private.ID, social.UserPermissionRequest{AccountID: f.other, PermissionOverrides: social.PermissionOverrides{CanChat: &deny, CanModerateChat: &deny}}); e != nil {
		t.Fatal(e)
	}
	meta := &rooms.EncryptionMeta{Scheme: "chat.mls.v2", Ciphertext: mlsBytes("forbidden-message"), Header: mlsBytes("header"), Epoch: 1}
	if _, e = chat.Send(f.otherCtx, f.other, room.ID, rooms.TextRequest{EncryptionMeta: meta}); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("canonical opaque send ignored realm revocation", e)
	}
	payload, e := json.Marshal(map[string]any{"type": "text", "content": nil, "client_message_id": "permission-denied", "encryption_meta": meta})
	if e != nil {
		t.Fatal(e)
	}
	for _, attempt := range []struct {
		op, target string
		request    rooms.MLSRequest
	}{
		{"application", "", rooms.MLSRequest{RoomID: group, Payload: payload}},
		{"commit-fanout", group, mlsCommitReq(2, "permission-denied-commit")},
		{"welcome", group, rooms.MLSRequest{RecipientAccountID: f.actor, Payloads: []rooms.MLSPayload{{RecipientDeviceID: ownerDevice, Ciphertext: mlsBytes("welcome"), Header: mlsBytes("header"), ClientMessageID: "permission-denied-welcome"}}}},
		{"reset", group, rooms.MLSRequest{ResetCapable: true, Generation: mlsInt(1), NewEpoch: mlsInt(0), StateVersion: mlsInt(2), Reason: "permission-denied-reset"}},
		{"enable", room.ID, rooms.MLSRequest{}},
	} {
		if _, e = chat.MLS(f.otherCtx, f.other, memberDevice, attempt.op, attempt.target, attempt.request); !errors.Is(e, rooms.ErrForbidden) {
			t.Fatalf("room creator bypassed current permission for %s: %v", attempt.op, e)
		}
	}
	var epoch, generation int64
	var requests int
	if e = f.db.QueryRow(`SELECT epoch,state_version,(SELECT count(*) FROM owned_mls_requests WHERE group_id=$1) FROM owned_mls_groups WHERE id=$1`, group).Scan(&epoch, &generation, &requests); e != nil || epoch != 1 || generation != 1 || requests != 1 {
		t.Fatal("denied operation changed opaque control state", epoch, generation, requests, e)
	}
	// A write denial retains the ordinary active member's existing read access.
	after := call(f.otherCtx, f.other, memberDevice, "pending", "", rooms.MLSRequest{}).([]rooms.MLSEnvelope)
	if len(after) != 1 || after[0].ID != prior[0].ID || after[0].Sequence != prior[0].Sequence {
		t.Fatal("write denial changed existing delivery", prior, after)
	}
	call(f.otherCtx, f.other, memberDevice, "groupinfo", group, rooms.MLSRequest{})
}
