// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"solsynth.local/go-migration/internal/activitypg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/friendeventpg"
	"solsynth.local/go-migration/internal/notificationpg"
	"solsynth.local/go-migration/internal/presencepg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/notifications"
	"strings"
	"testing"
	"time"
)

func TestPGFriendEventsArtworkWaitChecksOriginalSourceAndReader(t *testing.T) {
	for _, change := range []string{"source_expiry", "reader_expiry", "artwork_retention"} {
		t.Run(change, func(t *testing.T) {
			f := newArtworkFixture(t)
			at, wall := time.Now().UTC(), time.Now()
			f.config.Now = func() time.Time { return at.Add(time.Since(wall)) }
			for _, id := range []string{f.actor, f.other} {
				if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), id, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
					t.Fatal(e)
				}
			}
			var deadline time.Time
			if change == "reader_expiry" {
				cfg := f.config
				cfg.AccessTTL = 8 * time.Second
				issuer, e := authority.New(authoritypg.New(f.db), cfg)
				if e != nil {
					t.Fatal(e)
				}
				f.otherToken = pgIssue(t, issuer, f.other).Token
				raw, e := base64.RawURLEncoding.DecodeString(strings.Split(f.otherToken, ".")[1])
				if e != nil {
					t.Fatal(e)
				}
				var claims struct{ Exp int64 }
				if e = json.Unmarshal(raw, &claims); e != nil {
					t.Fatal(e)
				}
				deadline = time.Unix(claims.Exp, 0)
			}
			var e error
			f.verifier, e = authority.NewVerifier(authoritypg.New(f.db), f.config)
			if e != nil {
				t.Fatal(e)
			}
			f.guard = authoritypg.NewTransactionGuard(authoritypg.New(f.db), f.verifier)
			artworkApp(t, &f)
			proof, e := f.verifier.Authenticate(context.Background(), f.otherToken)
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(f.guard.Bind(context.Background(), proof), 20*time.Second)
			defer cancel()
			backendCall(t, f.app, "POST", "/stargate/relationships/"+f.other+"/friends", f.token, "", 200)
			backendCall(t, f.app, "POST", "/stargate/relationships/"+f.actor+"/friends/accept", f.otherToken, "", 200)
			asset := artworkPost(t, f.app, f.token, f.data)
			backendCall(t, f.app, "POST", "/api/activities", f.token, fmt.Sprintf(`{"type":"coding","manual_id":"wait","large_image":%q}`, asset.Hash), 200)
			events := notificationpg.New(f.db, f.guard, notifications.DefaultApp).WithFriendEvents(friendeventpg.New(f.db, f.guard, activitypg.New(f.db, f.guard)).WithStatus(presencepg.NewWithAuthority(f.db, f.guard)))
			for i := 0; i < 1000; i++ {
				n := commerceCount(t, f.db, `SELECT count(*) FROM owned_friend_audiences a JOIN owned_friend_events e ON e.id=a.event_id WHERE e.account_id=$1 AND a.processed_at IS NULL`, f.actor)
				if n == 0 {
					break
				}
				if _, e = events.ProjectFriends(ctx, 100); e != nil {
					t.Fatal(e)
				}
			}
			before, e := events.Replay(ctx, f.other, notifications.DefaultApp, 0, 100)
			if e != nil || len(before.Events) != 1 {
				t.Fatal(before, e)
			}
			switch change {
			case "source_expiry":
				if e = f.db.QueryRow(`UPDATE owned_authority_sessions SET expires_at=clock_timestamp()+interval '2 seconds' WHERE account_id=$1 RETURNING expires_at`, f.actor).Scan(&deadline); e != nil {
					t.Fatal(e)
				}
			case "artwork_retention":
				if e = f.db.QueryRow(`UPDATE owned_presence_artworks SET last_referenced_at=clock_timestamp()-(retention_seconds * interval '1 second')+interval '2 seconds' WHERE hash=$1 RETURNING last_referenced_at+(retention_seconds * interval '1 second')`, asset.Hash).Scan(&deadline); e != nil {
					t.Fatal(e)
				}
			}
			var file string
			if e = f.db.QueryRow(`SELECT file_id::text FROM owned_presence_artworks WHERE hash=$1`, asset.Hash).Scan(&file); e != nil {
				t.Fatal(e)
			}
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			blocker, pid := atomicBlocker(t, locks, "owned_media_files", file)
			type result struct {
				p notifications.Page
				e error
			}
			done := make(chan result, 1)
			go func() { p, e := events.Replay(ctx, f.other, notifications.DefaultApp, 0, 100); done <- result{p, e} }()
			waitAtomicBlocked(t, locks, pid, "owned_media_files")
			if change == "reader_expiry" {
				// The asserted deadline belongs to the original verifier clock,
				// not merely PostgreSQL wall time. Never infer expiry from a
				// differently anchored clock or extend the original token.
				tick := time.NewTicker(5 * time.Millisecond)
				defer tick.Stop()
				for !f.config.Now().After(deadline) {
					select {
					case <-tick.C:
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
			} else {
				organizationWaitExpired(t, ctx, f.db, deadline)
			}
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			got := <-done
			switch change {
			case "reader_expiry":
				if got.e == nil {
					t.Fatal("original expired reader proof survived artwork wait", got)
				}
			case "source_expiry":
				if got.e != nil || len(got.p.Events) != 0 {
					t.Fatal("expired source emitted", got)
				}
			case "artwork_retention":
				if got.e != nil || len(got.p.Events) != 1 || strings.Contains(string(got.p.Events[0].Data), asset.Hash) {
					t.Fatal("expired artwork leaked", got)
				}
			}
		})
	}
}
