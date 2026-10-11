// SPDX-License-Identifier: AGPL-3.0-only
package petpg

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/pets"
	"strings"
	"sync"
	"testing"
	"time"
)

type petFixture struct {
	db         *sql.DB
	config     *pgx.ConnConfig
	ctx        context.Context
	actor      string
	guard      *authoritypg.TransactionGuard
	repo       *Repository
	authConfig authority.Config
	token      string
}

func petID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		t.Fatal(e)
	}
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}
func petExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, e := db.Exec(q, args...); e != nil {
		t.Fatal(e)
	}
}
func newPetFixture(t *testing.T) petFixture {
	t.Helper()
	dsn := os.Getenv("PET_TEST_DSN")
	if dsn == "" {
		t.Skip("real PostgreSQL not run: set PET_TEST_DSN for a disposable local pet_test database")
	}
	config, e := pgx.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	if os.Getenv("PET_TEST_DISPOSABLE") != "1" || config.Database != "pet_test" || config.User != "synthetic" || (config.Host != "127.0.0.1" && config.Host != "::1") {
		t.Fatal("requires PET_TEST_DISPOSABLE=1, loopback TCP, user synthetic, database pet_test")
	}
	root := stdlib.OpenDB(*config)
	t.Cleanup(func() { root.Close() })
	schema := "pet_" + strings.ReplaceAll(petID(t), "-", "")
	phase := ""
	if t.Name() == "TestPostgresPetServerRestartPersistence" {
		phase = os.Getenv("PET_TEST_RESTART_PHASE")
		schema = "pet_restart_fixture"
	}
	if phase != "after" {
		petExec(t, root, `CREATE SCHEMA `+schema)
	}
	if phase != "before" {
		t.Cleanup(func() { _, _ = root.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`) })
	}
	scoped := config.Copy()
	scoped.RuntimeParams["search_path"] = schema + ",pg_catalog"
	scoped.RuntimeParams["application_name"] = schema
	db := stdlib.OpenDB(*scoped)
	db.SetMaxOpenConns(12)
	t.Cleanup(func() { db.Close() })
	if phase != "after" {
		petExec(t, db, authoritypg.Schema)
		petExec(t, db, Schema)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	hash := make([]byte, 32)
	seal := make([]byte, 32)
	if _, e = rand.Read(hash); e != nil {
		t.Fatal(e)
	}
	if _, e = rand.Read(seal); e != nil {
		t.Fatal(e)
	}
	cfg := authority.Config{Issuer: "https://pet-tests.invalid", Audience: "synthetic-pets", SigningKeyID: "synthetic", SigningKey: key, TrustedKeys: map[string]*rsa.PublicKey{"synthetic": &key.PublicKey}, RefreshHashKey: hash, ReplaySealKey: seal, AccessTTL: 10 * time.Minute, SessionTTL: time.Hour, RefreshGrace: time.Minute, Now: time.Now}
	actor := petID(t)
	if phase != "" {
		actor = "b8910dea-d247-4b27-83b2-f03a93a931a0"
	}
	if phase != "after" {
		if e = authoritypg.SeedSynthetic(ctx, db, actor, "synthetic_pet_"+strings.ReplaceAll(actor, "-", ""), time.Now()); e != nil {
			t.Fatal(e)
		}
	}
	store := authoritypg.New(db)
	issuer, e := authority.New(store, cfg)
	if e != nil {
		t.Fatal(e)
	}
	pair, e := issuer.IssueSession(ctx, actor, authority.SessionOptions{Label: "synthetic pet integration", Scopes: []string{"*"}})
	if e != nil {
		t.Fatal(e)
	}
	verifier, e := authority.NewVerifier(store, cfg)
	if e != nil {
		t.Fatal(e)
	}
	p, e := verifier.Authenticate(ctx, pair.Token)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(store, verifier)
	return petFixture{db, scoped, guard.Bind(ctx, p), actor, guard, New(db, guard), cfg, pair.Token}
}
func (f petFixture) command(t *testing.T, version int64, kind string) pets.Command {
	t.Helper()
	return pets.Command{RequestID: petID(t), ExpectedVersion: version, Kind: kind}
}
func (f petFixture) adopt(t *testing.T) pets.State {
	t.Helper()
	c := f.command(t, 0, "adopt")
	c.Name = "Mochi"
	c.Species = "cloudling"
	s, e := f.repo.Mutate(f.ctx, f.actor, c)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func petMutation(t *testing.T, f petFixture, c pets.Command) pets.State {
	t.Helper()
	s, e := f.repo.Mutate(f.ctx, f.actor, c)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func petJournalCount(t *testing.T, f petFixture) int {
	t.Helper()
	var n int
	if e := f.db.QueryRow(`SELECT count(*) FROM owned_pet_commands WHERE account_id=$1`, f.actor).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}

func TestPostgresPetLifecycleReplayAndCoins(t *testing.T) {
	f := newPetFixture(t)
	if e := f.repo.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	if _, e := f.repo.Get(f.ctx, f.actor); !errors.Is(e, pets.ErrNotFound) {
		t.Fatal(e)
	}
	s := f.adopt(t)
	feed := f.command(t, s.Version, "feed")
	s = petMutation(t, f, feed)
	replay := petMutation(t, f, feed)
	if !reflect.DeepEqual(s, replay) || petJournalCount(t, f) != 2 {
		t.Fatal("replay changed state or journal")
	}
	changed := feed
	changed.Kind = "wash"
	if _, e := f.repo.Mutate(f.ctx, f.actor, changed); !errors.Is(e, pets.ErrConflict) {
		t.Fatal("request hash conflict", e)
	}
	stale := f.command(t, 1, "rename")
	stale.Name = "Old"
	if _, e := f.repo.Mutate(f.ctx, f.actor, stale); !errors.Is(e, pets.ErrConflict) {
		t.Fatal("CAS", e)
	}
	for _, kind := range []string{"wash", "play", "rest"} {
		s = petMutation(t, f, f.command(t, s.Version, kind))
	}
	for _, id := range []string{"breakfast", "playtime", "care_four"} {
		c := f.command(t, s.Version, "claim")
		c.TaskID = id
		s = petMutation(t, f, c)
	}
	if s.Coins != 48 || s.Experience != 60 {
		t.Fatalf("earned-only rewards: %+v", s)
	}
	c := f.command(t, s.Version, "buy")
	c.ItemID = "blue_scarf"
	s = petMutation(t, f, c)
	if s.Coins != 18 || s.Inventory["blue_scarf"] != 1 {
		t.Fatal("shop debit", s)
	}
	c = f.command(t, s.Version, "equip")
	c.ItemID = "blue_scarf"
	c.Slot = "neck"
	s = petMutation(t, f, c)
	if s.Equipment["neck"] != "blue_scarf" {
		t.Fatal("equipment")
	}
	c = f.command(t, s.Version, "buy")
	c.ItemID = "leaf_hat"
	n := petJournalCount(t, f)
	if _, e := f.repo.Mutate(f.ctx, f.actor, c); !errors.Is(e, pets.ErrRequirements) {
		t.Fatal("insufficient coins", e)
	}
	after, e := f.repo.Get(f.ctx, f.actor)
	if e != nil || after.Version != s.Version || after.Coins != s.Coins || petJournalCount(t, f) != n {
		t.Fatal("failed debit mutated state", e)
	}
	var delta, balance int64
	if e = f.db.QueryRow(`SELECT sum(coin_delta),max(version) FROM owned_pet_commands WHERE account_id=$1`, f.actor).Scan(&delta, &balance); e != nil {
		t.Fatal(e)
	}
	if delta != s.Coins || balance != s.Version {
		t.Fatal("journal balance does not reconcile")
	}
	// Old successful requests replay their original response even after newer mutations.
	old := petMutation(t, f, feed)
	if old.Version != 2 || old.Coins != 22 {
		t.Fatal("replay was recomputed", old)
	}
}
func TestPostgresPetConcurrentAdoptionAndCAS(t *testing.T) {
	f := newPetFixture(t)
	adopt := f.command(t, 0, "adopt")
	adopt.Name = "Mochi"
	adopt.Species = "foxlet"
	var wg sync.WaitGroup
	results := make(chan pets.State, 12)
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s, e := f.repo.Mutate(f.ctx, f.actor, adopt); results <- s; errs <- e }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	for s := range results {
		if s.Version != 1 {
			t.Fatal(s)
		}
	}
	if petJournalCount(t, f) != 1 {
		t.Fatal("duplicate adoption journal")
	}
	second := adopt
	second.RequestID = petID(t)
	if _, e := f.repo.Mutate(f.ctx, f.actor, second); !errors.Is(e, pets.ErrConflict) {
		t.Fatal("second pet accepted", e)
	}
	successes := 0
	conflicts := 0
	errs = make(chan error, 12)
	for i := 0; i < 12; i++ {
		c := f.command(t, 1, "buy")
		c.ItemID = "snack"
		wg.Add(1)
		go func(c pets.Command) { defer wg.Done(); _, e := f.repo.Mutate(f.ctx, f.actor, c); errs <- e }(c)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e == nil {
			successes++
		} else if errors.Is(e, pets.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	s, e := f.repo.Get(f.ctx, f.actor)
	if e != nil || successes != 1 || conflicts != 11 || s.Coins != 18 || s.Inventory["snack"] != 6 || petJournalCount(t, f) != 2 {
		t.Fatal("concurrent debit/CAS", successes, conflicts, s, e)
	}
	claim := f.command(t, s.Version, "feed")
	s = petMutation(t, f, claim)
	claim = f.command(t, s.Version, "claim")
	claim.TaskID = "breakfast"
	errs = make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := f.repo.Mutate(f.ctx, f.actor, claim); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	s, e = f.repo.Get(f.ctx, f.actor)
	if e != nil || s.Coins != 25 || !s.Daily.Claimed["breakfast"] || petJournalCount(t, f) != 4 {
		t.Fatal("double quest grant", s, e)
	}
}
func TestPostgresPetAuthorityAndRollback(t *testing.T) {
	f := newPetFixture(t)
	s := f.adopt(t)
	if _, e := f.repo.Get(context.Background(), f.actor); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("missing proof", e)
	}
	if _, e := f.repo.Get(f.ctx, petID(t)); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("cross account proof", e)
	}
	if _, e := New(f.db, nil).Get(f.ctx, f.actor); !errors.Is(e, pets.ErrUnavailable) {
		t.Fatal("missing guard", e)
	}
	forged := f.guard.Bind(context.Background(), authority.Principal{Account: authority.Account{ID: f.actor}})
	if _, e := f.repo.Get(forged, f.actor); e == nil {
		t.Fatal("forged principal accepted")
	}
	// A journal failure must roll back the preceding pet update and coin effects.
	petExec(t, f.db, `CREATE FUNCTION fail_pet_journal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='buy' THEN RAISE EXCEPTION 'synthetic journal failure'; END IF; RETURN NEW; END $$`)
	petExec(t, f.db, `CREATE TRIGGER fail_pet_journal BEFORE INSERT ON owned_pet_commands FOR EACH ROW EXECUTE FUNCTION fail_pet_journal()`)
	c := f.command(t, s.Version, "buy")
	c.ItemID = "snack"
	if _, e := f.repo.Mutate(f.ctx, f.actor, c); e == nil {
		t.Fatal("expected injected journal failure")
	}
	after, e := f.repo.Get(f.ctx, f.actor)
	if e != nil || after.Version != s.Version || after.Coins != 20 || after.Inventory["snack"] != 5 || petJournalCount(t, f) != 1 {
		t.Fatal("partial commit", after, e)
	}
	petExec(t, f.db, `DROP TRIGGER fail_pet_journal ON owned_pet_commands`)
	s = petMutation(t, f, c)
	petExec(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
	if _, e = f.repo.Get(f.ctx, f.actor); !errors.Is(e, authority.ErrDisabled) {
		t.Fatal("disabled read", e)
	}
	if _, e = f.repo.Mutate(f.ctx, f.actor, c); !errors.Is(e, authority.ErrDisabled) {
		t.Fatal("disabled replay", e)
	}
	petExec(t, f.db, `UPDATE owned_authority_accounts SET disabled=false WHERE id=$1`, f.actor)
	if after, e = f.repo.Get(f.ctx, f.actor); e != nil || after.Version != s.Version {
		t.Fatal(after, e)
	}
}
func TestPostgresPetLazyDecayAndFreshPoolPersistence(t *testing.T) {
	f := newPetFixture(t)
	s := f.adopt(t)
	adoptedVersion := s.Version
	anchor := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339Nano)
	petExec(t, f.db, `UPDATE owned_pets SET state=jsonb_set(state,'{settled_at}',to_jsonb($2::text)) WHERE account_id=$1`, f.actor, anchor)
	a, e := f.repo.Get(f.ctx, f.actor)
	if e != nil {
		t.Fatal(e)
	}
	b, e := f.repo.Get(f.ctx, f.actor)
	if e != nil {
		t.Fatal(e)
	}
	if a.Stats != b.Stats || a.Stats.Satiety != 72 || a.Version != adoptedVersion {
		t.Fatal("read settlement charged twice", a, b)
	}
	var persisted string
	if e = f.db.QueryRow(`SELECT state->>'settled_at' FROM owned_pets WHERE account_id=$1`, f.actor).Scan(&persisted); e != nil || persisted != anchor {
		t.Fatal("GET wrote settlement", persisted, e)
	}
	c := f.command(t, a.Version, "rename")
	c.Name = "Mochi II"
	s = petMutation(t, f, c)
	// Close the original pool, create a new pool/guard/verifier and authenticate
	// the same session: no in-memory pet/cache/clock state is required to recover.
	if e = f.db.Close(); e != nil {
		t.Fatal(e)
	}
	db := stdlib.OpenDB(*f.config)
	defer db.Close()
	store := authoritypg.New(db)
	v, e := authority.NewVerifier(store, f.authConfig)
	if e != nil {
		t.Fatal(e)
	}
	p, e := v.Authenticate(context.Background(), f.token)
	if e != nil {
		t.Fatal(e)
	}
	g := authoritypg.NewTransactionGuard(store, v)
	repo := New(db, g)
	ctx := g.Bind(context.Background(), p)
	recovered, e := repo.Get(ctx, f.actor)
	if e != nil || !reflect.DeepEqual(s, recovered) {
		t.Fatal("fresh pool recovery", s, recovered, e)
	}
	replay, e := repo.Mutate(ctx, f.actor, c)
	if e != nil || !reflect.DeepEqual(s, replay) {
		t.Fatal("fresh pool replay", replay, e)
	}
	var stored []byte
	if e = db.QueryRow(`SELECT state FROM owned_pets WHERE account_id=$1`, f.actor).Scan(&stored); e != nil {
		t.Fatal(e)
	}
	var state pets.State
	if e = json.Unmarshal(stored, &state); e != nil {
		t.Fatal(e)
	}
	if state.Name != "Mochi II" || state.Version != 2 {
		t.Fatal(fmt.Sprint(state))
	}
}

type petFinalCheckGuard struct {
	inner TransactionGuard
	calls int
}

func (g *petFinalCheckGuard) Authenticate(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string) (authority.Principal, error) {
	g.calls++
	if g.calls == 2 {
		return authority.Principal{}, authority.ErrRevoked
	}
	return g.inner.Authenticate(ctx, db, tx, actor)
}
func TestPostgresPetFinalGuardFailureRollsBack(t *testing.T) {
	f := newPetFixture(t)
	s := f.adopt(t)
	g := &petFinalCheckGuard{inner: f.guard}
	repo := New(f.db, g)
	c := f.command(t, s.Version, "feed")
	if _, e := repo.Mutate(f.ctx, f.actor, c); !errors.Is(e, authority.ErrRevoked) {
		t.Fatal("final check", e)
	}
	actual, e := f.repo.Get(f.ctx, f.actor)
	if e != nil || actual.Version != s.Version || actual.Coins != s.Coins || actual.Inventory["snack"] != s.Inventory["snack"] || petJournalCount(t, f) != 1 {
		t.Fatal("final guard partial commit", actual, e)
	}
	// A matching DSN is not enough: guard and domain must use the exact same pool.
	other := stdlib.OpenDB(*f.config)
	defer other.Close()
	if _, e = New(other, f.guard).Get(f.ctx, f.actor); !errors.Is(e, authority.ErrUnavailable) {
		t.Fatal("different pool accepted", e)
	}
}
func TestPostgresPetAccountWaitRechecksDisable(t *testing.T) {
	f := newPetFixture(t)
	s := f.adopt(t)
	lock, e := f.db.BeginTx(f.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Rollback()
	if _, e = lock.ExecContext(f.ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, f.actor); e != nil {
		t.Fatal(e)
	}
	result := make(chan error, 1)
	c := f.command(t, s.Version, "feed")
	go func() { _, e := f.repo.Mutate(f.ctx, f.actor, c); result <- e }()
	deadline := time.Now().Add(3 * time.Second)
	waiting := false
	for time.Now().Before(deadline) {
		if e = f.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND application_name=$1 AND wait_event_type='Lock')`, f.config.RuntimeParams["application_name"]).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("pet mutation did not wait on canonical account lock")
	}
	if _, e = lock.ExecContext(f.ctx, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor); e != nil {
		t.Fatal(e)
	}
	if e = lock.Commit(); e != nil {
		t.Fatal(e)
	}
	select {
	case e = <-result:
		if !errors.Is(e, authority.ErrDisabled) {
			t.Fatal("wait used stale proof", e)
		}
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	if petJournalCount(t, f) != 1 {
		t.Fatal("disabled waiter committed mutation")
	}
}

// Run before/after phases in separate test processes around an actual pg_ctl
// restart. The fixed fixture is allowed only inside the disposable pet_test DB.
func TestPostgresPetServerRestartPersistence(t *testing.T) {
	phase := os.Getenv("PET_TEST_RESTART_PHASE")
	if phase != "before" && phase != "after" {
		t.Skip("requires explicit before/after processes and a PostgreSQL restart between them")
	}
	f := newPetFixture(t)
	adopt := pets.Command{RequestID: "restart-adopt-00000001", Kind: "adopt", Name: "Persistent", Species: "mossling"}
	feed := pets.Command{RequestID: "restart-feed-00000001", ExpectedVersion: 1, Kind: "feed"}
	if phase == "before" {
		petMutation(t, f, adopt)
		petMutation(t, f, feed)
		return
	}
	if e := f.repo.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	s, e := f.repo.Get(f.ctx, f.actor)
	if e != nil || s.Name != "Persistent" || s.Version != 2 || s.Coins != 22 || s.Inventory["snack"] != 4 || petJournalCount(t, f) != 2 {
		t.Fatal("server restart state", s, e)
	}
	replay := petMutation(t, f, feed)
	if replay.Version != 2 || replay.Coins != 22 || replay.Inventory["snack"] != 4 || petJournalCount(t, f) != 2 {
		t.Fatal("server restart replay", replay)
	}
	changed := feed
	changed.Kind = "wash"
	if _, e = f.repo.Mutate(f.ctx, f.actor, changed); !errors.Is(e, pets.ErrConflict) {
		t.Fatal("server restart hash conflict", e)
	}
}

func TestPostgresPetHTTPIntegration(t *testing.T) {
	f := newPetFixture(t)
	// Reapplying the explicit initial migration must preserve existing authority.
	petExec(t, f.db, Schema)
	verifier, e := authority.NewVerifier(authoritypg.New(f.db), f.authConfig)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(f.db), verifier)
	handler := (&pets.Service{Repository: New(f.db, guard), Authority: verifier, Bind: guard.Bind}).Handler()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+f.token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	adoption := `{"request_id":"http-adopt-00000001","expected_version":0,"name":"HTTP Pet","species":"cloudling"}`
	w := request(http.MethodPost, "/api/pets/me/adopt", adoption)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	original := w.Body.String()
	w = request(http.MethodPost, "/api/pets/me/adopt", adoption)
	if w.Code != 200 || w.Body.String() != original || petJournalCount(t, f) != 1 {
		t.Fatal("HTTP replay", w.Code, w.Body.String())
	}
	w = request(http.MethodPost, "/api/pets/me/actions", `{"request_id":"http-feed-000000001","expected_version":1,"kind":"feed"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var s pets.State
	if e = json.Unmarshal(w.Body.Bytes(), &s); e != nil || s.Version != 2 || s.Coins != 22 {
		t.Fatal(s, e)
	}
	w = request(http.MethodPost, "/api/pets/me/commands", `{"request_id":"http-forged-0000001","expected_version":2,"kind":"feed","account_id":"`+f.actor+`"}`)
	if w.Code != 400 || petJournalCount(t, f) != 2 {
		t.Fatal("caller authority accepted", w.Code, w.Body.String())
	}
	w = request(http.MethodPatch, "/api/pets/me", `{"request_id":"http-stale-00000001","expected_version":1,"name":"Stale"}`)
	if w.Code != 409 || petJournalCount(t, f) != 2 {
		t.Fatal("HTTP stale CAS", w.Code, w.Body.String())
	}
	if _, e = f.db.Exec(`UPDATE owned_pets SET coins=-1 WHERE account_id=$1`, f.actor); e == nil {
		t.Fatal("schema allowed negative balance")
	}
	petExec(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
	w = request(http.MethodPost, "/api/pets/me/adopt", adoption)
	if w.Code != 423 || petJournalCount(t, f) != 2 {
		t.Fatal("disabled HTTP replay", w.Code, w.Body.String())
	}
}
