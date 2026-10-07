package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/boardpg"
	"solsynth.local/go-migration/ports/passport/board"
)

func stringPointer(v string) *string                { return &v }
func boardCandidate(db *sql.DB) *boardpg.Repository { return boardpg.NewForCompatibilityTests(db, nil) }
func boardItems(t *testing.T, r board.Repository, account string) []board.Item {
	t.Helper()
	got, err := r.List(context.Background(), account)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestPGBoardRoundTripIsolation(t *testing.T) {
	db := database(t)
	createdTime := time.Date(2026, 10, 7, 1, 0, 0, 0, time.UTC)
	clock := createdTime
	r := boardpg.NewForCompatibilityTests(db, func() time.Time { return clock })
	ctx := context.Background()
	id := func(n int) string { return watcherID(0x81000000, n) }
	a, b := id(1), id(2)
	payload := board.Payload{"large": json.Number("9007199254740993123456789"), "null": nil, "false": false, "empty": "", "nested": map[string]any{"list": []any{json.Number("1"), "雪", nil}}, "array": []any{}, "object": map[string]any{}}
	input := []board.Item{{ID: id(10), AccountID: a, Order: 2147483647, Kind: board.Prebuilt, WidgetKey: stringPointer("clock"), IsEnabled: true, Payload: payload}, {ID: id(11), AccountID: a, Order: -2147483648, Kind: board.CustomApp, CustomAppID: stringPointer(id(20)), CustomAppWidgetKey: stringPointer("status"), Payload: nil}}
	if err := r.Replace(ctx, a, input); err != nil {
		t.Fatal(err)
	}
	got := boardItems(t, boardCandidate(database(t)), a)
	if len(got) != 2 || got[0].ID != id(11) || got[0].Order != -2147483648 || got[0].Payload != nil || got[0].WidgetKey != nil || got[0].CustomAppID == nil || *got[0].CustomAppID != id(20) || got[1].CustomAppID != nil || !reflect.DeepEqual(got[1].Payload, payload) {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if len(boardItems(t, r, b)) != 0 {
		t.Fatal("cross-account list leakage")
	}
	if _, err := r.FindByID(ctx, b, id(10)); !errors.Is(err, board.ErrNotFound) {
		t.Fatalf("cross-account find: %v", err)
	}
	assertAudit := func(wantUpdated time.Time) {
		t.Helper()
		var created, updated time.Time
		if err := db.QueryRow("SELECT created_at,updated_at FROM account_board_items WHERE id=$1", id(10)).Scan(&created, &updated); err != nil {
			t.Fatal(err)
		}
		if !created.Equal(createdTime) || !updated.Equal(wantUpdated) {
			t.Fatalf("candidate audit timestamps: created=%v updated=%v", created, updated)
		}
	}
	assertAudit(clock)
	clock = clock.Add(time.Hour)
	if err := r.Replace(ctx, a, input); err != nil {
		t.Fatal(err)
	}
	assertAudit(clock)
	stale := got[1]
	stale.Order = 0
	stale.WidgetKey = nil
	stale.Payload = board.Payload{"latest": "value"}
	clock = clock.Add(time.Hour)
	if err := r.Save(ctx, stale); err != nil {
		t.Fatal(err)
	}
	saved, err := r.FindByID(ctx, a, id(10))
	if err != nil {
		t.Fatal(err)
	}
	if saved.Order != 2147483647 || saved.WidgetKey == nil || saved.Payload["latest"] != "value" {
		t.Fatal("payload save overwrote detached layout")
	}
	assertAudit(clock)
	// A second account cannot claim a globally existing row ID; its own prior
	// board and the first account both survive the real primary-key failure.
	other := board.Item{ID: id(30), AccountID: b, Order: 0, Payload: board.Payload{"owner": "b"}}
	if err := r.Replace(ctx, b, []board.Item{other}); err != nil {
		t.Fatal(err)
	}
	collision := board.Item{ID: id(10), AccountID: b, Order: 1, Payload: board.Payload{"collision": true}}
	if err := r.Replace(ctx, b, []board.Item{collision}); err == nil {
		t.Fatal("global row ID collision accepted")
	}
	if got := boardItems(t, r, b); len(got) != 1 || got[0].ID != other.ID || got[0].Payload["owner"] != "b" {
		t.Fatalf("collision lost account b: %+v", got)
	}
	if got := boardItems(t, r, a); len(got) != 2 || got[1].ID != id(10) || got[1].Payload["latest"] != "value" {
		t.Fatalf("collision changed account a: %+v", got)
	}
}

func TestPGBoardAtomicRollback(t *testing.T) {
	db := database(t)
	r := boardCandidate(db)
	ctx := context.Background()
	id := func(n int) string { return watcherID(0x82000000, n) }
	account := id(1)
	original := board.Item{ID: id(10), AccountID: account, Order: 1, Payload: board.Payload{"version": "original"}}
	if err := r.Replace(ctx, account, []board.Item{original}); err != nil {
		t.Fatal(err)
	}
	bad := []board.Item{{ID: id(11), AccountID: account, Order: 2, Payload: board.Payload{}}, {ID: id(12), AccountID: account, Order: 3, WidgetKey: stringPointer(strings.Repeat("x", 257)), Payload: board.Payload{}}}
	if err := r.Replace(ctx, account, bad); err == nil {
		t.Fatal("expected real varchar constraint failure")
	}
	if got := boardItems(t, r, account); len(got) != 1 || got[0].ID != original.ID || got[0].Payload["version"] != "original" {
		t.Fatalf("failed replacement partly committed: %+v", got)
	}
	tombstone := board.Item{ID: id(13), AccountID: account, Order: 0, Payload: board.Payload{"retain": true}}
	if err := r.Replace(ctx, account, []board.Item{tombstone, original}); err != nil {
		t.Fatal(err)
	}
	query(t, db, "UPDATE account_board_items SET deleted_at=now() WHERE id=$1", tombstone.ID)
	if err := r.Replace(ctx, account, []board.Item{{ID: id(14), AccountID: account, Order: 0, Payload: board.Payload{}}}); err == nil {
		t.Fatal("retained tombstone unique-order conflict silently bypassed")
	}
	if got := boardItems(t, r, account); len(got) != 1 || got[0].ID != original.ID {
		t.Fatalf("order-conflict rollback lost active row: %+v", got)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM account_board_items WHERE id=$1 AND deleted_at IS NOT NULL", tombstone.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("tombstone deleted: %d %v", count, err)
	}
}

// These adapters are explicit synthetic validation fixtures, not production
// catalog, payload-normalization, authorization or cache implementations.
type boardTestCache struct{}

func (boardTestCache) InvalidateGroup(context.Context, string) error { return nil }

type boardTestNormalizer struct{}

func (boardTestNormalizer) Normalize(_ context.Context, p board.Payload) (board.Payload, error) {
	return p, nil
}

type barrierBoardNormalizer struct{ entered, release chan struct{} }

func (n barrierBoardNormalizer) Normalize(ctx context.Context, p board.Payload) (board.Payload, error) {
	close(n.entered)
	select {
	case <-n.release:
		return p, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type barrierBoardCatalog struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *barrierBoardCatalog) Lookup(ctx context.Context, _ string, key string) (*board.Widget, error) {
	c.once.Do(func() { close(c.entered) })
	select {
	case <-c.release:
		return &board.Widget{Key: key, Enabled: true, AllowMultiple: true}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (*barrierBoardCatalog) Validate(_ context.Context, _ string, _ string, p board.Payload) (board.Payload, error) {
	return p, nil
}

func TestPGBoardTransactionConcurrency(t *testing.T) {
	replaceDB, writeDB, observeDB := database(t), database(t), database(t)
	writeDB.SetMaxOpenConns(1)
	replaceDB.SetMaxOpenConns(1)
	query(t, writeDB, "SELECT set_config('application_name','solsynth_board_payload_writer',false)")
	query(t, replaceDB, "SELECT set_config('application_name','solsynth_board_layout_writer',false)")
	r, w := boardCandidate(replaceDB), boardCandidate(writeDB)
	id := func(n int) string { return watcherID(0x83000000, n) }
	account, itemID, appID := id(1), id(2), id(3)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	item := board.Item{ID: itemID, AccountID: account, Kind: board.CustomApp, CustomAppID: &appID, CustomAppWidgetKey: stringPointer("status"), Payload: board.Payload{"version": "initial"}}
	if err := r.Replace(ctx, account, []board.Item{item}); err != nil {
		t.Fatal(err)
	}
	catalog := &barrierBoardCatalog{entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(catalog.release) }) }
	defer release()
	replaceService := board.Service{Repository: r, Catalog: catalog, Cache: boardTestCache{}}
	writerService := board.Service{Repository: w, Normalizer: boardTestNormalizer{}, Cache: boardTestCache{}}
	replaced, written := make(chan error, 1), make(chan error, 1)
	item.Payload = board.Payload{"version": "untrusted-replacement"}
	item.Order = 7
	go func() { _, err := replaceService.Replace(ctx, account, []board.Item{item}); replaced <- err }()
	select {
	case <-catalog.entered:
	case err := <-replaced:
		t.Fatalf("replace did not reach post-read barrier: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() {
		_, err := writerService.UpdateAppPayload(ctx, account, itemID, appID, "status", board.Payload{"version": "latest-writer"})
		written <- err
	}()
	// Observe the second pool waiting on the actual PostgreSQL advisory lock,
	// rather than inferring serialization solely from a sleep or final ordering.
	for {
		var blocked bool
		if err := observeDB.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name='solsynth_board_payload_writer' AND wait_event_type='Lock' AND wait_event='advisory')").Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err := <-written:
			t.Fatalf("payload writer completed before lock release: %v", err)
		case <-ctx.Done():
			t.Fatal("writer never visibly blocked on advisory lock")
		case <-time.After(10 * time.Millisecond):
		}
	}
	release()
	for _, done := range []chan error{replaced, written} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	final, err := w.FindByID(ctx, account, itemID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Order != 7 || final.Payload["version"] != "latest-writer" {
		t.Fatalf("stale layout/payload survived transaction: %+v", final)
	}
	// Inverse ordering proves replacement snapshots the committed app payload
	// after waiting, rather than preserving an earlier detached value.
	normalizer := barrierBoardNormalizer{entered: make(chan struct{}), release: make(chan struct{})}
	var releaseNormOnce sync.Once
	releaseNorm := func() { releaseNormOnce.Do(func() { close(normalizer.release) }) }
	defer releaseNorm()
	writerService.Normalizer = normalizer
	go func() {
		_, err := writerService.UpdateAppPayload(ctx, account, itemID, appID, "status", board.Payload{"version": "writer-before-replace"})
		written <- err
	}()
	select {
	case <-normalizer.entered:
	case err := <-written:
		t.Fatalf("writer did not reach normalizer barrier: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	item.Order = 9
	item.Payload = board.Payload{"version": "stale-client-layout"}
	go func() { _, err := replaceService.Replace(ctx, account, []board.Item{item}); replaced <- err }()
	for {
		var blocked bool
		if err := observeDB.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name='solsynth_board_layout_writer' AND wait_event_type='Lock' AND wait_event='advisory')").Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err := <-replaced:
			t.Fatalf("layout writer completed before payload lock release: %v", err)
		case <-ctx.Done():
			t.Fatal("layout writer never visibly blocked on advisory lock")
		case <-time.After(10 * time.Millisecond):
		}
	}
	releaseNorm()
	for _, done := range []chan error{written, replaced} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	final, err = w.FindByID(ctx, account, itemID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Order != 9 || final.Payload["version"] != "writer-before-replace" {
		t.Fatalf("replacement lost committed app payload: %+v", final)
	}
}

func TestPGBoardPolicyAndErrors(t *testing.T) {
	db := database(t)
	r := boardCandidate(db)
	ctx := context.Background()
	id := func(n int) string { return watcherID(0x84000000, n) }
	account := id(1)
	if _, err := boardpg.New(db).List(ctx, account); !errors.Is(err, boardpg.ErrPolicyUnverified) {
		t.Fatalf("default read must deny: %v", err)
	}
	if err := boardpg.New(db).Replace(ctx, account, nil); !errors.Is(err, boardpg.ErrPolicyUnverified) {
		t.Fatalf("default write must deny: %v", err)
	}
	if _, err := r.List(ctx, "invalid"); !errors.Is(err, boardpg.ErrInvalidID) {
		t.Fatalf("invalid ID: %v", err)
	}
	item := board.Item{ID: id(10), AccountID: account, Order: 0, Payload: board.Payload{"old": true}}
	if err := r.Replace(ctx, account, []board.Item{item}); err != nil {
		t.Fatal(err)
	}
	abort := errors.New("synthetic callback abort")
	if err := r.WithinAccount(ctx, account, func(scoped board.Repository) error {
		item.Payload = board.Payload{"new": true}
		if err := scoped.Save(ctx, item); err != nil {
			return err
		}
		return abort
	}); !errors.Is(err, abort) {
		t.Fatalf("callback error: %v", err)
	}
	if got := boardItems(t, r, account); got[0].Payload["old"] != true {
		t.Fatal("callback rollback lost payload")
	}
	if err := r.WithinAccount(ctx, account, func(scoped board.Repository) error { _, err := scoped.List(ctx, id(2)); return err }); !errors.Is(err, boardpg.ErrAccountMismatch) {
		t.Fatalf("cross-account transaction: %v", err)
	}
	item.Payload = board.Payload{"invalid": make(chan int)}
	if err := r.Replace(ctx, account, []board.Item{item}); err == nil {
		t.Fatal("non-JSON payload accepted")
	}
	if got := boardItems(t, r, account); got[0].Payload["old"] != true {
		t.Fatal("marshal error lost prior rows")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := r.Replace(cancelled, account, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestPGBoardRestartPersistence(t *testing.T) {
	r := boardCandidate(database(t))
	ctx := context.Background()
	id := func(n int) string { return watcherID(0x85000000, n) }
	account := id(1)
	if os.Getenv("PG_TEST_PHASE") == "before" {
		if err := r.Replace(ctx, account, []board.Item{{ID: id(2), AccountID: account, Order: 42, Payload: board.Payload{"persisted": "board"}}}); err != nil {
			t.Fatal(err)
		}
		return
	}
	if os.Getenv("PG_TEST_PHASE") != "after" {
		t.Fatal("missing restart phase")
	}
	got := boardItems(t, r, account)
	if len(got) != 1 || got[0].ID != id(2) || got[0].Order != 42 || got[0].Payload["persisted"] != "board" {
		t.Fatalf("board lost across restart: %+v", got)
	}
}
