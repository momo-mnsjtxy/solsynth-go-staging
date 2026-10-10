// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"image"
	"image/png"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/locationpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/sphere/locations"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func reviewDatabaseDeadline(t *testing.T, db *sql.DB, deadline time.Time) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var reached bool
		if e := db.QueryRowContext(ctx, `SELECT clock_timestamp()>=$1::timestamptz`, deadline).Scan(&reached); e != nil {
			t.Fatal(e)
		}
		if reached {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick.C:
		}
	}
}
func reviewDeadline(t *testing.T, db *sql.DB) time.Time {
	t.Helper()
	var v time.Time
	if e := db.QueryRowContext(context.Background(), `SELECT clock_timestamp()+interval '2 seconds'`).Scan(&v); e != nil {
		t.Fatal(e)
	}
	return v
}
func reviewLocks(t *testing.T, db *sql.DB) atomicAuthorityFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return atomicAuthorityFixture{db: db, ctx: ctx}
}
func reviewRealLocationClock(t *testing.T, f locationFixture) locationFixture {
	t.Helper()
	now := &atomic.Int64{}
	now.Store(time.Now().Unix())
	cfg := authorityConfig(t, authorityKeys(t), now)
	cfg.Now = time.Now
	var e error
	f.service, e = authority.New(authoritypg.New(f.db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	f.v, e = authority.NewVerifier(authoritypg.New(f.db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	f.g = authoritypg.NewTransactionGuard(authoritypg.New(f.db), f.v)
	f.r = locationpg.NewWithAuthority(f.db, f.g)
	f.contexts = nil
	f.tokens = nil
	for _, id := range f.actors {
		token := pgIssue(t, f.service, id).Token
		f.tokens = append(f.tokens, token)
		f.contexts = append(f.contexts, locationProof(t, f, token))
	}
	return f
}

func TestPGNearbyDatabaseClockAcrossInstancesAndRateSerialization(t *testing.T) {
	f := newLocationFixture(t, 4660)
	ahead := locationpg.NewWithAuthorityAndClock(f.db, f.g, func() time.Time { return time.Now().Add(48 * time.Hour) })
	behind := locationpg.NewWithAuthorityAndClock(f.db, f.g, func() time.Time { return time.Now().Add(-48 * time.Hour) })
	type result struct {
		tokens locations.PresenceTokens
		e      error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for _, repo := range []*locationpg.Repository{ahead, behind} {
		go func(r *locationpg.Repository) {
			<-start
			v, e := r.IssuePresenceTokens(f.contexts[0], f.actors[0], locations.PresenceRequest{DeviceID: "same", FriendOnly: boolean(false)})
			results <- result{v, e}
		}(repo)
	}
	close(start)
	wins, limited := 0, 0
	var tokens locations.PresenceTokens
	for i := 0; i < 2; i++ {
		v := <-results
		if v.e == nil {
			wins++
			tokens = v.tokens
		} else if errors.Is(v.e, locations.ErrRateLimit) {
			limited++
		} else {
			t.Fatal(v.e)
		}
	}
	if wins != 1 || limited != 1 || len(tokens.Tokens) != 3 {
		t.Fatal(wins, limited, tokens)
	}
	var last time.Time
	if e := f.db.QueryRowContext(context.Background(), `SELECT last_at FROM owned_nearby_limits WHERE account_id=$1 AND action='issue'`, f.actors[0]).Scan(&last); e != nil {
		t.Fatal(e)
	}
	if tokens.Tokens[0].Slot != last.Unix()/60 || !tokens.Tokens[0].ValidFrom.Equal(time.Unix(last.Unix()/60*60, 0)) {
		t.Fatal("instance clock selected slot", tokens, last)
	}
	stale := last.Add(-10 * time.Minute)
	observation := locations.Observation{Token: tokens.Tokens[0].Token, Slot: tokens.Tokens[0].Slot, SeenCount: 1, AvgRSSI: -50, LastSeenAt: &stale}
	if _, e := ahead.ResolveNearby(f.contexts[1], f.actors[1], []locations.Observation{observation}); !errors.Is(e, locations.ErrInvalid) {
		t.Fatal("instance clock changed observation freshness", e)
	}
	observation.LastSeenAt = nil
	peers, e := behind.ResolveNearby(f.contexts[1], f.actors[1], []locations.Observation{observation})
	if e != nil || len(peers) != 1 {
		t.Fatal(peers, e)
	}
	if _, e = ahead.ResolveNearby(f.contexts[1], f.actors[1], []locations.Observation{observation}); !errors.Is(e, locations.ErrRateLimit) {
		t.Fatal("cross-instance rate bypass", e)
	}
}

func TestPGMediaBundleQuotaExpiryAfterActualLockWait(t *testing.T) {
	for _, kind := range []string{"bundle", "quota"} {
		t.Run(kind, func(t *testing.T) {
			f := newMediaFixture(t)
			f.config.Now = time.Now
			f = mediaPool(t, f, f.db)
			locks := reviewLocks(t, f.db)
			var table, id string
			var done = make(chan error, 1)
			var initial media.Bundle
			if kind == "bundle" {
				var e error
				initial, e = f.repo.SaveBundle(f.ctx, f.actor, "", media.BundleRequest{Name: "before", Public: true})
				if e != nil {
					t.Fatal(e)
				}
				table, id = "owned_media_bundles", initial.ID
			} else {
				if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
					t.Fatal(e)
				}
				table, id = "owned_media_quotas", f.other
			}
			// quota's primary key is account_id, so use an explicit fixture lock.
			var blocker *sql.Tx
			var pid int
			if kind == "bundle" {
				blocker, pid = atomicBlocker(t, locks, table, id)
			} else {
				var e error
				blocker, e = f.db.BeginTx(locks.ctx, nil)
				if e != nil {
					t.Fatal(e)
				}
				t.Cleanup(func() { blocker.Rollback() })
				if e = blocker.QueryRowContext(locks.ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
					t.Fatal(e)
				}
				if _, e = blocker.ExecContext(locks.ctx, `SELECT account_id FROM owned_media_quotas WHERE account_id=$1 FOR UPDATE`, id); e != nil {
					t.Fatal(e)
				}
			}
			expiry := reviewDeadline(t, f.db)
			go func() {
				if kind == "bundle" {
					_, e := f.repo.SaveBundle(f.ctx, f.actor, id, media.BundleRequest{Name: "must rollback", Public: true, ExpiredAt: &expiry})
					done <- e
				} else {
					_, e := f.repo.AddQuotaRecord(f.ctx, f.actor, media.QuotaRecord{AccountID: f.other, Name: "expired while waiting", SourceKey: "wait-expiry", QuotaBytes: 100, ExpiredAt: &expiry})
					done <- e
				}
			}()
			waitAtomicBlocked(t, locks, pid, table)
			reviewDatabaseDeadline(t, f.db, expiry)
			if e := blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e := <-done; !errors.Is(e, media.ErrInvalid) {
				t.Fatal("expired TTL admitted after SQL wait", e)
			}
			if kind == "bundle" {
				got, e := f.repo.Bundle(f.ctx, f.actor, id, "")
				if e != nil || got.Name != initial.Name || got.ExpiredAt != nil {
					t.Fatal("expired bundle changed state", got, e)
				}
			} else {
				var n int
				var limit, used, reserved int64
				if e := f.db.QueryRowContext(locks.ctx, `SELECT limit_bytes,used_bytes,reserved_bytes FROM owned_media_quotas WHERE account_id=$1`, f.other).Scan(&limit, &used, &reserved); e != nil {
					t.Fatal(e)
				}
				if e := f.db.QueryRowContext(locks.ctx, `SELECT count(*) FROM owned_media_quota_records WHERE account_id=$1`, f.other).Scan(&n); e != nil {
					t.Fatal(e)
				}
				if limit != 65536 || used != 0 || reserved != 0 || n != 0 {
					t.Fatal("expired grant broke conservation", limit, used, reserved, n)
				}
			}
		})
	}
}

type reviewFileProtection struct{ denied string }

func (p reviewFileProtection) ProtectFileReadInTransaction(ctx context.Context, tx *sql.Tx, actor, id string) error {
	var owner string
	if e := tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_media_files WHERE id=$1`, id).Scan(&owner); e != nil {
		return e
	}
	if id == p.denied {
		return media.ErrNotFound
	}
	return nil
}
func TestPGMediaProtectedListCountsAndOffsets(t *testing.T) {
	f := newMediaFixture(t)
	ids := []string{}
	for _, name := range []string{"a", "b", "c"} {
		u := mediaUpload(name, name, true)
		u.Name = name
		v, _, e := f.repo.Upload(f.ctx, f.actor, u, strings.NewReader(name))
		if e != nil {
			t.Fatal(e)
		}
		ids = append(ids, v.ID)
	}
	f.repo.WithFileReadProtection(reviewFileProtection{denied: ids[0]})
	for offset := 0; offset < 3; offset++ {
		items, total, e := f.repo.ListFiles(f.ctx, f.actor, media.FileQuery{All: true, Order: "name", Take: 1, Offset: offset})
		if e != nil || total != 2 {
			t.Fatal(items, total, e)
		}
		if offset < 2 {
			if len(items) != 1 || items[0].ID != ids[offset+1] {
				t.Fatal("protected item influenced pagination", offset, items)
			}
		} else if len(items) != 0 {
			t.Fatal(items)
		}
	}
	if _, e := f.repo.FileRecord(f.ctx, f.actor, ids[0]); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("owner bypassed mandatory protection", e)
	}
	if _, e := f.repo.FileReferences(f.ctx, f.actor, ids[0]); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("references bypassed mandatory protection", e)
	}
	mediaUsage(t, f, 3, 0)
}

func TestPGNFCProvisionSortedLocksTombstoneAndScheduledDisable(t *testing.T) {
	f := newLocationFixture(t, 4670)
	if e := f.r.WithEncryptedNFC(make([]byte, 32), nil); e != nil {
		t.Fatal(e)
	}
	request := locations.ProvisionTag{UID: "04958CAA5C5E84", SunKey: strings.Repeat("0", 32), MACProfile: "encrypted-file", AssignedUserID: &f.actors[1]}
	tag, e := f.r.ProvisionEncryptedTag(f.contexts[0], f.actors[0], request)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.r.DeleteTag(f.contexts[1], f.actors[1], tag.ID); e != nil {
		t.Fatal(e)
	}
	restored, e := f.r.ProvisionEncryptedTag(f.contexts[0], f.actors[0], request)
	if e != nil || restored.ID != tag.ID || restored.AccountID == nil || *restored.AccountID != f.actors[1] {
		t.Fatal("UID tombstone not restored canonically", restored, e)
	}
	var n int
	if e = f.db.QueryRowContext(context.Background(), `SELECT count(*) FROM owned_encrypted_nfc_tags WHERE uid=$1`, request.UID).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at) VALUES($1,'disable_account',clock_timestamp()-interval '1 second')`, f.actors[1])
	request.UID = "04958CAA5C5E81"
	if _, e = f.r.ProvisionEncryptedTag(f.contexts[0], f.actors[0], request); !errors.Is(e, locations.ErrNotFound) {
		t.Fatal("assigned scheduled-disabled account admitted", e)
	}
	sun := locations.SUNRequest{RecordID: tag.ID, PICCData: "EE3FA5FF30ABADCCDCB00D748A5A03B9", Encrypted: "CEE9A53E3E463EF1F459635736738962", CMAC: "B2B87420FDEC29FA"}
	if _, e = f.r.ResolveSUN(f.contexts[2], f.actors[2], sun, false); !errors.Is(e, locations.ErrNotFound) {
		t.Fatal("disabled owner profile projected", e)
	}
	query(t, f.db, `DELETE FROM owned_authority_punishments WHERE account_id=$1`, f.actors[1])
	// Both administrators assign each other concurrently: complete account sets
	// must be sorted rather than actor-first, even though UID locks differ.
	outcomes := make(chan error, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		i := i
		go func() {
			<-start
			uid := []string{"04958CAA5C5E82", "04958CAA5C5E83"}[i]
			_, e := f.r.ProvisionEncryptedTag(f.contexts[i], f.actors[i], locations.ProvisionTag{UID: uid, SunKey: strings.Repeat("0", 32), AssignedUserID: &f.actors[1-i]})
			outcomes <- e
		}()
	}
	close(start)
	for i := 0; i < 2; i++ {
		select {
		case e := <-outcomes:
			if e != nil {
				t.Fatal("cross-assignment deadlock/failure", e)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("cross-assignment did not finish")
		}
	}
}

func TestPGLocationsNFCAndReplayOriginalProofAfterDomainWait(t *testing.T) {
	for i, kind := range []string{"nfc-audit", "replay-parent"} {
		t.Run(kind, func(t *testing.T) {
			f := reviewRealLocationClock(t, newLocationFixture(t, 4680+i*10))
			locks := reviewLocks(t, f.db)
			var tag locations.EncryptedTag
			var meet locations.Meet
			var blocker *sql.Tx
			var pid int
			var e error
			if kind == "nfc-audit" {
				if e = f.r.WithEncryptedNFC(make([]byte, 32), nil); e != nil {
					t.Fatal(e)
				}
				tag, e = f.r.ProvisionEncryptedTag(f.contexts[0], f.actors[0], locations.ProvisionTag{UID: "04958CAA5C5E85", SunKey: strings.Repeat("0", 32), MACProfile: "encrypted-file"})
				if e != nil {
					t.Fatal(e)
				}
				blocker, e = f.db.BeginTx(locks.ctx, nil)
				if e != nil {
					t.Fatal(e)
				}
				t.Cleanup(func() { blocker.Rollback() })
				if e = blocker.QueryRowContext(locks.ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
					t.Fatal(e)
				}
				if _, e = blocker.ExecContext(locks.ctx, `LOCK TABLE owned_location_audit IN ACCESS EXCLUSIVE MODE`); e != nil {
					t.Fatal(e)
				}
			} else {
				meet = locationMeet(t, f, locations.Unlisted)
				blocker, pid = atomicBlocker(t, locks, "owned_meets", meet.ID)
			}
			expiry := reviewDeadline(t, f.db)
			query(t, f.db, `UPDATE owned_authority_sessions SET expires_at=$2 WHERE account_id=$1`, f.actors[0], expiry)
			done := make(chan error, 1)
			go func() {
				if kind == "nfc-audit" {
					_, e := f.r.ResolveSUN(f.contexts[0], f.actors[0], locations.SUNRequest{RecordID: tag.ID, PICCData: "96EAC85E4785F75B98F7024F2E43F4B7", Encrypted: "CEE9A53E3E463EF1F459635736738962", CMAC: "2D57B7EB7CE85957"}, true)
					done <- e
				} else {
					_, _, e := f.r.LocationEvents(f.contexts[0], f.actors[0], meet.ID, false, nil, 0, 100)
					done <- e
				}
			}()
			table := "owned_location_audit"
			if kind == "replay-parent" {
				table = "owned_meets"
			}
			waitAtomicBlocked(t, locks, pid, table)
			reviewDatabaseDeadline(t, f.db, expiry)
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-done; e == nil {
				t.Fatal("expired original proof committed after domain wait")
			}
			if kind == "nfc-audit" {
				var counter int
				var owner *string
				if e = f.db.QueryRowContext(locks.ctx, `SELECT counter,account_id::text FROM owned_encrypted_nfc_tags WHERE id=$1`, tag.ID).Scan(&counter, &owner); e != nil || counter != -1 || owner != nil {
					t.Fatal("failed claim leaked counter/ownership", counter, owner, e)
				}
			}
		})
	}
}

func TestPGNFCRecipientScheduledDisableAfterUIDWait(t *testing.T) {
	f := newLocationFixture(t, 4700)
	if e := f.r.WithEncryptedNFC(make([]byte, 32), nil); e != nil {
		t.Fatal(e)
	}
	locks := reviewLocks(t, f.db)
	tx, e := f.db.BeginTx(locks.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	var pid int
	if e = tx.QueryRowContext(locks.ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	uid := "04958CAA5C5E86"
	if _, e = tx.ExecContext(locks.ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text,19424))`, uid); e != nil {
		t.Fatal(e)
	}
	starts := reviewDeadline(t, f.db)
	query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at)VALUES($1,'disable_account',$2)`, f.actors[1], starts)
	done := make(chan error, 1)
	go func() {
		_, e := f.r.ProvisionEncryptedTag(f.contexts[0], f.actors[0], locations.ProvisionTag{UID: uid, SunKey: strings.Repeat("0", 32), AssignedUserID: &f.actors[1]})
		done <- e
	}()
	waitAtomicBlocked(t, locks, pid, "pg_advisory_xact_lock")
	reviewDatabaseDeadline(t, f.db, starts)
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, locations.ErrNotFound) {
		t.Fatal("UID wait admitted disabled target", e)
	}
	var count int
	if e = f.db.QueryRowContext(locks.ctx, `SELECT count(*) FROM owned_encrypted_nfc_tags WHERE uid=$1`, uid).Scan(&count); e != nil || count != 0 {
		t.Fatal("forbidden enrollment persisted", count, e)
	}
}

func TestPGMediaAndNearbyScheduledDisabledOwners(t *testing.T) {
	f := newMediaFixture(t)
	for _, id := range []string{f.actor, f.other} {
		if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), id, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
			t.Fatal(e)
		}
	}
	bundle, e := f.repo.SaveBundle(f.otherCtx, f.other, "", media.BundleRequest{Name: "public owner content", Public: true})
	if e != nil {
		t.Fatal(e)
	}
	poolID := authorityUUID(t)
	if e = f.repo.WithPools(map[string]media.ObjectStore{poolID: f.store}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.CreatePool(f.otherCtx, f.other, media.Pool{ID: poolID, Name: "public pool", Public: true, AllowEncryption: true, MaxFileBytes: 1000}); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at)VALUES($1,'disable_account',clock_timestamp()-interval '1 second')`, f.other)
	if _, e = f.repo.Bundle(f.ctx, f.actor, bundle.ID, ""); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("scheduled-disabled public bundle owner projected", e)
	}
	pools, e := f.repo.Pools(f.ctx, f.actor)
	if e != nil {
		t.Fatal(e)
	}
	for _, p := range pools {
		if p.ID == poolID {
			t.Fatal("disabled public pool projected", p)
		}
	}
	u := mediaUpload("disabled-pool", "hello", false)
	u.Options.PoolID = poolID
	if _, _, e = f.repo.Upload(f.ctx, f.actor, u, strings.NewReader("hello")); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("upload admitted disabled pool policy", e)
	}
	if _, e = f.repo.AddQuotaRecord(f.ctx, f.actor, media.QuotaRecord{AccountID: f.other, Name: "disabled recipient", SourceKey: "disabled", QuotaBytes: 100}); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("quota granted to disabled recipient", e)
	}
	mediaUsage(t, f, 0, 0)
	l := newLocationFixture(t, 4710)
	tokens, e := l.r.IssuePresenceTokens(l.contexts[0], l.actors[0], locations.PresenceRequest{DeviceID: "disable-fixture", FriendOnly: boolean(false)})
	if e != nil {
		t.Fatal(e)
	}
	query(t, l.db, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at)VALUES($1,'disable_account',clock_timestamp()-interval '1 second')`, l.actors[0])
	peers, e := l.r.ResolveNearby(l.contexts[1], l.actors[1], []locations.Observation{{Token: tokens.Tokens[0].Token, Slot: tokens.Tokens[0].Slot, SeenCount: 1, AvgRSSI: -40}})
	if e != nil || len(peers) != 0 {
		t.Fatal("disabled token owner projected", peers, e)
	}
}

type reviewLateProtection struct {
	file  string
	calls atomic.Int64
}

func (p *reviewLateProtection) ProtectFileReadInTransaction(ctx context.Context, tx *sql.Tx, actor, id string) error {
	if id == p.file && p.calls.Add(1) == 4 {
		_, e := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('live-photo-original-proof-review',777))`)
		return e
	}
	return nil
}
func TestPGMediaLivePhotoOriginalProofAfterLateAudienceWait(t *testing.T) {
	f := newMediaFixture(t)
	f.config.Now = time.Now
	f = mediaPool(t, f, f.db)
	var pixels bytes.Buffer
	if e := png.Encode(&pixels, image.NewNRGBA(image.Rect(0, 0, 1, 1))); e != nil {
		t.Fatal(e)
	}
	u := mediaUpload("late-image", pixels.String(), false)
	u.MIMEType = "image/png"
	picture, _, e := f.repo.Upload(f.ctx, f.actor, u, bytes.NewReader(pixels.Bytes()))
	if e != nil {
		t.Fatal(e)
	}
	u = mediaUpload("late-video", "synthetic video provider contract", false)
	u.MIMEType = "video/mp4"
	video, _, e := f.repo.Upload(f.ctx, f.actor, u, strings.NewReader("synthetic video provider contract"))
	if e != nil {
		t.Fatal(e)
	}
	f.repo.WithRenderer(syntheticRasterRenderer{pixels.Bytes()})
	pair, e := f.repo.ComposeLivePhoto(f.ctx, f.actor, media.LivePhotoRequest{ImageID: picture.ID, VideoID: video.ID})
	if e != nil {
		t.Fatal(e)
	}
	f.repo.WithFileReadProtection(&reviewLateProtection{file: video.ID})
	locks := reviewLocks(t, f.db)
	tx, e := f.db.BeginTx(locks.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	var pid int
	if e = tx.QueryRowContext(locks.ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.ExecContext(locks.ctx, `SELECT pg_advisory_xact_lock(hashtextextended('live-photo-original-proof-review',777))`); e != nil {
		t.Fatal(e)
	}
	expiry := reviewDeadline(t, f.db)
	query(t, f.db, `UPDATE owned_authority_sessions SET expires_at=$2 WHERE account_id=$1`, f.actor, expiry)
	done := make(chan error, 1)
	go func() { _, e := f.repo.LivePhoto(f.ctx, f.actor, pair.ID); done <- e }()
	waitAtomicBlocked(t, locks, pid, "pg_advisory_xact_lock")
	reviewDatabaseDeadline(t, f.db, expiry)
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e == nil {
		t.Fatal("late protected audience wait substituted fresh proof")
	}
	mediaUsage(t, f, int64(pixels.Len()+len("synthetic video provider contract")), 0)
}

type reviewNFCLocker struct {
	started, release chan struct{}
	calls            int
}

func (s *reviewNFCLocker) Ready(context.Context) error { return nil }
func (s *reviewNFCLocker) Lock(ctx context.Context, req locations.NFCLockRequest) (locations.NFCLockProof, error) {
	s.calls++
	if s.started != nil {
		close(s.started)
		select {
		case <-s.release:
		case <-ctx.Done():
			return locations.NFCLockProof{}, ctx.Err()
		}
		s.started = nil
	}
	return locations.NFCLockProof{SUN: locations.SUNRequest{RecordID: req.RecordID, PICCData: "89917C11A53271A07FA7A0F66B3EF963", Encrypted: "CEE9A53E3E463EF1F459635736738962", CMAC: "D4A09C7B09965AA2"}, TranscriptSHA256: strings.Repeat("a", 64)}, nil
}
func TestPGNFCHardwareReceiptOriginalProofAndRestart(t *testing.T) {
	f := reviewRealLocationClock(t, newLocationFixture(t, 4720))
	provider := &reviewNFCLocker{started: make(chan struct{}), release: make(chan struct{})}
	if e := f.r.WithEncryptedNFC(make([]byte, 32), provider); e != nil {
		t.Fatal(e)
	}
	tag, e := f.r.ProvisionEncryptedTag(f.contexts[0], f.actors[0], locations.ProvisionTag{UID: "04958CAA5C5E87", SunKey: strings.Repeat("0", 32), MACProfile: "encrypted-file", AssignedUserID: &f.actors[0]})
	if e != nil {
		t.Fatal(e)
	}
	expiry := reviewDeadline(t, f.db)
	query(t, f.db, `UPDATE owned_authority_sessions SET expires_at=$2 WHERE account_id=$1`, f.actors[0], expiry)
	done := make(chan error, 1)
	started := provider.started
	go func() { _, e := f.r.LockEncryptedTag(f.contexts[0], f.actors[0], tag.ID); done <- e }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not receive lock")
	}
	reviewDatabaseDeadline(t, f.db, expiry)
	close(provider.release)
	if e = <-done; e == nil {
		t.Fatal("hardware receipt granted expired original proof")
	}
	var locked bool
	var counter int
	var digest *string
	if e = f.db.QueryRowContext(context.Background(), `SELECT locked_at IS NOT NULL,counter,lock_proof_sha256 FROM owned_encrypted_nfc_tags WHERE id=$1`, tag.ID).Scan(&locked, &counter, &digest); e != nil || locked || counter != -1 || digest != nil {
		t.Fatal("failed commit fabricated durable lock", locked, counter, digest, e)
	}
	// The same explicit provider binding survives a backend restart. The physical
	// protocol's authenticated already-locked path is independently tested above.
	reopened := locationpg.NewWithAuthority(f.db, f.g)
	if e = reopened.WithEncryptedNFC(make([]byte, 32), provider); e != nil {
		t.Fatal(e)
	}
	token := pgIssue(t, f.service, f.actors[0]).Token
	proof := locationProof(t, f, token)
	value, e := reopened.LockEncryptedTag(proof, f.actors[0], tag.ID)
	if e != nil || !value.Locked || provider.calls != 2 {
		t.Fatal("uncertain hardware commit not reconciled", value, e, provider.calls)
	}
	if e = f.db.QueryRowContext(context.Background(), `SELECT locked_at IS NOT NULL,counter,lock_proof_sha256 FROM owned_encrypted_nfc_tags WHERE id=$1`, tag.ID).Scan(&locked, &counter, &digest); e != nil || !locked || counter != 9 || digest == nil || *digest != strings.Repeat("a", 64) {
		t.Fatal(locked, counter, digest, e)
	}
	if _, e = reopened.LockEncryptedTag(proof, f.actors[0], tag.ID); e != nil || provider.calls != 2 {
		t.Fatal("verified lock unnecessarily rewrote device", e, provider.calls)
	}
}

func TestPGMediaQuotaConcurrentGrantConservation(t *testing.T) {
	f := newMediaFixture(t)
	if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	request := media.QuotaRecord{AccountID: f.other, Name: "one actual grant", SourceKey: "concurrent-source", QuotaBytes: 100}
	done := make(chan error, 8)
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() { <-start; _, e := f.repo.AddQuotaRecord(f.ctx, f.actor, request); done <- e }()
	}
	close(start)
	for i := 0; i < 8; i++ {
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	}
	var count int
	var limit, used, reserved int64
	if e := f.db.QueryRowContext(context.Background(), `SELECT count(*) FROM owned_media_quota_records WHERE account_id=$1 AND source_key=$2`, f.other, request.SourceKey).Scan(&count); e != nil {
		t.Fatal(e)
	}
	if e := f.db.QueryRowContext(context.Background(), `SELECT limit_bytes,used_bytes,reserved_bytes FROM owned_media_quotas WHERE account_id=$1`, f.other).Scan(&limit, &used, &reserved); e != nil {
		t.Fatal(e)
	}
	if count != 1 || limit != 65636 || used != 0 || reserved != 0 {
		t.Fatal("concurrent grant duplicated quota", count, limit, used, reserved)
	}
}
