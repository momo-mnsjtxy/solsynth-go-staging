// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
)

// Called by the registered reservation/recovery case, so both reservation and
// storage eligibility are checked in the mandatory PostgreSQL group.
func miniappReservationExpiredAfterQuotaWait(t *testing.T) {
	for _, finish := range []bool{false, true} {
		name := "reserve"
		if finish {
			name = "finish"
		}
		t.Run(name, func(t *testing.T) {
			f := newBotFixture(t)
			_, store := miniappMedia(t, f, t.TempDir())
			clock := &atomic.Int64{}
			clock.Store(f.config.Now().Unix())
			cfg := f.config
			cfg.Now = func() time.Time { return time.Unix(clock.Load(), 0) }
			verifier, err := authority.NewVerifier(authoritypg.New(f.db), cfg)
			if err != nil {
				t.Fatal(err)
			}
			guard := authoritypg.NewTransactionGuard(authoritypg.New(f.db), verifier)
			proof, err := verifier.Authenticate(f.ctx, f.token)
			if err != nil {
				t.Fatal(err)
			}
			ctx := guard.Bind(f.ctx, proof)
			var stored atomic.Int64
			hook := miniappHookStore{ObjectStore: store, afterPut: func() { stored.Add(1) }}
			m, err := mediapg.New(f.db, hook, guard, mediapg.Config{MaxFileBytes: 5 << 20, PendingTTL: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			data := botZip(t, authorityUUID(t))
			upload := media.Upload{Name: "quota-wait.zip", MIMEType: "application/zip", Size: int64(len(data)), SHA256: mediaDigest(string(data))}
			var reserved media.File
			var cleanup func(context.Context) error
			if finish {
				reserved, cleanup, err = m.ReserveParentUpload(ctx, f.actor, upload)
				if err != nil {
					t.Fatal(err)
				}
			}
			blocker, err := f.db.BeginTx(f.ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback()
			var pid int
			if err = blocker.QueryRowContext(f.ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			if _, err = blocker.ExecContext(f.ctx, `SELECT account_id FROM owned_media_quotas WHERE account_id=$1 FOR UPDATE`, f.actor); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				if !finish {
					_, _, e := m.ReserveParentUpload(ctx, f.actor, upload)
					done <- e
					return
				}
				tx, e := f.db.BeginTx(ctx, nil)
				if e == nil {
					_, e = m.FinishParentUpload(ctx, tx, f.actor, reserved, bytes.NewReader(data))
					_ = tx.Rollback()
				}
				done <- e
			}()
			waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: f.ctx}, pid, "owned_media_quotas")
			clock.Add(48 * 60 * 60)
			if err = blocker.Commit(); err != nil {
				t.Fatal(err)
			}
			if err = <-done; !errors.Is(err, authority.ErrExpired) {
				t.Fatal("original proof expired during quota wait", err)
			}
			if stored.Load() != 0 {
				t.Fatal("expired proof reached object storage")
			}
			if finish {
				if err = cleanup(f.ctx); err != nil {
					t.Fatal(err)
				}
			} else if n := botCount(t, f, `SELECT count(*) FROM owned_media_files WHERE account_id=$1`, f.actor); n != 0 {
				t.Fatal("expired proof persisted reservation", n)
			}
			if n := botCount(t, f, `SELECT used_bytes+reserved_bytes FROM owned_media_quotas WHERE account_id=$1`, f.actor); n != 0 {
				t.Fatal("expired proof retained quota", n)
			}
		})
	}
}
