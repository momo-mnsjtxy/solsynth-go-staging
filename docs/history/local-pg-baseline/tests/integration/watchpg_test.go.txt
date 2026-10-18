package integration_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"solsynth.local/go-migration/internal/watchpg"
	"solsynth.local/go-migration/ports/sphere"
)

func database(t *testing.T) *sql.DB {
	t.Helper()
	dsn, root := os.Getenv("PG_TEST_DSN"), os.Getenv("SOLSYNTH_DISPOSABLE_PG")
	if dsn == "" {
		if os.Getenv("PG_TEST_REQUIRED") == "1" {
			t.Fatal("required PostgreSQL integration DSN is missing")
		}
		t.Skip("actual PostgreSQL integration requires scripts/integration-postgres.sh")
	}
	parent := os.Getenv("PG_TEST_ROOT")
	if parent == "" {
		parent = "/workspace/shared/solsynth-go-integration"
	}
	marker, markerErr := os.ReadFile(filepath.Join(root, ".solsynth-disposable"))
	if !filepath.IsAbs(root) || filepath.Dir(root) != filepath.Clean(parent) || !strings.HasPrefix(filepath.Base(root), "run.") || markerErr != nil || string(marker) != "synthetic-only\n" || !strings.Contains(dsn, "host="+filepath.Join(root, "socket")+" ") {
		t.Fatal("refusing a database outside the disposable test cluster")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db
}
func boolean(v bool) *bool { return &v }
func service(db *sql.DB) *sphere.WatchService {
	return &sphere.WatchService{Repository: watchpg.New(db)}
}
func query(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}
func preferences(t *testing.T, s *sphere.WatchService, id string) []sphere.WatchPreference {
	t.Helper()
	r, e := s.GetEffectivePreferences(context.Background(), id)
	if e != nil {
		t.Fatal(e)
	}
	return r
}

func TestPGOriginalSchemaAndIsolation(t *testing.T) {
	db := database(t)
	s := service(db)
	ctx := context.Background()
	a := "10000000-0000-0000-0000-000000000001"
	b := "10000000-0000-0000-0000-000000000002"
	rows := preferences(t, s, a)
	if len(rows) != 3 || !rows[0].NotifyReplies {
		t.Fatal("defaults")
	}
	_, err := s.UpdatePreferences(ctx, a, []sphere.WatchPreferenceUpdate{{Source: sphere.WatchBookmark, NotifyReplies: boolean(false)}})
	if err != nil {
		t.Fatal(err)
	}
	if preferences(t, service(database(t)), a)[0].NotifyReplies {
		t.Fatal("independent pool did not observe committed patch")
	}
	if !preferences(t, s, b)[0].NotifyReplies {
		t.Fatal("cross-account write")
	}
	var source int
	var id string
	if err := db.QueryRow("SELECT source,id::text FROM post_watch_preferences WHERE account_id=$1", a).Scan(&source, &id); err != nil || source != 0 || id == "" {
		t.Fatalf("persisted source/id: %d %q %v", source, id, err)
	}
}

func TestPGIndependentServicePatchConcurrency(t *testing.T) {
	a := service(database(t))
	b := service(database(t))
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		id := "20000000-0000-0000-0000-" + strings.Repeat("0", 10) + string([]byte{'0' + byte(i/10), '0' + byte(i%10)})
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for n, s := range []*sphere.WatchService{a, b} {
			wg.Add(1)
			go func(n int, s *sphere.WatchService) {
				defer wg.Done()
				<-start
				u := sphere.WatchPreferenceUpdate{Source: sphere.WatchBookmark}
				if n == 0 {
					u.NotifyReplies = boolean(false)
				} else {
					u.NotifyReactions = boolean(false)
				}
				_, e := s.UpdatePreferences(ctx, id, []sphere.WatchPreferenceUpdate{u})
				errs <- e
			}(n, s)
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		r := preferences(t, a, id)
		if r[0].NotifyReplies || r[0].NotifyReactions {
			t.Fatalf("lost independent-service patch at iteration %d", i)
		}
	}
}

func TestPGSoftDeleteAndDuplicateLiveRows(t *testing.T) {
	db := database(t)
	s := service(db)
	ctx := context.Background()
	id := "30000000-0000-0000-0000-000000000001"
	_, err := s.UpdatePreferences(ctx, id, []sphere.WatchPreferenceUpdate{{Source: sphere.WatchBookmark, NotifyReplies: boolean(false)}})
	if err != nil {
		t.Fatal(err)
	}
	query(t, db, "UPDATE post_watch_preferences SET deleted_at=now() WHERE account_id=$1", id)
	if !preferences(t, s, id)[0].NotifyReplies {
		t.Fatal("soft-deleted row exposed")
	}
	_, err = s.UpdatePreferences(ctx, id, []sphere.WatchPreferenceUpdate{{Source: sphere.WatchBookmark, NotifyEdits: boolean(false)}})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	db.QueryRow("SELECT count(*) FROM post_watch_preferences WHERE account_id=$1", id).Scan(&count)
	if count != 2 {
		t.Fatalf("deleted row not preserved: %d", count)
	}
	query(t, db, `INSERT INTO post_watch_preferences SELECT '30000000-0000-0000-0000-000000000099'::uuid,account_id,source,notify_reactions,notify_replies,notify_chains,notify_forwards,notify_edits,created_at,updated_at,NULL FROM post_watch_preferences WHERE account_id=$1 AND deleted_at IS NULL`, id)
	if _, err = s.GetEffectivePreferences(ctx, id); err == nil {
		t.Fatal("duplicate live rows accepted")
	}
	if _, err = s.UpdatePreferences(ctx, id, []sphere.WatchPreferenceUpdate{{Source: sphere.WatchBookmark, NotifyReplies: boolean(false)}}); err == nil {
		t.Fatal("ambiguous duplicate update accepted")
	}
}

func TestPGTransactionRollbackAndCancellation(t *testing.T) {
	db := database(t)
	s := service(db)
	id := "40000000-0000-0000-0000-000000000001"
	ctx := context.Background()
	query(t, db, `ALTER TABLE post_watch_preferences ADD CONSTRAINT synthetic_failure CHECK (account_id <> '40000000-0000-0000-0000-000000000001'::uuid OR source <> 1 OR notify_edits)`)
	t.Cleanup(func() { query(t, db, "ALTER TABLE post_watch_preferences DROP CONSTRAINT synthetic_failure") })
	_, err := s.UpdatePreferences(ctx, id, []sphere.WatchPreferenceUpdate{{Source: sphere.WatchBookmark, NotifyReplies: boolean(false)}, {Source: sphere.WatchReaction, NotifyEdits: boolean(false)}})
	if err == nil {
		t.Fatal("constraint error lost")
	}
	if !preferences(t, s, id)[0].NotifyReplies {
		t.Fatal("partial transaction persisted")
	}
	var count int
	if err = db.QueryRow("SELECT count(*) FROM post_watch_preferences WHERE account_id=$1", id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback rows=%d err=%v", count, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.UpdatePreferences(cancelled, id, []sphere.WatchPreferenceUpdate{{Source: sphere.WatchBookmark, NotifyReplies: boolean(false)}}); err == nil {
		t.Fatal("cancelled operation accepted")
	}
}

func TestPGRestartPersistence(t *testing.T) {
	s := service(database(t))
	id := "50000000-0000-0000-0000-000000000001"
	if os.Getenv("PG_TEST_PHASE") == "before" {
		if _, e := s.UpdatePreferences(context.Background(), id, []sphere.WatchPreferenceUpdate{{Source: sphere.WatchBookmark, NotifyForwards: boolean(false)}}); e != nil {
			t.Fatal(e)
		}
		return
	}
	if os.Getenv("PG_TEST_PHASE") != "after" {
		t.Fatal("missing restart phase")
	}
	if preferences(t, s, id)[0].NotifyForwards {
		t.Fatal("committed preference lost across PostgreSQL restart")
	}
}

func TestPGIdentityAndTimestampPreservation(t *testing.T) {
	db := database(t)
	s := service(db)
	id := "60000000-0000-0000-0000-000000000001"
	ctx := context.Background()
	_, err := s.UpdatePreferences(ctx, id, []sphere.WatchPreferenceUpdate{{Source: sphere.WatchBookmark, NotifyReplies: boolean(false)}})
	if err != nil {
		t.Fatal(err)
	}
	var beforeID, afterID string
	var created, updated, afterCreated, afterUpdated time.Time
	if err := db.QueryRow("SELECT id::text,created_at,updated_at FROM post_watch_preferences WHERE account_id=$1", id).Scan(&beforeID, &created, &updated); err != nil {
		t.Fatal(err)
	}
	_, err = s.UpdatePreferences(ctx, id, []sphere.WatchPreferenceUpdate{{Source: sphere.WatchBookmark, NotifyEdits: boolean(false)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT id::text,created_at,updated_at FROM post_watch_preferences WHERE account_id=$1", id).Scan(&afterID, &afterCreated, &afterUpdated); err != nil {
		t.Fatal(err)
	}
	if beforeID != afterID || !created.Equal(afterCreated) || afterUpdated.Before(updated) {
		t.Fatal("row identity/creation time changed or update time moved backwards")
	}
	if _, err = s.GetEffectivePreferences(ctx, "not-a-uuid"); err == nil {
		t.Fatal("invalid account accepted")
	}
	r := watchpg.New(db)
	if err := r.SavePreferences(ctx, id, []sphere.WatchPreference{{AccountID: "60000000-0000-0000-0000-000000000002", Source: sphere.WatchReply}}); err == nil {
		t.Fatal("cross-account row accepted")
	}
}
