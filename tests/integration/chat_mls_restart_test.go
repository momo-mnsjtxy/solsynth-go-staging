// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"solsynth.local/go-migration/ports/messager/rooms"
)

func TestPGMLSRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	file := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "owned-mls-restart.json")
	var saved struct {
		Owner, Other, Third, OwnerDevice, OtherDevice, ThirdDevice, Group, Room, Envelope string
		Sequence                                                                          int64
	}
	if phase == "before" {
		f, room, group := mlsFixture(t)
		mlsExternalJoin(t, f, room, group)
		ownerDevice, otherDevice, thirdDevice := mlsDeviceName(f, "device-owner"), mlsDeviceName(f, "device-other"), mlsDeviceName(f, "device-stranger")
		pending := mlsDo(t, f, f.owner, ownerDevice, "pending", "", rooms.MLSRequest{}).([]rooms.MLSEnvelope)
		if len(pending) != 1 {
			t.Fatal("restart fixture pending count", len(pending))
		}
		claims := mlsDo(t, f, f.owner, ownerDevice, "claim", f.other, rooms.MLSRequest{}).([]map[string]any)
		if len(claims) != 1 {
			t.Fatal("restart fixture package claim", len(claims))
		}
		mlsDo(t, f, f.third, thirdDevice, "revoke", thirdDevice, rooms.MLSRequest{})
		saved.Owner, saved.Other, saved.Third = f.owner, f.other, f.third
		saved.OwnerDevice, saved.OtherDevice, saved.ThirdDevice = ownerDevice, otherDevice, thirdDevice
		saved.Group, saved.Room, saved.Envelope, saved.Sequence = group, room.ID, pending[0].ID, pending[0].Sequence
		b, err := json.Marshal(saved)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(file, b, 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	if phase != "after" {
		t.Fatal("restart test requires before/after phase")
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	r := chatRepo(t, db).WithMLSDelivery()
	ctx := context.Background()
	if err = r.MLSReady(ctx); err != nil {
		t.Fatal(err)
	}
	v, err := r.MLS(ctx, saved.Owner, saved.OwnerDevice, "pending", "", rooms.MLSRequest{})
	if err != nil {
		t.Fatal(err)
	}
	pending := v.([]rooms.MLSEnvelope)
	if len(pending) != 1 || pending[0].ID != saved.Envelope || pending[0].Sequence != saved.Sequence || pending[0].Epoch != 1 {
		t.Fatal("database restart changed opaque queue", pending)
	}
	v, err = r.MLS(ctx, saved.Owner, saved.OwnerDevice, "claim", saved.Other, rooms.MLSRequest{})
	if err != nil || len(v.([]map[string]any)) != 0 {
		t.Fatal("database restart replenished consumed package", v, err)
	}
	if _, err = r.MLS(ctx, saved.Third, saved.ThirdDevice, "publish", "", rooms.MLSRequest{DeviceID: saved.ThirdDevice, KeyPackage: mlsBytes("revoked-after-restart")}); !errors.Is(err, rooms.ErrForbidden) {
		t.Fatal("database restart revived device", err)
	}
	if _, err = r.MLS(ctx, saved.Other, saved.OtherDevice, "ack", saved.Envelope, rooms.MLSRequest{}); !errors.Is(err, rooms.ErrNotFound) {
		t.Fatal("foreign device ack after restart", err)
	}
	if _, err = r.MLS(ctx, saved.Other, saved.OtherDevice, "commit-fanout", saved.Group, mlsCommitReq(1, "external-join")); err != nil {
		t.Fatal("durable commit retry lost", err)
	}
	v, err = r.MLS(ctx, saved.Owner, saved.OwnerDevice, "pending", "", rooms.MLSRequest{})
	if err != nil || len(v.([]rooms.MLSEnvelope)) != 1 {
		t.Fatal("restart retry duplicated envelope", v, err)
	}
	if _, err = r.MLS(ctx, saved.Owner, saved.OwnerDevice, "ack", saved.Envelope, rooms.MLSRequest{}); err != nil {
		t.Fatal(err)
	}
	v, err = r.MLS(ctx, saved.Owner, saved.OwnerDevice, "pending", "", rooms.MLSRequest{})
	if err != nil || len(v.([]rooms.MLSEnvelope)) != 0 {
		t.Fatal("ack not persisted after restart", v, err)
	}
}
