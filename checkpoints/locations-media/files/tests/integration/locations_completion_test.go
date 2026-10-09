// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"errors"
	"os"
	"solsynth.local/go-migration/internal/locationpg"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/internal/mediastore"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/locations"
	"strings"
	"testing"
	"time"
)

type syntheticLocationGeocoder func(context.Context, locations.GeocodeRequest) ([]locations.GeocodePlace, error)

func (g syntheticLocationGeocoder) Geocode(ctx context.Context, v locations.GeocodeRequest) ([]locations.GeocodePlace, error) {
	return g(ctx, v)
}
func TestPGLocationsConfiguredGeocodingRateAndOriginalProof(t *testing.T) {
	f := newLocationFixture(t, 4650)
	v := locations.GeocodeRequest{Query: "synthetic place", Language: "en"}
	if _, e := f.r.Geocode(f.contexts[0], f.actors[0], v); !errors.Is(e, locations.ErrUnavailable) {
		t.Fatal("unconfigured geocoding", e)
	}
	f.r.WithGeocoder(syntheticLocationGeocoder(func(ctx context.Context, got locations.GeocodeRequest) ([]locations.GeocodePlace, error) {
		if got != v {
			t.Fatal(got)
		}
		return []locations.GeocodePlace{{Name: "synthetic", Longitude: 10, Latitude: 20}}, nil
	}))
	places, e := f.r.Geocode(f.contexts[0], f.actors[0], v)
	if e != nil || len(places) != 1 || places[0].Longitude != 10 {
		t.Fatal(places, e)
	}
	if _, e = f.r.Geocode(f.contexts[0], f.actors[0], v); !errors.Is(e, locations.ErrRateLimit) {
		t.Fatal("provider request rate bypass", e)
	}
	query(t, f.db, `UPDATE owned_location_geocode_limits SET last_at=clock_timestamp()-interval '6 seconds' WHERE account_id=$1`, f.actors[0])
	started, release := make(chan struct{}), make(chan struct{})
	f.r.WithGeocoder(syntheticLocationGeocoder(func(context.Context, locations.GeocodeRequest) ([]locations.GeocodePlace, error) {
		close(started)
		<-release
		return places, nil
	}))
	result := make(chan error, 1)
	go func() { _, e := f.r.Geocode(f.contexts[0], f.actors[0], v); result <- e }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start")
	}
	query(t, f.db, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE account_id=$1`, f.actors[0])
	close(release)
	select {
	case e := <-result:
		if e == nil {
			t.Fatal("provider wait substituted fresh authority")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("provider proof check did not finish")
	}
}

func TestPGLocationsDurableReplayCurrentAudience(t *testing.T) {
	f := newLocationFixture(t, 4640)
	m := locationMeet(t, f, locations.Unlisted)
	locationFriends(t, f, 0, 1)
	if _, e := f.r.Join(f.contexts[1], f.actors[1], m.ID, nil); e != nil {
		t.Fatal(e)
	}
	events, cursor, e := f.r.LocationEvents(f.contexts[0], f.actors[0], m.ID, false, nil, 0, 0)
	if e != nil || len(events) != 0 || cursor == 0 {
		t.Fatal("durable snapshot cursor", events, cursor, e)
	}
	p, e := f.r.PutPin(f.contexts[1], f.actors[1], m.ID, "", locations.PinRequest{Visibility: postInt(locations.Private), LocationWKT: postString("POINT(10 20)")})
	if e != nil {
		t.Fatal(e)
	}
	events, _, e = f.r.LocationEvents(f.contexts[0], f.actors[0], m.ID, false, nil, cursor, 100)
	if e != nil || len(events) != 0 {
		t.Fatal("private child-pin activity leaked to host", events, e)
	}
	events, ownCursor, e := f.r.LocationEvents(f.contexts[1], f.actors[1], m.ID, false, nil, cursor, 100)
	if e != nil || len(events) != 1 || events[0].Type != "pin_updated" || ownCursor <= cursor {
		t.Fatal("own pin transition missing", p, events, ownCursor, e)
	}
	if _, e = f.r.Complete(f.contexts[0], f.actors[0], m.ID); e != nil {
		t.Fatal(e)
	}
	// A fresh repository has no process-local replay buffer to recover from.
	reopened := locationpg.NewWithAuthority(f.db, f.g)
	events, next, e := reopened.LocationEvents(f.contexts[1], f.actors[1], m.ID, false, nil, ownCursor, 100)
	if e != nil || len(events) != 1 || events[0].Type != "completed" || next <= ownCursor {
		t.Fatal("durable completion replay", events, next, e)
	}
	if _, _, e = reopened.LocationEvents(f.contexts[2], f.actors[2], m.ID, false, nil, 0, 100); !errors.Is(e, locations.ErrNotFound) {
		t.Fatal("foreign replay granted historical audience", e)
	}
	if _, e = socialpg.New(f.db).ChangeRelationship(context.Background(), f.actors[0], f.actors[1], social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	if _, _, e = reopened.LocationEvents(f.contexts[1], f.actors[1], m.ID, false, nil, ownCursor, 100); !errors.Is(e, locations.ErrNotFound) {
		t.Fatal("replay survived current block", e)
	}
}

func TestPGNearbyPrivacyRotationExpiryAndRevocation(t *testing.T) {
	f := newLocationFixture(t, 4610)
	tokens, e := f.r.IssuePresenceTokens(f.contexts[0], f.actors[0], locations.PresenceRequest{DeviceID: "synthetic-device", PrefetchSlots: 2})
	if e != nil || len(tokens.Tokens) != 2 || len(tokens.Tokens[0].Token) != 32 {
		t.Fatal(tokens, e)
	}
	o := locations.Observation{Token: tokens.Tokens[0].Token, Slot: tokens.Tokens[0].Slot, AvgRSSI: -50, SeenCount: 2, DurationMS: 100}
	peers, e := f.r.ResolveNearby(f.contexts[1], f.actors[1], []locations.Observation{o})
	if e != nil || len(peers) != 0 {
		t.Fatal("default friend-only leak", peers, e)
	}
	locationFriends(t, f, 0, 1)
	query(t, f.db, `UPDATE owned_nearby_limits SET last_at=clock_timestamp()-interval '6 seconds'`)
	peers, e = f.r.ResolveNearby(f.contexts[1], f.actors[1], []locations.Observation{o, o})
	if e != nil || len(peers) != 1 || peers[0].UserID != f.actors[0] || !peers[0].IsFriend || peers[0].CanInvite {
		t.Fatal(peers, e)
	}
	if _, e = f.r.IssuePresenceTokens(f.contexts[0], f.actors[0], locations.PresenceRequest{DeviceID: "synthetic-device"}); !errors.Is(e, locations.ErrRateLimit) {
		t.Fatal("issue rate bypass", e)
	}
	query(t, f.db, `UPDATE owned_nearby_limits SET last_at=clock_timestamp()-interval '6 seconds'`)
	tokens2, e := f.r.IssuePresenceTokens(f.contexts[0], f.actors[0], locations.PresenceRequest{DeviceID: "synthetic-device", FriendOnly: boolean(false)})
	if e != nil || tokens2.Tokens[0].Token == o.Token {
		t.Fatal("nonrotating token", tokens2, e)
	}
	peers, e = f.r.ResolveNearby(f.contexts[1], f.actors[1], []locations.Observation{o})
	if e != nil || len(peers) != 0 {
		t.Fatal("old device tokens survived rotation", peers, e)
	}
	query(t, f.db, `UPDATE owned_nearby_limits SET last_at=clock_timestamp()-interval '6 seconds'`)
	o.Token = tokens2.Tokens[0].Token
	o.Slot = tokens2.Tokens[0].Slot
	peers, e = f.r.ResolveNearby(f.contexts[2], f.actors[2], []locations.Observation{o})
	if e != nil || len(peers) != 1 || peers[0].IsFriend || !peers[0].CanInvite {
		t.Fatal("discoverable mode", peers, e)
	}
	query(t, f.db, `UPDATE owned_nearby_limits SET last_at=clock_timestamp()-interval '6 seconds';UPDATE owned_nearby_tokens SET valid_to=valid_from+interval '1 millisecond'`)
	peers, e = f.r.ResolveNearby(f.contexts[2], f.actors[2], []locations.Observation{o})
	if e != nil || len(peers) != 0 {
		t.Fatal("expired peer leaked", peers, e)
	}
	query(t, f.db, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE account_id=$1`, f.actors[0])
	if _, e = f.r.IssuePresenceTokens(f.contexts[0], f.actors[0], locations.PresenceRequest{DeviceID: "synthetic-device"}); e == nil {
		t.Fatal("revoked original proof reused")
	}
}

func TestPGLocationsImageOwnershipAndParentAudience(t *testing.T) {
	f := newLocationFixture(t, 4630)
	root := t.TempDir()
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	store, e := mediastore.NewFilesystem(root, 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	mr, e := mediapg.New(f.db, store, f.g, mediapg.Config{MaxFileBytes: 1 << 20, PendingTTL: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	f.r.WithMedia()
	mr.WithReadVisibility(f.r)
	for _, id := range f.actors {
		query(t, f.db, `INSERT INTO owned_media_quotas(account_id,limit_bytes) VALUES($1,65536)`, id)
	}
	u := mediaUpload("meet-image", "synthetic image", false)
	u.MIMEType = "image/png"
	pic, _, e := mr.Upload(f.contexts[0], f.actors[0], u, strings.NewReader("synthetic image"))
	if e != nil {
		t.Fatal(e)
	}
	m, e := f.r.CreateMeet(f.contexts[0], f.actors[0], locations.CreateMeet{Visibility: postInt(locations.Unlisted), ImageID: &pic.ID})
	if e != nil || m.Image == nil {
		t.Fatal(m, e)
	}
	if _, e = mr.Info(f.contexts[1], f.actors[1], pic.ID); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("unlisted meet grants foreign file", e)
	}
	locationFriends(t, f, 0, 1)
	if _, e = mr.Info(f.contexts[1], f.actors[1], pic.ID); e != nil {
		t.Fatal("canonical friend cannot read meet image", e)
	}
	if _, e = f.r.CreateMeet(f.contexts[1], f.actors[1], locations.CreateMeet{ImageID: &pic.ID}); !errors.Is(e, locations.ErrForbidden) {
		t.Fatal("read grant promoted to attachment ownership", e)
	}
	if _, e = socialpg.New(f.db).ChangeRelationship(context.Background(), f.actors[0], f.actors[1], social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	if _, e = mr.Info(f.contexts[1], f.actors[1], pic.ID); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("blocked parent grant remains", e)
	}
	if e = f.r.DeleteMeet(f.contexts[0], f.actors[0], m.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = mr.Info(f.contexts[2], f.actors[2], pic.ID); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("deleted parent exposes image", e)
	}
}

type syntheticNFCLocker struct{ calls int }

func (s *syntheticNFCLocker) Lock(context.Context, string, string) error { s.calls++; return nil }
func TestPGEncryptedNFCSUNClaimPrivacyReplayAndHardwareLock(t *testing.T) {
	f := newLocationFixture(t, 4620)
	locker := &syntheticNFCLocker{}
	if e := f.r.WithEncryptedNFC(make([]byte, 32), locker); e != nil {
		t.Fatal(e)
	}
	tag, e := f.r.ProvisionEncryptedTag(f.contexts[0], f.actors[0], locations.ProvisionTag{UID: "04958CAA5C5E80", SunKey: "00000000000000000000000000000000", MACProfile: "encrypted-file"})
	if e != nil || !tag.Encrypted || tag.AccountID != nil {
		t.Fatal(tag, e)
	}
	v := locations.SUNRequest{RecordID: tag.ID, PICCData: "FD91EC264309878BE6345CBE53BADF40", Encrypted: "CEE9A53E3E463EF1F459635736738962", CMAC: "ECC1E7F6C6C73BF6"}
	bad := v
	bad.CMAC = "ECC1E7F6C6C73BF7"
	if _, e = f.r.ResolveSUN(f.contexts[1], f.actors[1], bad, true); e == nil {
		t.Fatal("invalid MAC claimed")
	}
	claimed, e := f.r.ResolveSUN(f.contexts[1], f.actors[1], v, true)
	if e != nil || claimed.Account.ID != f.actors[1] || !claimed.Claimed {
		t.Fatal(claimed, e)
	}
	if _, e = f.r.ResolveSUN(f.contexts[1], f.actors[1], v, true); !errors.Is(e, locations.ErrConflict) {
		t.Fatal("equal read counter replay", e)
	}
	if _, e = f.r.ResolveTag(context.Background(), "", tag.UID, false); !errors.Is(e, locations.ErrNotFound) {
		t.Fatal("encrypted profile exposed by plain UID", e)
	}
	if _, e = f.r.LockEncryptedTag(f.contexts[2], f.actors[2], tag.ID); !errors.Is(e, locations.ErrNotFound) {
		t.Fatal("foreign physical lock", e)
	}
	locked, e := f.r.LockEncryptedTag(f.contexts[1], f.actors[1], tag.ID)
	if e != nil || !locked.Locked || locker.calls != 1 {
		t.Fatal(locked, e, locker.calls)
	}
	if _, e = f.r.LockEncryptedTag(f.contexts[1], f.actors[1], tag.ID); e != nil || locker.calls != 1 {
		t.Fatal("idempotent hardware lock", e, locker.calls)
	}
	listed, e := f.r.Tags(f.contexts[1], f.actors[1])
	if e != nil || len(listed) != 1 || listed[0].LastSeenAt == nil || listed[0].LastSeenAt.After(time.Now()) {
		t.Fatal(listed, e)
	}
	if e = f.r.DeleteTag(f.contexts[1], f.actors[1], tag.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.UpdateTag(f.contexts[1], f.actors[1], tag.ID, locations.TagPatch{Active: boolean(true)}); !errors.Is(e, locations.ErrNotFound) {
		t.Fatal("deleted tag resurrected", e)
	}
}
