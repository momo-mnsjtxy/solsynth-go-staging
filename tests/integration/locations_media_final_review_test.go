// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/sphere/locations"
	"strings"
	"testing"
	"time"
)

func finalReviewTableLock(t *testing.T, locks atomicAuthorityFixture, table string) (*sql.Tx, int) {
	t.Helper()
	// Identifiers are owned test constants, never request input.
	if table != "owned_media_bundles" && table != "owned_location_audit" {
		t.Fatal("unrecognized fixture table")
	}
	tx, e := locks.db.BeginTx(locks.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { tx.Rollback() })
	var pid int
	if e = tx.QueryRowContext(locks.ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.ExecContext(locks.ctx, `LOCK TABLE `+table+` IN ACCESS EXCLUSIVE MODE`); e != nil {
		t.Fatal(e)
	}
	return tx, pid
}

func TestPGMediaUploadPublicPoolOwnerAfterDatabaseWait(t *testing.T) {
	for _, kind := range []string{"ordinary", "resumable"} {
		t.Run(kind, func(t *testing.T) {
			f := newMediaFixture(t)
			for _, id := range []string{f.actor, f.other} {
				if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), id, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
					t.Fatal(e)
				}
			}
			poolID := authorityUUID(t)
			if e := f.repo.WithPools(map[string]media.ObjectStore{poolID: f.store}); e != nil {
				t.Fatal(e)
			}
			if _, e := f.repo.CreatePool(f.otherCtx, f.other, media.Pool{ID: poolID, Name: "late disabled public owner", Public: true, MaxFileBytes: 1000}); e != nil {
				t.Fatal(e)
			}
			var task media.UploadTask
			if kind == "resumable" {
				request := resumableRequest("hello", 5)
				request.PoolID = poolID
				var e error
				task, e = f.repo.CreateResumable(f.ctx, f.actor, request, "late-pool-owner")
				if e != nil {
					t.Fatal(e)
				}
				uploadResumableChunk(t, f, task.TaskID, 0, "hello")
				mediaUsage(t, f, 0, 5)
			}
			locks := reviewLocks(t, f.db)
			blocker, pid := finalReviewTableLock(t, locks, "owned_media_bundles")
			starts := reviewDeadline(t, f.db)
			query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at) VALUES($1,'disable_account',$2)`, f.other, starts)
			done := make(chan error, 1)
			if kind == "ordinary" {
				go func() {
					u := mediaUpload("late-pool-owner", "hello", false)
					u.Options.PoolID = poolID
					_, _, e := f.repo.Upload(f.ctx, f.actor, u, strings.NewReader("hello"))
					done <- e
				}()
			} else if e := f.repo.CompleteResumable(f.ctx, f.actor, task.TaskID); e != nil {
				t.Fatal(e)
			}
			// This is an observed PostgreSQL wait after bytes have been written,
			// not a synthetic clock jump or a pre-call disabled-owner fixture.
			waitAtomicBlocked(t, locks, pid, "owned_media_bundles")
			mediaUsage(t, f, 0, 5)
			var fileID string
			if e := f.db.QueryRowContext(locks.ctx, `SELECT id::text FROM owned_media_files WHERE account_id=$1 AND state='pending'`, f.actor).Scan(&fileID); e != nil {
				t.Fatal(e)
			}
			body, e := f.store.Open(locks.ctx, fileID)
			if e != nil {
				t.Fatal("final validation wait occurred before actual object I/O", e)
			}
			body.Close()
			reviewDatabaseDeadline(t, f.db, starts)
			if e := blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if kind == "ordinary" {
				if e := <-done; !errors.Is(e, media.ErrNotFound) {
					t.Fatal("ordinary upload ignored current foreign pool owner", e)
				}
			} else {
				failed := awaitResumable(t, f, task.TaskID, "Failed")
				if failed.FailureCode != "MEDIA_POLICY_CHANGED" || failed.FileID != nil {
					t.Fatal("resumable policy failure exposed a ready file", failed)
				}
				// Failed is durable before physical cleanup completes. Wait for the
				// terminal charge receipt, without accepting a leaked reservation.
				tick := time.NewTicker(10 * time.Millisecond)
				defer tick.Stop()
				for {
					var deleted bool
					if e := f.db.QueryRowContext(locks.ctx, `SELECT state='deleted' AND charge='none' FROM owned_media_files WHERE id=$1`, task.TaskID).Scan(&deleted); e != nil {
						t.Fatal(e)
					}
					if deleted {
						break
					}
					select {
					case <-locks.ctx.Done():
						t.Fatal("failed upload never released charge", locks.ctx.Err())
					case <-tick.C:
					}
				}
			}
			var ready int
			if e := f.db.QueryRowContext(locks.ctx, `SELECT count(*) FROM owned_media_files WHERE account_id=$1 AND state='ready'`, f.actor).Scan(&ready); e != nil || ready != 0 {
				t.Fatal("policy failure committed ready file", ready, e)
			}
			mediaUsage(t, f, 0, 0)
		})
	}
}

func TestPGNFCSUNBilateralBlockAfterAuditWait(t *testing.T) {
	f := newLocationFixture(t, 4760)
	if e := f.r.WithEncryptedNFC(make([]byte, 32), nil); e != nil {
		t.Fatal(e)
	}
	tag, e := f.r.ProvisionEncryptedTag(f.contexts[0], f.actors[0], locations.ProvisionTag{UID: "04958CAA5C5E88", SunKey: strings.Repeat("0", 32), MACProfile: "encrypted-file", AssignedUserID: &f.actors[0]})
	if e != nil {
		t.Fatal(e)
	}
	locationFriends(t, f, 0, 1)
	locks := reviewLocks(t, f.db)
	blocker, pid := finalReviewTableLock(t, locks, "owned_location_audit")
	type result struct {
		projection locations.TagResolution
		err        error
	}
	done := make(chan result, 1)
	go func() {
		v, e := f.r.ResolveSUN(f.contexts[1], f.actors[1], locations.SUNRequest{RecordID: tag.ID, PICCData: "52E5BCD73A33407B2D5D169B5BE8D571", Encrypted: "CEE9A53E3E463EF1F459635736738962", CMAC: "11D3B3D8272EB3AF"}, false)
		done <- result{v, e}
	}()
	waitAtomicBlocked(t, locks, pid, "owned_location_audit")
	// An existing canonical relationship can change without a new account FK
	// acquisition. Hold the blocking owner's row and perform its unilateral
	// block transition while the scanner retains its different account lock.
	change, e := f.db.BeginTx(locks.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer change.Rollback()
	if _, e = change.ExecContext(locks.ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, f.actors[0]); e != nil {
		t.Fatal(e)
	}
	if _, e = change.ExecContext(locks.ctx, `UPDATE owned_social_relationships SET status=-100,updated_at=clock_timestamp() WHERE account_id=$1 AND related_id=$2`, f.actors[0], f.actors[1]); e != nil {
		t.Fatal(e)
	}
	if _, e = change.ExecContext(locks.ctx, `DELETE FROM owned_social_relationships WHERE account_id=$1 AND related_id=$2 AND status>=100`, f.actors[1], f.actors[0]); e != nil {
		t.Fatal(e)
	}
	if e = change.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	v := <-done
	if !errors.Is(v.err, locations.ErrNotFound) || v.projection.Account.ID != "" || len(v.projection.Actions) != 0 {
		t.Fatal("audit wait returned blocked owner projection", v)
	}
	var counter int
	var owner string
	if e = f.db.QueryRowContext(locks.ctx, `SELECT counter,account_id::text FROM owned_encrypted_nfc_tags WHERE id=$1`, tag.ID).Scan(&counter, &owner); e != nil || counter != -1 || owner != f.actors[0] {
		t.Fatal("blocked verification committed counter or ownership", counter, owner, e)
	}
	var audits int
	if e = f.db.QueryRowContext(locks.ctx, `SELECT count(*) FROM owned_location_audit WHERE resource_id=$1 AND action='nfc.sun.verify'`, tag.ID).Scan(&audits); e != nil || audits != 0 {
		t.Fatal("blocked verification committed audit", audits, e)
	}
}

func TestPGPlainNFCOriginalProofAfterRowAndAuditWait(t *testing.T) {
	for i, kind := range []string{"update-row", "update-audit", "delete-row", "delete-audit", "resolve-row"} {
		t.Run(kind, func(t *testing.T) {
			f := reviewRealLocationClock(t, newLocationFixture(t, 4780+i*10))
			uid := []string{"04AABBCC4780", "04AABBCC4790", "04AABBCC4800", "04AABBCC4810", "04AABBCC4820"}[i]
			tag, e := f.r.RegisterTag(f.contexts[0], f.actors[0], locations.RegisterTag{UID: uid, Label: postString("unchanged")})
			if e != nil {
				t.Fatal(e)
			}
			actor := 0
			if kind == "resolve-row" {
				actor = 1
			}
			locks := reviewLocks(t, f.db)
			var blocker *sql.Tx
			var pid int
			table := "owned_nfc_tags"
			if strings.HasSuffix(kind, "audit") {
				table = "owned_location_audit"
				blocker, pid = finalReviewTableLock(t, locks, table)
			} else {
				blocker, pid = atomicBlocker(t, locks, table, tag.ID)
			}
			expiry := reviewDeadline(t, f.db)
			query(t, f.db, `UPDATE owned_authority_sessions SET expires_at=$2 WHERE account_id=$1`, f.actors[actor], expiry)
			done := make(chan struct {
				err        error
				resolution locations.TagResolution
			}, 1)
			go func() {
				var e error
				var out locations.TagResolution
				switch {
				case strings.HasPrefix(kind, "update"):
					_, e = f.r.UpdateTag(f.contexts[actor], f.actors[actor], tag.ID, locations.TagPatch{Label: postString("must rollback")})
				case strings.HasPrefix(kind, "delete"):
					e = f.r.DeleteTag(f.contexts[actor], f.actors[actor], tag.ID)
				default:
					out, e = f.r.ResolveTag(f.contexts[actor], f.actors[actor], tag.ID, true)
				}
				done <- struct {
					err        error
					resolution locations.TagResolution
				}{e, out}
			}()
			waitAtomicBlocked(t, locks, pid, table)
			reviewDatabaseDeadline(t, f.db, expiry)
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			result := <-done
			if result.err == nil || result.resolution.Account.ID != "" || len(result.resolution.Actions) != 0 {
				t.Fatal("plain NFC accepted expired retained proof")
			}
			var label string
			var active, deleted bool
			if e = f.db.QueryRowContext(locks.ctx, `SELECT label,is_active,deleted_at IS NOT NULL FROM owned_nfc_tags WHERE id=$1`, tag.ID).Scan(&label, &active, &deleted); e != nil || label != "unchanged" || !active || deleted {
				t.Fatal("expired plain NFC mutation committed", label, active, deleted, e)
			}
			var audits int
			if e = f.db.QueryRowContext(locks.ctx, `SELECT count(*) FROM owned_location_audit WHERE resource_id=$1 AND action IN('nfc.update','nfc.delete')`, tag.ID).Scan(&audits); e != nil || audits != 0 {
				t.Fatal("expired plain NFC audit committed", audits, e)
			}
			if out, e := f.r.ResolveTag(context.Background(), "", tag.ID, true); e != nil || out.Account.ID != f.actors[0] {
				t.Fatal("anonymous active plain association was removed", out, e)
			}
		})
	}
}
