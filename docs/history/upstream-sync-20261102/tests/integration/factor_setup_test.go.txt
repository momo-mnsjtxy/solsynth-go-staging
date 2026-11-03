// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
	"strings"
	"sync"
	"testing"
	"time"
)

type factorSetupFixture struct {
	*signinFixture
	account authority.Account
	app     http.Handler
	pair    authority.Pair
}

func newFactorSetupFixture(t *testing.T) *factorSetupFixture {
	t.Helper()
	f := &factorSetupFixture{signinFixture: newSigninFixture(t)}
	f.account, _ = f.register(t, true)
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: authorityConfig(t, f.keys, f.now), SessionService: f.issuer, Signin: &f.config, PublicVisibility: &visibility, MessagerPolicy: &policy})
	if e != nil {
		t.Fatal(e)
	}
	f.app = app
	f.pair = f.credentialLogin(t, "", "")
	return f
}
func (f *factorSetupFixture) call(t *testing.T, method, path, token string, body any, present bool, status int) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if present {
		req = fixtureJSONRequest(t, method, path, token, body)
	} else {
		req = fixtureNoBodyRequest(method, path, token)
	}
	// Every fixture has a separate synthetic peer, without listening on a socket.
	req.RemoteAddr = "[2001:db8:" + strings.ReplaceAll(f.account.ID[:8], "-", "")[:4] + "::1]:1234"
	w := httptest.NewRecorder()
	f.app.ServeHTTP(w, req)
	if w.Code != status {
		t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("credential response cacheable", w.Header())
	}
	return w
}
func (f *factorSetupFixture) credentialLogin(t *testing.T, secondID, secondCode string) authority.Pair {
	t.Helper()
	c := backendDecode[signin.Challenge](t, f.call(t, "POST", "/stargate/auth/challenge", "", map[string]string{"account": f.account.Name}, true, 200))
	factors := backendDecode[[]signin.Factor](t, f.call(t, "GET", "/stargate/auth/challenge/"+c.ID+"/factors", "", nil, false, 200))
	var password string
	for _, v := range factors {
		if v.Type == 0 {
			password = v.ID
		}
	}
	path := "/stargate/auth/challenge/" + c.ID
	done := backendDecode[signin.Challenge](t, f.call(t, "PATCH", path, "", map[string]string{"factor_id": password, "password": syntheticPassword}, true, 200))
	if done.StepRemain != 0 {
		if secondID == "" {
			t.Fatal("missing required synthetic second factor")
		}
		f.call(t, "PATCH", path, "", map[string]string{"factor_id": secondID, "password": secondCode}, true, 200)
	}
	return backendDecode[authority.Pair](t, f.call(t, "POST", "/stargate/auth/token", "", map[string]string{"grant_type": "authorization_code", "code": c.ID}, true, 200))
}
func (f *factorSetupFixture) create(t *testing.T, typ int) signin.Factor {
	t.Helper()
	return backendDecode[signin.Factor](t, f.call(t, "POST", "/stargate/factors", f.pair.Token, map[string]any{"type": typ, "secret": nil}, true, 201))
}
func (f *factorSetupFixture) refresh(t *testing.T) {
	t.Helper()
	f.pair = backendDecode[authority.Pair](t, f.call(t, "POST", "/stargate/auth/token", "", map[string]string{"grant_type": "refresh_token", "refresh_token": f.pair.RefreshToken}, true, 200))
}
func TestPGFactorSetupAuthoredHTTPChainAndRefresh(t *testing.T) {
	_ = database(t)
	for _, rotate := range []string{"none", "after_recovery", "after_totp"} {
		t.Run(rotate, func(t *testing.T) {
			f := newFactorSetupFixture(t)
			initial := f.pair.Token
			recovery := f.create(t, 5)
			var authTime, deadline time.Time
			var session string
			if e := f.db.QueryRow(`SELECT session_id::text,auth_time,expires_at FROM owned_authority_factor_setups WHERE account_id=$1::uuid`, f.account.ID).Scan(&session, &authTime, &deadline); e != nil {
				t.Fatal(e)
			}
			if rotate == "after_recovery" {
				f.now.Add(30)
				f.refresh(t)
			}
			factor := f.create(t, 3)
			if rotate == "after_totp" {
				f.now.Add(30)
				f.refresh(t)
			}
			var sameSession string
			var sameDeadline time.Time
			if e := f.db.QueryRow(`SELECT session_id::text,expires_at FROM owned_authority_factor_setups WHERE account_id=$1::uuid`, f.account.ID).Scan(&sameSession, &sameDeadline); e != nil || sameSession != session || !sameDeadline.Equal(deadline) || !deadline.Equal(authTime.Add(5*time.Minute)) {
				t.Fatal("receipt renewed", e)
			}
			if rotate != "none" {
				f.call(t, "GET", "/stargate/factors", initial, nil, false, 401)
			}
			// Receipt cannot authorize recovery rotation/deletion, disable, or a PIN.
			f.call(t, "DELETE", "/stargate/factors/"+recovery.ID, f.pair.Token, nil, false, 403)
			f.call(t, "POST", "/stargate/factors/"+recovery.ID+"/disable", f.pair.Token, nil, false, 403)
			f.call(t, "POST", "/stargate/factors", f.pair.Token, map[string]any{"type": 4, "secret": syntheticPaymentPIN}, true, 403)
			path := "/stargate/factors/" + factor.ID + "/enable"
			f.call(t, "POST", path, f.pair.Token, nil, false, 400)
			f.call(t, "POST", path, f.pair.Token, "badcode", true, 401)
			code := syntheticTOTP(t, factor.CreatedResponse["uri"], f.now.Load())
			f.call(t, "POST", path, f.pair.Token, code, true, 200)
			f.call(t, "POST", path, f.pair.Token, code, true, 401)
			var state string
			if e := f.db.QueryRow(`SELECT state FROM owned_authority_factor_setups WHERE account_id=$1::uuid`, f.account.ID).Scan(&state); e != nil || state != "complete" {
				t.Fatal(state, e)
			}
			// Actual next sign-in must verify the newly activated factor.
			f.now.Add(30)
			f.pair = f.credentialLogin(t, factor.ID, syntheticTOTP(t, factor.CreatedResponse["uri"], f.now.Load()))
			listed := f.call(t, "GET", "/stargate/factors", f.pair.Token, nil, false, 200).Body.String()
			if strings.Contains(listed, "createdResponse") || strings.Contains(listed, "secret") || strings.Contains(listed, recovery.CreatedResponse["recovery_code"]) {
				t.Fatal("factor list disclosed enrollment material")
			}
		})
	}
}
func TestPGFactorSetupExpiryFreshActivationAndPendingLifetime(t *testing.T) {
	_ = database(t)
	for _, expiredPending := range []bool{false, true} {
		t.Run(map[bool]string{false: "receipt_only", true: "pending_too"}[expiredPending], func(t *testing.T) {
			f := newFactorSetupFixture(t)
			factor := f.create(t, 3)
			f.now.Add(301)
			path := "/stargate/factors/" + factor.ID + "/enable"
			code := syntheticTOTP(t, factor.CreatedResponse["uri"], f.now.Load())
			f.call(t, "POST", path, f.pair.Token, code, true, 403)
			if expiredPending {
				f.now.Add(600)
			}
			f.pair = f.credentialLogin(t, "", "")
			code = syntheticTOTP(t, factor.CreatedResponse["uri"], f.now.Load())
			status := 200
			if expiredPending {
				status = 401
			}
			f.call(t, "POST", path, f.pair.Token, code, true, status)
			if !expiredPending {
				f.call(t, "POST", path, f.pair.Token, code, true, 401)
			}
		})
	}
}
func TestPGFactorSetupSecurityInvalidationAndSessionIsolation(t *testing.T) {
	_ = database(t)
	for _, change := range []string{"revoke", "scope_restore", "policy_restore", "password", "other_factor", "other_session"} {
		t.Run(change, func(t *testing.T) {
			f := newFactorSetupFixture(t)
			if change == "policy_restore" {
				if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.account.ID, []authority.Rule{{Pattern: "posts.read"}}, nil); e != nil {
					t.Fatal(e)
				}
			}
			factor := f.create(t, 3)
			code := syntheticTOTP(t, factor.CreatedResponse["uri"], f.now.Load())
			switch change {
			case "revoke":
				if e := f.issuer.Revoke(f.ctx, f.pair.Token, "current"); e != nil {
					t.Fatal(e)
				}
			case "scope_restore":
				p, e := f.verifier.Authenticate(f.ctx, f.pair.Token)
				if e != nil {
					t.Fatal(e)
				}
				e = authoritypg.New(f.db).WithinAccount(f.ctx, f.account.ID, func(tx authority.Transaction) error {
					s, e := tx.Session(p.Session.ID)
					if e != nil {
						return e
					}
					old := s.Scopes
					s.Scopes = []string{"posts.read"}
					if e = tx.SaveSession(s); e != nil {
						return e
					}
					s, e = tx.Session(s.ID)
					if e != nil {
						return e
					}
					s.Scopes = old
					return tx.SaveSession(s)
				})
				if e != nil {
					t.Fatal(e)
				}
			case "policy_restore":
				store := authoritypg.New(f.db)
				if e := store.ReplacePolicy(f.ctx, f.account.ID, []authority.Rule{{Pattern: "posts.write"}}, nil); e != nil {
					t.Fatal(e)
				}
				if e := store.ReplacePolicy(f.ctx, f.account.ID, []authority.Rule{{Pattern: "posts.read"}}, nil); e != nil {
					t.Fatal(e)
				}
			case "password":
				if e := f.service.ChangePassword(f.ctx, f.pair.Token, syntheticPassword, syntheticPassword); e != nil {
					t.Fatal(e)
				}
			case "other_factor":
				f.pair = f.credentialLogin(t, "", "")
				f.create(t, 5)
			case "other_session":
				pair, e := f.issuer.IssueSession(f.ctx, f.account.ID, authority.SessionOptions{Scopes: []string{"*"}})
				if e != nil {
					t.Fatal(e)
				}
				f.call(t, "POST", "/stargate/factors/"+factor.ID+"/enable", pair.Token, code, true, 403)
				return
			}
			f.pair = f.credentialLogin(t, "", "")
			f.call(t, "POST", "/stargate/factors/"+factor.ID+"/enable", f.pair.Token, code, true, 401)
		})
	}
}
func TestPGFactorSetupConcurrentStartOTPAndRollback(t *testing.T) {
	f := newFactorSetupFixture(t)
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, e := f.service.CreateFactor(f.ctx, f.pair.Token, 5, "")
			results <- e
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		} else if !errors.Is(e, signin.ErrConflict) {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatal("concurrent starts", success)
	}
	factor := f.create(t, 3)
	uri := factor.CreatedResponse["uri"]
	// Forced commit failure must retain the unconsumed setup, factor and OTP counter.
	constraint := "factor_setup_rollback_" + strings.ReplaceAll(f.account.ID, "-", "")
	query(t, f.db, `ALTER TABLE owned_authority_accounts ADD CONSTRAINT `+constraint+` CHECK(id<>'`+f.account.ID+`'::uuid OR version=1) NOT VALID`)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`ALTER TABLE owned_authority_accounts DROP CONSTRAINT IF EXISTS ` + constraint)
	})
	if _, e := f.service.EnableFactor(f.ctx, f.pair.Token, factor.ID, syntheticTOTP(t, uri, f.now.Load())); e == nil {
		t.Fatal("injected mutation unexpectedly committed")
	}
	query(t, f.db, `ALTER TABLE owned_authority_accounts DROP CONSTRAINT `+constraint)
	start = make(chan struct{})
	results = make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, e := f.service.EnableFactor(f.ctx, f.pair.Token, factor.ID, syntheticTOTP(t, uri, f.now.Load()))
			results <- e
		}()
	}
	close(start)
	success = 0
	for range 2 {
		e := <-results
		if e == nil {
			success++
		} else if !errors.Is(e, authority.ErrStale) && !errors.Is(e, signin.ErrInvalid) {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatal("activation not one-use", success)
	}
}
func TestPGFactorSetupWrongOTPBudgetAndNoRefreshRenewal(t *testing.T) {
	f := newFactorSetupFixture(t)
	factor := f.create(t, 3)
	for range 19 {
		if _, e := f.service.EnableFactor(f.ctx, f.pair.Token, factor.ID, "badcode"); !errors.Is(e, signin.ErrInvalid) {
			t.Fatal(e)
		}
	}
	if _, e := f.service.EnableFactor(f.ctx, f.pair.Token, factor.ID, syntheticTOTP(t, factor.CreatedResponse["uri"], f.now.Load())); !errors.Is(e, signin.ErrRate) {
		t.Fatal("failed OTP budget rolled back", e)
	}
	f.now.Add(601)
	f.refresh(t)
	if _, e := f.service.EnableFactor(f.ctx, f.pair.Token, factor.ID, syntheticTOTP(t, factor.CreatedResponse["uri"], f.now.Load())); !errors.Is(e, signin.ErrReauthentication) {
		t.Fatal("refresh renewed expired receipt", e)
	}
}
func TestPGRecoveryBodylessRotationAndOneTimeDisclosure(t *testing.T) {
	_ = database(t)
	for _, body := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "json_null"}[body], func(t *testing.T) {
			f := newFactorSetupFixture(t)
			recovery := f.create(t, 5)
			old := recovery.CreatedResponse["recovery_code"]
			// Deactivation needs a separate actual completed credential login.
			f.pair = f.credentialLogin(t, "", "")
			f.call(t, "POST", "/stargate/factors/"+recovery.ID+"/disable", f.pair.Token, nil, false, 204)
			f.pair = f.credentialLogin(t, "", "")
			path := "/stargate/factors/" + recovery.ID + "/enable"
			f.call(t, "POST", path, f.pair.Token, "unexpected-code", true, 400)
			replacement := backendDecode[signin.Factor](t, f.call(t, "POST", path, f.pair.Token, nil, body, 200))
			fresh := replacement.CreatedResponse["recovery_code"]
			if fresh == "" || fresh == old || replacement.ID != recovery.ID || replacement.EnabledAt == nil {
				t.Fatal("invalid rotation result")
			}
			f.call(t, "POST", path, f.pair.Token, nil, body, 401)
			f.pair = f.credentialLogin(t, "", "")
			factor := f.create(t, 3)
			uri := factor.CreatedResponse["uri"]
			f.call(t, "POST", "/stargate/factors/"+factor.ID+"/enable", f.pair.Token, syntheticTOTP(t, uri, f.now.Load()), true, 200)
			c, p := f.challenge(t, f.account.Name)
			if _, e := f.service.VerifyFactor(f.ctx, c.ID, p.ID, syntheticPassword, f.account.Name); e != nil {
				t.Fatal(e)
			}
			if _, e := f.service.VerifyFactor(f.ctx, c.ID, recovery.ID, old, f.account.Name); !errors.Is(e, signin.ErrInvalid) {
				t.Fatal("old recovery code survived", e)
			}
			if _, e := f.service.VerifyFactor(f.ctx, c.ID, recovery.ID, fresh, f.account.Name); e != nil {
				t.Fatal(e)
			}
			if _, e := f.issuer.ExchangeCode(f.ctx, c.ID); e != nil {
				t.Fatal(e)
			}
			c, p = f.challenge(t, f.account.Name)
			if _, e := f.service.VerifyFactor(f.ctx, c.ID, p.ID, syntheticPassword, f.account.Name); e != nil {
				t.Fatal(e)
			}
			if _, e := f.service.VerifyFactor(f.ctx, c.ID, recovery.ID, fresh, f.account.Name); !errors.Is(e, signin.ErrInvalid) {
				t.Fatal("recovery replay", e)
			}
		})
	}
}
func TestPGFactorSetupRestartPersistence(t *testing.T) {
	db := database(t)
	file := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "factor-setup-record.json")
	type saved struct {
		Account, Session, Factor string
		Expires                  time.Time
	}
	var v saved
	if os.Getenv("PG_TEST_PHASE") == "before" {
		f := newFactorSetupFixture(t)
		factor := f.create(t, 3)
		v.Account = f.account.ID
		v.Factor = factor.ID
		if e := db.QueryRow(`SELECT session_id::text,expires_at FROM owned_authority_factor_setups WHERE account_id=$1::uuid`, v.Account).Scan(&v.Session, &v.Expires); e != nil {
			t.Fatal(e)
		}
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(file, b, 0600); e != nil {
			t.Fatal(e)
		}
		return
	}
	if os.Getenv("PG_TEST_PHASE") != "after" {
		t.Fatal("explicit restart phase required")
	}
	b, e := os.ReadFile(file)
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &v); e != nil {
		t.Fatal(e)
	}
	var n int
	e = db.QueryRowContext(context.Background(), `SELECT count(*)FROM owned_authority_factor_setups s JOIN owned_authority_pending_enrollments p ON p.factor_id=s.totp_id JOIN owned_authority_credential_assurances a ON a.session_id=s.session_id WHERE s.account_id=$1::uuid AND s.session_id=$2::uuid AND s.totp_id=$3::uuid AND s.expires_at=$4::timestamptz AND s.state='pending' AND p.valid AND a.consumed_at IS NOT NULL AND a.target_id=p.factor_id`, v.Account, v.Session, v.Factor, v.Expires).Scan(&n)
	if e != nil || n != 1 {
		t.Fatal("durable enrollment missing", n, e)
	}
}

func TestPGRecoveryRotationRollbackAndConcurrentUse(t *testing.T) {
	f := newFactorSetupFixture(t)
	recovery := f.create(t, 5)
	f.pair = f.credentialLogin(t, "", "")
	if e := f.service.DisableFactor(f.ctx, f.pair.Token, recovery.ID); e != nil {
		t.Fatal(e)
	}
	f.pair = f.credentialLogin(t, "", "")
	var original []byte
	var version int64
	if e := f.db.QueryRow(`SELECT f.secret,a.version FROM owned_authority_factors f JOIN owned_authority_accounts a ON a.id=f.account_id WHERE f.id=$1::uuid`, recovery.ID).Scan(&original, &version); e != nil {
		t.Fatal(e)
	}
	constraint := "recovery_rotation_rollback_" + strings.ReplaceAll(f.account.ID, "-", "")
	query(t, f.db, `ALTER TABLE owned_authority_accounts ADD CONSTRAINT `+constraint+` CHECK(id<>'`+f.account.ID+`'::uuid OR version=`+fmt.Sprint(version)+`) NOT VALID`)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`ALTER TABLE owned_authority_accounts DROP CONSTRAINT IF EXISTS ` + constraint)
	})
	result, e := f.service.EnableFactor(f.ctx, f.pair.Token, recovery.ID, "")
	if e == nil || result.CreatedResponse != nil {
		t.Fatal("failed rotation disclosed code", e)
	}
	var hash []byte
	var enabled *time.Time
	if e = f.db.QueryRow(`SELECT secret,enabled_at FROM owned_authority_factors WHERE id=$1::uuid`, recovery.ID).Scan(&hash, &enabled); e != nil || !bytes.Equal(hash, original) || enabled != nil {
		t.Fatal("rotation rollback lost old state", e)
	}
	query(t, f.db, `ALTER TABLE owned_authority_accounts DROP CONSTRAINT `+constraint)
	type outcome struct {
		factor signin.Factor
		err    error
	}
	done := make(chan outcome, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			v, e := f.service.EnableFactor(f.ctx, f.pair.Token, recovery.ID, "")
			done <- outcome{v, e}
		}()
	}
	close(start)
	success := 0
	for range 2 {
		v := <-done
		if v.err == nil {
			success++
			if v.factor.CreatedResponse["recovery_code"] == "" {
				t.Fatal("missing committed disclosure")
			}
		} else if v.factor.CreatedResponse != nil {
			t.Fatal("failed concurrent rotation disclosed code")
		}
	}
	if success != 1 {
		t.Fatal("rotation not one-use", success)
	}
}
func TestPGTOTPDisableAndFreshReenable(t *testing.T) {
	f := newFactorSetupFixture(t)
	factor := f.create(t, 3)
	uri := factor.CreatedResponse["uri"]
	if _, e := f.service.EnableFactor(f.ctx, f.pair.Token, factor.ID, syntheticTOTP(t, uri, f.now.Load())); e != nil {
		t.Fatal(e)
	}
	f.now.Add(30)
	f.pair = f.credentialLogin(t, factor.ID, syntheticTOTP(t, uri, f.now.Load()))
	if e := f.service.DisableFactor(f.ctx, f.pair.Token, factor.ID); e != nil {
		t.Fatal(e)
	}
	f.pair = f.credentialLogin(t, "", "")
	f.now.Add(30)
	if _, e := f.service.EnableFactor(f.ctx, f.pair.Token, factor.ID, syntheticTOTP(t, uri, f.now.Load())); e != nil {
		t.Fatal("explicit disable prevented fresh OTP-proven reenable", e)
	}
}
