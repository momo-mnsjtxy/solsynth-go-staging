// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/rewindpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/rewind"
	"solsynth.local/go-migration/ports/sphere/posts"
)

const configuredRewindDictionary = "北京大学 1000\n北京 1\n大学 1\n研究 100\n生命 100\n"

func configuredRewindYear(now time.Time) int {
	year := now.UTC().Year()
	_, end, _ := rewind.Window(year)
	if !now.Before(end) {
		year++
	}
	return year
}

func configuredRewindDictionaryFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dictionary.txt")
	if e := os.WriteFile(path, []byte(configuredRewindDictionary), 0600); e != nil {
		t.Fatal(e)
	}
	return path
}

type configuredRewindFixture struct {
	db                               *sql.DB
	actor, token, session, publisher string
	config                           authority.Config
	guard                            *authoritypg.TransactionGuard
	verifier                         *authority.Verifier
	ctx                              context.Context
	repo                             *rewindpg.Repository
	posts                            *postpg.Repository
}

// This helper creates the original one-hour proof BEFORE publisher/post/report
// targets. It never runs in the after phase and never performs schema DDL.
func newConfiguredRewindFixture(t *testing.T, db *sql.DB, c rewindpg.DictionaryConfig) configuredRewindFixture {
	t.Helper()
	f := configuredRewindFixture{db: db, actor: authorityUUID(t)}
	now := &atomic.Int64{}
	now.Store(time.Now().UTC().Unix())
	f.config = authorityConfig(t, authorityKeys(t), now)
	f.config.Now = time.Now
	f.config.AccessTTL, f.config.SessionTTL = time.Hour, time.Hour
	store := authoritypg.New(db)
	if e := authoritypg.SeedSynthetic(context.Background(), db, f.actor, "synthetic_rewind_adapter_"+strings.ReplaceAll(f.actor, "-", ""), time.Now().UTC()); e != nil {
		t.Fatal(e)
	}
	if e := store.ReplacePolicy(context.Background(), f.actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	issuer, e := authority.New(store, f.config)
	if e != nil {
		t.Fatal(e)
	}
	f.token = pgIssue(t, issuer, f.actor).Token
	f.verifier, e = authority.NewVerifier(store, f.config)
	if e != nil {
		t.Fatal(e)
	}
	f.guard = authoritypg.NewTransactionGuard(store, f.verifier)
	p, e := f.verifier.Authenticate(context.Background(), f.token)
	if e != nil {
		t.Fatal(e)
	}
	f.session = p.Session.ID
	f.ctx = f.guard.Bind(context.Background(), p)
	visible := socialpg.NewWithAuthority(db, f.guard)
	query(t, db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at)VALUES($1,4,clock_timestamp()-interval '1 second')`, f.actor)
	name := "rewind_adapter_" + f.actor
	pub, e := accountpg.NewWithAuthority(db, f.guard).WithRealms(visible).CreatePublisher(f.ctx, f.actor, accounts.PublisherPatch{Name: &name, Nick: &name})
	if e != nil {
		t.Fatal(e)
	}
	f.publisher = pub.ID
	f.posts = postpg.NewWithAuthority(db, f.guard).WithVisibility(visible)
	f.repo = configuredRewindRepository(t, db, f.config, c)
	return f
}
func configuredRewindRepository(t *testing.T, db *sql.DB, c authority.Config, dictionary rewindpg.DictionaryConfig) *rewindpg.Repository {
	return configuredRewindRepositoryYear(t, db, c, dictionary, configuredRewindYear(c.Now()))
}
func configuredRewindRepositoryYear(t *testing.T, db *sql.DB, c authority.Config, dictionary rewindpg.DictionaryConfig, year int) *rewindpg.Repository {
	t.Helper()
	store := authoritypg.New(db)
	verifier, e := authority.NewVerifier(store, c)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(store, verifier)
	social := socialpg.NewWithAuthority(db, guard)
	chats, e := chatpg.NewWithAuthority(db, chatPolicy, guard)
	if e != nil {
		t.Fatal(e)
	}
	chats.WithSocial(social)
	r, e := rewindpg.New(db, guard, accountpg.NewWithAuthority(db, guard).WithRealms(social), chats, social, rewindpg.Options{Now: c.Now, ReportingYear: year, CanonicalActionActivity: true, Dictionary: &dictionary})
	if e != nil {
		t.Fatal(e)
	}
	if e = r.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	return r
}
func configuredRewindPost(t *testing.T, f configuredRewindFixture, body string, private bool) posts.Post {
	t.Helper()
	visible := posts.Public
	if private {
		visible = posts.Private
	}
	p, e := f.posts.Create(f.ctx, f.actor, f.publisher, posts.Patch{Content: &body, Visibility: &visible})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func TestPGRewindActualCanonicalActivityDictionaryHostAndPrivacy(t *testing.T) {
	f := newConfiguredRewindFixture(t, database(t), rewindpg.DictionaryConfig{DictionaryPath: configuredRewindDictionaryFile(t)})
	configuredRewindPost(t, f, "北京大学研究生命 北京大学", true)
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	dictionary := rewindpg.DictionaryConfig{DictionaryPath: configuredRewindDictionaryFile(t)}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, Rewind: &rewindpg.Options{ReportingYear: configuredRewindYear(f.config.Now()), CanonicalActionActivity: true, Dictionary: &dictionary}})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	point := backendDecode[rewind.Point](t, backendCall(t, app, "GET", "/api/rewind/me", f.token, "", 200))
	if len(point.UnavailableComponents) != 0 {
		t.Fatal(point.UnavailableComponents)
	}
	raw, _ := json.Marshal(point)
	if !strings.Contains(string(raw), `"word":"北京大学","count":2`) || !strings.Contains(string(raw), `"most_active_day":"`+time.Now().UTC().Format("2006-01-02")) {
		t.Fatal("configured adapters not used", string(raw))
	}
	if _, e = f.repo.Current(context.Background(), f.actor); e == nil {
		t.Fatal("claimed actor accepted")
	}
	shared, e := f.repo.Share(f.ctx, f.actor, point.Year, true)
	if e != nil {
		t.Fatal(e)
	}
	public, e := f.repo.Public(context.Background(), *shared.SharableCode)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ = json.Marshal(public)
	if strings.Contains(string(raw), "北京大学") || !strings.Contains(string(raw), "sphere.private_text_aggregation_redacted") {
		t.Fatal("private word leakage", string(raw))
	}
}
func TestPGRewindActualAdaptersBoundedCorpusAndActionRows(t *testing.T) {
	for _, c := range []rewindpg.DictionaryConfig{
		{MaxInputBytes: 1}, {MaxTexts: 1}, {MaxTokens: 1},
	} {
		c.DictionaryPath = configuredRewindDictionaryFile(t)
		f := newConfiguredRewindFixture(t, database(t), c)
		configuredRewindPost(t, f, "北京大学北京大学", false)
		configuredRewindPost(t, f, "研究生命", false)
		p, e := f.repo.Current(f.ctx, f.actor)
		if e != nil || !containsRewindUnavailable(p, "sphere.top_words_token_limit") {
			t.Fatal("token/corpus bound not explicit", p, e)
		}
	}
	f := newConfiguredRewindFixture(t, database(t), rewindpg.DictionaryConfig{DictionaryPath: configuredRewindDictionaryFile(t)})
	name := "rewind_second_" + f.actor
	if _, e := accountpg.NewWithAuthority(f.db, f.guard).CreatePublisher(f.ctx, f.actor, accounts.PublisherPatch{Name: &name, Nick: &name}); e != nil {
		t.Fatal(e)
	}
	logs, e := rewindpg.NewCanonicalActionLogs(f.db)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := f.db.BeginTx(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	start, end, _ := rewind.Window(configuredRewindYear(time.Now()))
	if _, e = logs.ActionTimes(f.ctx, f.db, tx, f.actor, start, end, 1); !errors.Is(e, rewind.ErrUnavailable) {
		t.Fatal("action overflow silently truncated", e)
	}
	if _, e = logs.ActionTimes(f.ctx, database(t), tx, f.actor, start, end, 100); !errors.Is(e, rewind.ErrInvalid) {
		t.Fatal("foreign pool accepted", e)
	}
}
func containsRewindUnavailable(p rewind.Point, component string) bool {
	for _, v := range p.UnavailableComponents {
		if v == component {
			return true
		}
	}
	return false
}
func TestPGRewindConfiguredOriginalProofRevokedWhileAccountBlocked(t *testing.T) {
	f := newConfiguredRewindFixture(t, database(t), rewindpg.DictionaryConfig{DictionaryPath: configuredRewindDictionaryFile(t)})
	configuredRewindPost(t, f, "北京大学研究生命", false)
	locks := atomicAuthorityFixture{db: f.db, ctx: context.Background()}
	blocker, pid := atomicBlocker(t, locks, "owned_authority_accounts", f.actor)
	done := make(chan error, 1)
	go func() { _, e := f.repo.Current(f.ctx, f.actor); done <- e }()
	waitAtomicBlocked(t, locks, pid, "owned_authority_accounts")
	if _, e := blocker.Exec(`UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE id=$1`, f.session); e != nil {
		t.Fatal(e)
	}
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; e == nil {
		t.Fatal("revoked original proof created report after lock wait")
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_account_rewind_points WHERE account_id=$1`, f.actor); n != 0 {
		t.Fatal("revoked snapshot persisted", n)
	}
}

func TestPGRewindConfiguredAdaptersSavedRestartPersistence(t *testing.T) {
	_ = database(t)
	phase := fixtureRestartPhase(t)
	type state struct {
		Actor, Token, Point, Code, Row, Actions, DictionaryHash string
		Year                                                    int
		Authority                                               restartAuthoritySnapshot
		Database                                                isolatedRestartDatabase
	}
	var saved state
	var db *sql.DB
	path := restartFixturePath(t, "rewind-configured-dictionary.txt")
	if phase == "before" {
		var identity isolatedRestartDatabase
		db, identity = createIsolatedRestartDatabase(t, "rewind")
		file, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = file.WriteString(configuredRewindDictionary); e != nil {
			file.Close()
			t.Fatal(e)
		}
		if e = file.Close(); e != nil {
			t.Fatal(e)
		}
		f := newConfiguredRewindFixture(t, db, rewindpg.DictionaryConfig{DictionaryPath: path})
		configuredRewindPost(t, f, "北京大学研究生命北京大学", true)
		p, e := f.repo.Current(f.ctx, f.actor)
		if e != nil {
			t.Fatal(e)
		}
		p, e = f.repo.Share(f.ctx, f.actor, p.Year, true)
		if e != nil {
			t.Fatal(e)
		}
		saved = state{Actor: f.actor, Token: f.token, Point: p.ID, Code: *p.SharableCode, Authority: saveAuthoritySnapshot(t, f.config), Database: identity, Year: p.Year, DictionaryHash: fmt.Sprintf("%x", sha256.Sum256([]byte(configuredRewindDictionary)))}
		if e = db.QueryRow(`SELECT to_jsonb(p)::text FROM owned_account_rewind_points p WHERE id=$1`, saved.Point).Scan(&saved.Row); e != nil {
			t.Fatal(e)
		}
		if e = db.QueryRow(`SELECT COALESCE(jsonb_agg(to_jsonb(a) ORDER BY a.id)::text,'[]') FROM owned_account_action_history a WHERE account_id=$1`, saved.Actor).Scan(&saved.Actions); e != nil {
			t.Fatal(e)
		}
		saveRestartFixture(t, "rewind-configured-adapters.json", saved)
		return
	}
	loadRestartFixture(t, "rewind-configured-adapters.json", &saved)
	db = openIsolatedRestartDatabase(t, saved.Database)
	config := restoreAuthoritySnapshot(t, saved.Authority)
	repo := configuredRewindRepositoryYear(t, db, config, rewindpg.DictionaryConfig{DictionaryPath: path, DictionarySHA256: saved.DictionaryHash}, saved.Year)
	store := authoritypg.New(db)
	verifier, e := authority.NewVerifier(store, config)
	if e != nil {
		t.Fatal(e)
	}
	proof, e := verifier.Authenticate(context.Background(), saved.Token)
	if e != nil {
		t.Fatal(e)
	}
	ctx := authoritypg.NewTransactionGuard(store, verifier).Bind(context.Background(), proof)
	var before, actions string
	if e = db.QueryRow(`SELECT to_jsonb(p)::text FROM owned_account_rewind_points p WHERE id=$1`, saved.Point).Scan(&before); e != nil || before != saved.Row {
		t.Fatal("original snapshot changed", e)
	}
	if e = db.QueryRow(`SELECT COALESCE(jsonb_agg(to_jsonb(a) ORDER BY a.id)::text,'[]') FROM owned_account_action_history a WHERE account_id=$1`, saved.Actor).Scan(&actions); e != nil || actions != saved.Actions {
		t.Fatal("original canonical actions changed", e)
	}
	own, e := repo.Current(ctx, saved.Actor)
	if e != nil || own.ID != saved.Point || len(own.UnavailableComponents) != 0 {
		t.Fatal("configured report missing after restart", own, e)
	}
	words := own.Data["sphere"].(map[string]any)["top_words"].([]rewind.Word)
	if !reflect.DeepEqual(words, []rewind.Word{{Word: "北京大学", Count: 2}, {Word: "生命", Count: 1}, {Word: "研究", Count: 1}}) {
		t.Fatal("original words missing", words)
	}
	public, e := repo.Public(context.Background(), saved.Code)
	if e != nil || public.ID != saved.Point || public.Data["sphere"].(map[string]any)["top_words"] != nil {
		t.Fatal("sharing/privacy changed after restart", public, e)
	}
	var after string
	if e = db.QueryRow(`SELECT to_jsonb(p)::text FROM owned_account_rewind_points p WHERE id=$1`, saved.Point).Scan(&after); e != nil || after != saved.Row {
		t.Fatal("after phase repaired original snapshot", e)
	}
}

// This coordination wrapper delegates every word computation to the real local
// dictionary tokenizer. It lets the PG test prove that provider work begins only
// after canonical SQL account locks are released, and then revalidate the actual
// original proof/current corpus rather than caching the first authorized input.
type rewindPausedDictionary struct {
	actual           *rewindpg.DictionaryTokenizer
	entered, release chan struct{}
	once             sync.Once
}

func (p *rewindPausedDictionary) TopWords(ctx context.Context, texts []string, limit int) ([]rewind.Word, error) {
	wait := false
	p.once.Do(func() { wait = true; close(p.entered) })
	if wait {
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return p.actual.TopWords(ctx, texts, limit)
}
func TestPGRewindTokenizerReleasesAccountLockAndRevalidatesOriginalProofAndCorpus(t *testing.T) {
	for _, kind := range []string{"revoke", "edit"} {
		t.Run(kind, func(t *testing.T) {
			dictionary := rewindpg.DictionaryConfig{DictionaryPath: configuredRewindDictionaryFile(t)}
			f := newConfiguredRewindFixture(t, database(t), dictionary)
			post := configuredRewindPost(t, f, "北京大学北京大学", false)
			actual, e := rewindpg.NewDictionaryTokenizer(dictionary)
			if e != nil {
				t.Fatal(e)
			}
			paused := &rewindPausedDictionary{actual: actual, entered: make(chan struct{}), release: make(chan struct{})}
			social := socialpg.NewWithAuthority(f.db, f.guard)
			chats, e := chatpg.NewWithAuthority(f.db, chatPolicy, f.guard)
			if e != nil {
				t.Fatal(e)
			}
			chats.WithSocial(social)
			repo, e := rewindpg.New(f.db, f.guard, accountpg.NewWithAuthority(f.db, f.guard).WithRealms(social), chats, social, rewindpg.Options{Now: f.config.Now, ReportingYear: configuredRewindYear(f.config.Now()), CanonicalActionActivity: true, Tokenizer: paused})
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
			defer cancel()
			type result struct {
				point rewind.Point
				err   error
			}
			done := make(chan result, 1)
			go func() { p, e := repo.Current(ctx, f.actor); done <- result{p, e} }()
			select {
			case <-paused.entered:
			case <-ctx.Done():
				t.Fatal("provider did not begin", ctx.Err())
			}
			if kind == "revoke" {
				// Proper canonical account -> session lock order must be available while
				// the tokenizer runs. A hidden held account lock causes this timeout.
				write, cancelWrite := context.WithTimeout(context.Background(), 2*time.Second)
				tx, e := f.db.BeginTx(write, nil)
				if e != nil {
					cancelWrite()
					t.Fatal(e)
				}
				var actor string
				if e = tx.QueryRowContext(write, `SELECT id::text FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, f.actor).Scan(&actor); e != nil {
					tx.Rollback()
					cancelWrite()
					t.Fatal("tokenizer held account lock", e)
				}
				if _, e = tx.ExecContext(write, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE id=$1`, f.session); e != nil {
					tx.Rollback()
					cancelWrite()
					t.Fatal(e)
				}
				if e = tx.Commit(); e != nil {
					cancelWrite()
					t.Fatal(e)
				}
				cancelWrite()
			} else {
				write, cancelWrite := context.WithTimeout(f.ctx, 2*time.Second)
				content := "研究生命"
				_, e = f.posts.Update(write, f.actor, post.ID, posts.Patch{Content: &content})
				cancelWrite()
				if e != nil {
					t.Fatal("tokenizer held account lock", e)
				}
			}
			close(paused.release)
			received := <-done
			if kind == "revoke" {
				if received.err == nil || commerceCount(t, f.db, `SELECT count(*) FROM owned_account_rewind_points WHERE account_id=$1`, f.actor) != 0 {
					t.Fatal("stale original proof stored report", received.err)
				}
			} else {
				if received.err != nil {
					t.Fatal(received.err)
				}
				raw, _ := json.Marshal(received.point)
				if strings.Contains(string(raw), `"word":"北京大学"`) || !strings.Contains(string(raw), `"word":"研究","count":1`) {
					t.Fatal("first captured corpus persisted despite edit", string(raw))
				}
			}
		})
	}
}
