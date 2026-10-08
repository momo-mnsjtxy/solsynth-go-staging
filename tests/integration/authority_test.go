// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
)

func authorityConfig(t *testing.T, dir string, now *atomic.Int64) authority.Config {
	t.Helper()
	b, e := os.ReadFile(filepath.Join(dir, "synthetic-signing.pem"))
	if e != nil {
		t.Fatal(e)
	}
	p, _ := pem.Decode(b)
	if p == nil {
		t.Fatal("fixture PEM")
	}
	key, e := x509.ParsePKCS1PrivateKey(p.Bytes)
	if e != nil {
		t.Fatal(e)
	}
	hash, e := os.ReadFile(filepath.Join(dir, "synthetic-hash.bin"))
	if e != nil {
		t.Fatal(e)
	}
	seal, e := os.ReadFile(filepath.Join(dir, "synthetic-seal.bin"))
	if e != nil {
		t.Fatal(e)
	}
	return authority.Config{Issuer: "https://disposable-authority.invalid", Audience: "disposable-ci", SigningKeyID: "synthetic", SigningKey: key, TrustedKeys: map[string]*rsa.PublicKey{"synthetic": &key.PublicKey}, RefreshHashKey: hash, ReplaySealKey: seal, AccessTTL: 10 * time.Minute, SessionTTL: time.Hour, RefreshGrace: 60 * time.Second, Now: func() time.Time { return time.Unix(now.Load(), 0) }}
}
func authorityKeys(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	k, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	key := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	if e = os.WriteFile(filepath.Join(dir, "synthetic-signing.pem"), key, 0600); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"synthetic-hash.bin", "synthetic-seal.bin"} {
		b := make([]byte, 32)
		if _, e = rand.Read(b); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dir, name), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	return dir
}
func authorityUUID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		t.Fatal(e)
	}
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
func newPGAuthority(t *testing.T, db *sql.DB, dir string, now *atomic.Int64) *authority.Service {
	t.Helper()
	s, e := authority.New(authoritypg.New(db), authorityConfig(t, dir, now))
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func pgIssue(t *testing.T, s *authority.Service, a string) authority.Pair {
	t.Helper()
	p, e := s.IssueSession(context.Background(), a, authority.SessionOptions{Label: "synthetic integration", Scopes: []string{"*"}})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func TestPGAuthorityLifecycle(t *testing.T) {
	db := database(t)
	ctx := context.Background()
	if _, e := db.ExecContext(ctx, authoritypg.Schema); e != nil {
		t.Fatal(e)
	}
	now := &atomic.Int64{}
	now.Store(time.Now().UTC().Unix())
	dir := authorityKeys(t)
	a, b := authorityUUID(t), authorityUUID(t)
	for _, id := range []string{a, b} {
		if e := authoritypg.SeedSynthetic(ctx, db, id, "synthetic_authority_"+strings.ReplaceAll(id, "-", ""), time.Unix(now.Load(), 0)); e != nil {
			t.Fatal(e)
		}
	}
	s := newPGAuthority(t, db, dir, now)
	repo := authoritypg.New(db)
	p := pgIssue(t, s, a)
	other := pgIssue(t, s, b)
	pa, e := s.Authenticate(ctx, p.Token)
	if e != nil || pa.Account.ID != a {
		t.Fatal(pa, e)
	}
	pb, e := s.Authenticate(ctx, other.Token)
	if e != nil {
		t.Fatal(e)
	}
	t.Run("isolation_and_policy", func(t *testing.T) {
		for _, ref := range []string{a, "synthetic_authority_" + strings.ReplaceAll(a, "-", "")} {
			resolved, e := repo.LookupAccount(ctx, ref)
			if e != nil || resolved.ID != a {
				t.Fatal("account lookup", e)
			}
		}
		if _, e := repo.LookupAccount(ctx, "missing_synthetic_name"); !errors.Is(e, authority.ErrNotFound) {
			t.Fatal("missing account lookup", e)
		}

		if e := s.Revoke(ctx, p.Token, pb.Session.ID); e == nil {
			t.Fatal("cross-account revoke succeeded")
		}
		if _, e := s.Authenticate(ctx, other.Token); e != nil {
			t.Fatal(e)
		}
		if _, e := s.Authorize(ctx, p.Token, "posts.create"); !errors.Is(e, authority.ErrForbidden) {
			t.Fatal(e)
		}
		if e := repo.ReplacePolicy(ctx, a, []authority.Rule{{Pattern: "posts.*"}}, nil); e != nil {
			t.Fatal(e)
		}
		if _, e := s.Authorize(ctx, p.Token, "posts.create"); e != nil {
			t.Fatal(e)
		}
		end := time.Unix(now.Load()+1, 0)
		if e := repo.ReplacePolicy(ctx, a, []authority.Rule{{Pattern: "posts.*"}}, []authority.Punishment{{Kind: "permission_modification", Pattern: "posts.*", ExpiresAt: &end}}); e != nil {
			t.Fatal(e)
		}
		if _, e := s.Authorize(ctx, p.Token, "posts.create"); !errors.Is(e, authority.ErrForbidden) {
			t.Fatal(e)
		}
		now.Add(1)
		if _, e := s.Authorize(ctx, p.Token, "posts.create"); e != nil {
			t.Fatal(e)
		}
	})
	var refreshed authority.Pair
	t.Run("concurrent_refresh_and_restart", func(t *testing.T) {
		const n = 12
		pairs := make(chan authority.Pair, n)
		errs := make(chan error, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				pair, e := newPGAuthority(t, database(t), dir, now).Refresh(ctx, p.RefreshToken, 0, "")
				pairs <- pair
				errs <- e
			}()
		}
		wg.Wait()
		close(pairs)
		close(errs)
		for e := range errs {
			if e != nil {
				t.Fatal(e)
			}
		}
		for pair := range pairs {
			if refreshed.Token == "" {
				refreshed = pair
			}
			if refreshed.Token != pair.Token || refreshed.RefreshToken != pair.RefreshToken {
				t.Fatal("refresh grace diverged")
			}
		}
		if _, e := s.Authenticate(ctx, p.Token); !errors.Is(e, authority.ErrStale) {
			t.Fatal(e)
		}
		if _, e := newPGAuthority(t, database(t), dir, now).Authenticate(ctx, refreshed.Token); e != nil {
			t.Fatal(e)
		}
		var epoch int64
		var hash, previous, sealed []byte
		if e := db.QueryRowContext(ctx, `SELECT epoch,refresh_hash,previous_hash,sealed_pair FROM owned_authority_sessions WHERE id=$1`, pa.Session.ID).Scan(&epoch, &hash, &previous, &sealed); e != nil {
			t.Fatal(e)
		}
		if epoch != 2 || len(hash) != 32 || len(previous) != 32 || bytes.Contains(sealed, []byte(refreshed.RefreshToken)) || bytes.Equal(hash, []byte(refreshed.RefreshToken)) {
			t.Fatal("unsafe refresh persistence")
		}
		tokenFile := filepath.Join(dir, "synthetic-token.txt")
		if e := os.WriteFile(tokenFile, []byte(refreshed.Token), 0600); e != nil {
			t.Fatal(e)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestAuthorityProcessHelper$", "-test.v")
		cmd.Env = append(os.Environ(), "AUTHORITY_PROCESS_HELPER=1", "AUTHORITY_TEST_KEY_DIR="+dir, "AUTHORITY_TEST_ACCOUNT="+a, "AUTHORITY_TEST_TIME="+strconv.FormatInt(now.Load(), 10))
		out, e := cmd.CombinedOutput()
		if e != nil || !strings.Contains(string(out), "synthetic authority verified after process restart") {
			t.Fatalf("process restart: %v %s", e, out)
		}
		now.Add(60)
		if _, e := s.Refresh(ctx, p.RefreshToken, 0, ""); !errors.Is(e, authority.ErrRefresh) {
			t.Fatal(e)
		}
	})
	t.Run("transactions_code_and_global_invalidation", func(t *testing.T) {
		sentinel := errors.New("deliberate rollback")
		e := repo.WithinAccount(ctx, a, func(tx authority.Transaction) error {
			row, e := tx.Session(pa.Session.ID)
			if e != nil {
				return e
			}
			row.Epoch += 100
			if e = tx.SaveSession(row); e != nil {
				return e
			}
			return sentinel
		})
		if !errors.Is(e, sentinel) {
			t.Fatal(e)
		}
		if _, e = s.Authenticate(ctx, refreshed.Token); e != nil {
			t.Fatal("rollback did not retain current token", e)
		}
		code, e := s.CreateLoginCode(ctx, a, "synthetic code", []string{"posts.*"})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.ExchangeCode(ctx, code); e != nil {
			t.Fatal(e)
		}
		if _, e = s.ExchangeCode(ctx, code); !errors.Is(e, authority.ErrInvalid) {
			t.Fatal(e)
		}
		if e = s.InvalidateAccount(ctx, a); e != nil {
			t.Fatal(e)
		}
		if _, e = s.Authenticate(ctx, refreshed.Token); !errors.Is(e, authority.ErrStale) {
			t.Fatal(e)
		}
		if _, e = s.Refresh(ctx, refreshed.RefreshToken, 0, ""); !errors.Is(e, authority.ErrStale) {
			t.Fatal(e)
		}
		if _, e = s.Authenticate(ctx, other.Token); e != nil {
			t.Fatal("global invalidation crossed account", e)
		}
	})
	t.Run("public_key_verifier_read_only_snapshot", func(t *testing.T) {
		p := pgIssue(t, s, b)
		config := authorityConfig(t, dir, now)
		verifier, e := authority.NewVerifier(repo, authority.Config{Issuer: config.Issuer, Audience: config.Audience, TrustedKeys: config.TrustedKeys, Now: config.Now})
		if e != nil {
			t.Fatal(e)
		}
		principal, e := verifier.Authenticate(ctx, p.Token)
		if e != nil {
			t.Fatal(e)
		}
		if e = repo.ReplacePolicy(ctx, b, []authority.Rule{{Pattern: "posts.create"}}, nil); e != nil {
			t.Fatal(e)
		}
		if _, e = verifier.AuthorizePrincipal(ctx, principal, "posts.create"); e != nil {
			t.Fatal(e)
		}
		if e = repo.ReadAccount(ctx, b, func(tx authority.Transaction) error { return tx.SaveAccount(tx.Account()) }); !errors.Is(e, authority.ErrForbidden) {
			t.Fatal("read-only transaction accepted write", e)
		}
		if e = repo.ReplacePolicy(ctx, b, nil, nil); e != nil {
			t.Fatal(e)
		}
		if _, e = verifier.AuthorizePrincipal(ctx, principal, "posts.create"); !errors.Is(e, authority.ErrForbidden) {
			t.Fatal("verifier cached stale policy", e)
		}

		group := authorityUUID(t)
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, b); e != nil {
			t.Fatal(e)
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_authority_groups(id,name) VALUES($1,$2)`, group, "synthetic_"+group); e != nil {
			t.Fatal(e)
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_authority_group_members(account_id,group_id) VALUES($1,$2)`, b, group); e != nil {
			t.Fatal(e)
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_authority_group_grants(group_id,pattern) VALUES($1,'posts.*')`, group); e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
		if _, e = verifier.AuthorizePrincipal(ctx, principal, "posts.create"); e != nil {
			t.Fatal("group grant not applied", e)
		}
		if e = repo.ReplacePolicy(ctx, b, []authority.Rule{{Pattern: "posts.create", Deny: true}}, nil); e != nil {
			t.Fatal(e)
		}
		if _, e = verifier.AuthorizePrincipal(ctx, principal, "posts.create"); !errors.Is(e, authority.ErrForbidden) {
			t.Fatal("direct deny did not override group", e)
		}
	})
	t.Run("refresh_racing_revoke", func(t *testing.T) {
		target := pgIssue(t, s, a)
		manager := pgIssue(t, s, a)
		p, e := s.Authenticate(ctx, target.Token)
		if e != nil {
			t.Fatal(e)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		var rotation authority.Pair
		var refreshErr, revokeErr error
		wg.Add(2)
		go func() { defer wg.Done(); <-start; rotation, refreshErr = s.Refresh(ctx, target.RefreshToken, 0, "") }()
		go func() { defer wg.Done(); <-start; revokeErr = s.Revoke(ctx, manager.Token, p.Session.ID) }()
		close(start)
		wg.Wait()
		if revokeErr != nil {
			t.Fatal(revokeErr)
		}
		if refreshErr != nil && !errors.Is(refreshErr, authority.ErrRevoked) && !errors.Is(refreshErr, authority.ErrRefresh) {
			t.Fatal(refreshErr)
		}
		if _, e = s.Authenticate(ctx, target.Token); !errors.Is(e, authority.ErrRevoked) {
			t.Fatal(e)
		}
		if rotation.Token != "" {
			if _, e = s.Authenticate(ctx, rotation.Token); !errors.Is(e, authority.ErrRevoked) {
				t.Fatal(e)
			}
		}
		rows, _, e := s.Sessions(ctx, manager.Token, 0, 200, true)
		if e != nil {
			t.Fatal(e)
		}
		for _, row := range rows {
			if row.ID == p.Session.ID {
				t.Fatal("revoked session still listed")
			}
		}
	})
}
func TestAuthorityProcessHelper(t *testing.T) {
	if os.Getenv("AUTHORITY_PROCESS_HELPER") != "1" {
		t.Skip("only invoked by disposable authority process test")
	}
	db := database(t)
	now := &atomic.Int64{}
	v, e := strconv.ParseInt(os.Getenv("AUTHORITY_TEST_TIME"), 10, 64)
	if e != nil {
		t.Fatal(e)
	}
	now.Store(v)
	dir := os.Getenv("AUTHORITY_TEST_KEY_DIR")
	s := newPGAuthority(t, db, dir, now)
	token, e := os.ReadFile(filepath.Join(dir, "synthetic-token.txt"))
	if e != nil {
		t.Fatal(e)
	}
	p, e := s.Authenticate(context.Background(), string(token))
	if e != nil || p.Account.ID != os.Getenv("AUTHORITY_TEST_ACCOUNT") {
		t.Fatal("new process authority validation failed", e)
	}
	t.Log("synthetic authority verified after process restart")
}

// The after phase is deliberately read-only and cannot repair missing state.
func TestPGAuthorityRestartPersistence(t *testing.T) {
	db := database(t)
	const account = "a0700000-0000-4000-8000-000000000001"
	const session = "a0700000-0000-4000-8000-000000000002"
	switch os.Getenv("PG_TEST_PHASE") {
	case "before":
		query(t, db, `INSERT INTO owned_authority_accounts(id,name,nick,version,disabled,created_at) VALUES($1,'authority_restart','Restart',7,true,'2026-01-01')`, account)
		query(t, db, `INSERT INTO owned_authority_sessions(id,account_id,label,type,client_id,created_at,last_granted_at,category,epoch,authorization_epoch,account_version,expires_at,revoked_at,scopes) VALUES($1,$2,'Restart',0,'','2026-01-01','2026-01-01','test',3,3,7,'2027-01-01','2026-02-01','["accounts.manage"]')`, session, account)
		query(t, db, `INSERT INTO owned_authority_grants(account_id,pattern,deny) VALUES($1,'accounts.*',true)`, account)
	case "after":
	default:
		t.Fatal("PG_TEST_PHASE must be before or after")
	}
	var version, epoch, grants int
	var disabled, revoked bool
	if err := db.QueryRow(`SELECT a.version,a.disabled,s.epoch,s.revoked_at IS NOT NULL,(SELECT count(*) FROM owned_authority_grants g WHERE g.account_id=a.id AND g.pattern='accounts.*' AND g.deny) FROM owned_authority_accounts a JOIN owned_authority_sessions s ON s.account_id=a.id WHERE a.id=$1 AND s.id=$2`, account, session).Scan(&version, &disabled, &epoch, &revoked, &grants); err != nil {
		t.Fatal(err)
	}
	if version != 7 || !disabled || epoch != 3 || !revoked || grants != 1 {
		t.Fatalf("authority state changed across database restart: %d %v %d %v %d", version, disabled, epoch, revoked, grants)
	}
}
