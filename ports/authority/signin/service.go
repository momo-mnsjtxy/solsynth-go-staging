// SPDX-License-Identifier: AGPL-3.0-only
package signin

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"solsynth.local/go-migration/ports/authority"
)

var accountName = regexp.MustCompile(`^[a-z][a-z0-9_]{2,39}$`)

func New(store Store, issuer *authority.Service, verifier authority.Authenticator, cfg Config) (*Service, error) {
	if store == nil || issuer == nil || verifier == nil || len(cfg.SealKey) != 32 || cfg.Issuer == "" || len(cfg.Issuer) > 100 {
		return nil, ErrInput
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if len(cfg.InitialMemberPermissions) > 100 {
		return nil, ErrInput
	}
	seen := map[string]bool{}
	for _, key := range cfg.InitialMemberPermissions {
		if !authority.ValidPermissionPattern(key) || strings.Contains(key, "*") || seen[key] {
			return nil, ErrInput
		}
		seen[key] = true
	}
	cfg.InitialMemberPermissions = append([]string(nil), cfg.InitialMemberPermissions...)
	cfg.SealKey = append([]byte(nil), cfg.SealKey...)
	dummy, e := hashPassword("unusable synthetic dummy password")
	if e != nil {
		return nil, e
	}
	return &Service{store: store, authority: issuer, verifier: verifier, cfg: cfg, dummy: dummy}, nil
}
func known(e error) error {
	if e == nil {
		return nil
	}
	var f *authority.Failure
	if errors.As(e, &f) {
		return f
	}
	if errors.Is(e, authority.ErrNotFound) {
		return ErrInvalid
	}
	return authority.ErrUnavailable
}
func (s *Service) now() time.Time { return s.cfg.Now().UTC() }
func (s *Service) limit(ctx context.Context, lane, key string, n int, d time.Duration) error {
	if key == "" || len(key) > 512 {
		return ErrInput
	}
	return known(s.store.Attempt(ctx, digest(lane+":"+key), n, d, s.now()))
}
func (s *Service) captcha(ctx context.Context, token, peer string) error {
	if s.cfg.Captcha == nil {
		return ErrCaptcha
	}
	if len(token) == 0 || len(token) > 4096 {
		return ErrInput
	}
	if e := s.cfg.Captcha.Verify(ctx, token, peer); e != nil {
		if errors.Is(e, ErrInvalid) {
			return ErrInvalid
		}
		return ErrCaptcha
	}
	return nil
}
func (s *Service) active(tx Transaction, login bool) error {
	a := tx.Authority().Account()
	if a.Disabled {
		return authority.ErrDisabled
	}
	ps, e := tx.Authority().Punishments()
	if e != nil {
		return e
	}
	n := s.now()
	for _, p := range ps {
		if (p.StartsAt == nil || !n.Before(*p.StartsAt)) && (p.ExpiresAt == nil || n.Before(*p.ExpiresAt)) && (p.Kind == "disable_account" || login && p.Kind == "block_login") {
			return authority.ErrDisabled
		}
	}
	return nil
}
func (s *Service) makeChallenge(account string, version int64, purpose, label string, platform int, d time.Duration) (Challenge, error) {
	raw, e := newSecret()
	if e != nil {
		return Challenge{}, e
	}
	now := s.now()
	return Challenge{ID: raw, Hash: digest(raw), AccountID: account, Version: version, Purpose: purpose, CreatedAt: now, UpdatedAt: now, ExpiredAt: now.Add(d), DeviceName: label, Platform: platform, BlacklistFactors: []string{}}, nil
}
func cleanFactor(f Factor) Factor { f.Secret = nil; f.CreatedResponse = nil; return f }
func state(c Challenge, raw string) Challenge {
	c.ID = raw
	c.StepRemain = 0
	c.StepTotal = 1
	if c.RequireSecond {
		c.StepTotal++
	}
	c.Audiences = []string{}
	c.Scopes = []string{"*"}
	if !c.PasswordOK {
		c.StepRemain++
	}
	if c.RequireSecond && !c.SecondOK {
		c.StepRemain++
	}
	if c.BlacklistFactors == nil {
		c.BlacklistFactors = []string{}
	}
	return c
}
func (s *Service) Register(ctx context.Context, r RegisterRequest, peer string) (authority.Account, error) {
	var a authority.Account
	if !accountName.MatchString(r.Name) || len(r.Nick) > 100 || len(r.Language) > 32 || !validPassword(r.Password) || len(r.AffiliationSpell) > 4096 {
		return a, ErrInput
	}
	email, e := NormalizeEmail(r.Email)
	if e != nil {
		return a, ErrInput
	}
	if e = s.limit(ctx, "register-peer", peer, 5, time.Hour); e != nil {
		return a, e
	}
	if s.cfg.Delivery == nil {
		return a, ErrDelivery
	}
	if e = s.captcha(ctx, r.CaptchaToken, peer); e != nil {
		return a, e
	}
	hash, e := hashPassword(r.Password)
	if e != nil {
		return a, e
	}
	id, e := newID()
	if e != nil {
		return a, e
	}
	fid, e := newID()
	if e != nil {
		return a, e
	}
	now := s.now()
	a = authority.Account{ID: id, Name: r.Name, Nick: r.Nick, Language: r.Language, Version: 1, CreatedAt: now}
	if a.Nick == "" {
		a.Nick = a.Name
	}
	c, e := s.makeChallenge(id, 1, "verify_email", "", 0, 30*time.Minute)
	if e != nil {
		return a, e
	}
	f := Factor{ID: fid, Type: 0, Trustworthy: 1, CreatedAt: now, EnabledAt: &now, Secret: hash, LastCounter: -1}
	if r.AffiliationSpell != "" {
		admission, ok := s.store.(AffiliationRegistrationStore)
		if !ok {
			return authority.Account{}, ErrInput
		}
		e = admission.RegisterWithAffiliation(ctx, a, Credentials{AccountID: id, Email: email}, f, c, s.cfg.InitialMemberPermissions, strings.TrimSpace(r.AffiliationSpell))
	} else {
		e = s.store.Register(ctx, a, Credentials{AccountID: id, Email: email}, f, c, s.cfg.InitialMemberPermissions)
	}
	if e != nil {
		return authority.Account{}, known(e)
	}
	if e = s.cfg.Delivery.Send(ctx, email, "verify_email", c.ID); e != nil {
		return authority.Account{}, ErrDelivery
	}
	return a, nil
}
func (s *Service) VerifyEmail(ctx context.Context, raw string) error {
	return s.consume(ctx, raw, "verify_email", func(tx Transaction, c Challenge) error {
		if e := s.active(tx, false); e != nil {
			return e
		}
		if c.Purpose == "verify_contact" {
			contacts, ok := tx.(ContactTransaction)
			if !ok || !authority.ValidID(c.ContactID) {
				return ErrInvalid
			}
			if e := contacts.VerifyContact(c.ContactID, s.now()); e != nil {
				return e
			}
		} else {
			v, e := tx.Credentials()
			if e != nil {
				return e
			}
			n := s.now()
			v.VerifiedAt = &n
			if e = tx.SaveCredentials(v); e != nil {
				return e
			}
		}
		if completion, ok := tx.(ContactVerificationCompletion); ok {
			return completion.CompleteContactVerification(c)
		}
		return nil
	})
}
func (s *Service) deliver(ctx context.Context, account, captcha, peer, purpose string) error {
	if len(account) < 1 || len(account) > 254 {
		return ErrInput
	}
	if e := s.limit(ctx, purpose+"-peer", peer, 10, time.Hour); e != nil {
		return e
	}
	if e := s.limit(ctx, purpose+"-account", strings.ToLower(account), 3, time.Hour); e != nil {
		return e
	}
	if s.cfg.Delivery == nil {
		return ErrDelivery
	}
	if e := s.captcha(ctx, captcha, peer); e != nil {
		return e
	}
	id, e := s.store.Lookup(ctx, account)
	if errors.Is(e, authority.ErrNotFound) {
		return nil
	}
	if e != nil {
		return known(e)
	}
	var email, raw string
	e = s.store.Within(ctx, id, func(tx Transaction) error {
		if e := s.active(tx, false); e != nil {
			return e
		}
		v, e := tx.Credentials()
		if e != nil {
			return e
		}
		if purpose == "reset_password" && v.VerifiedAt == nil {
			return ErrUnverified
		}
		if purpose == "verify_email" && v.VerifiedAt != nil {
			return nil
		}
		c, e := s.makeChallenge(id, tx.Authority().Account().Version, purpose, "", 0, 30*time.Minute)
		if e != nil {
			return e
		}
		if e = tx.CancelChallenges(purpose); e != nil {
			return e
		}
		if e = tx.SaveChallenge(c); e != nil {
			return e
		}
		email, raw = v.Email, c.ID
		return nil
	})
	if errors.Is(e, authority.ErrDisabled) || errors.Is(e, ErrUnverified) {
		return nil
	}
	if e != nil {
		return known(e)
	}
	if raw == "" {
		return nil
	}
	if s.cfg.Delivery.Send(ctx, email, purpose, raw) != nil {
		return ErrDelivery
	}
	return nil
}
func (s *Service) RequestReset(ctx context.Context, account, captcha, peer string) error {
	return s.deliver(ctx, account, captcha, peer, "reset_password")
}
func (s *Service) ResendVerification(ctx context.Context, account, captcha, peer string) error {
	return s.deliver(ctx, account, captcha, peer, "verify_email")
}
func (s *Service) consume(ctx context.Context, raw, purpose string, fn func(Transaction, Challenge) error) error {
	if !validToken(raw) {
		return ErrInvalid
	}
	id, e := s.store.FindChallenge(ctx, digest(raw))
	if e != nil {
		return known(e)
	}
	return known(s.store.Within(ctx, id, func(tx Transaction) error {
		c, e := tx.Challenge(digest(raw))
		if e != nil {
			return e
		}
		if (c.Purpose != purpose && !(purpose == "verify_email" && c.Purpose == "verify_contact")) || c.Completed || c.Version != tx.Authority().Account().Version || !s.now().Before(c.ExpiredAt) {
			return ErrInvalid
		}
		if e = fn(tx, c); e != nil {
			return e
		}
		if !s.now().Before(c.ExpiredAt) {
			return ErrInvalid
		}
		if e = s.active(tx, false); e != nil {
			return e
		}
		c.Completed = true
		c.UpdatedAt = s.now()
		return tx.SaveChallenge(c)
	}))
}
func (s *Service) ResetPassword(ctx context.Context, raw, password string) error {
	if !validPassword(password) || !validToken(raw) {
		return ErrInput
	}
	if e := s.limit(ctx, "reset-token", raw, 5, time.Hour); e != nil {
		return e
	}
	return s.consume(ctx, raw, "reset_password", func(tx Transaction, c Challenge) error {
		if e := s.active(tx, false); e != nil {
			return e
		}
		hash, e := hashPassword(password)
		if e != nil {
			return e
		}
		return s.replacePassword(tx, hash)
	})
}
func (s *Service) replacePassword(tx Transaction, hash []byte) error {
	fs, e := tx.Factors()
	if e != nil {
		return e
	}
	for _, f := range fs {
		if f.Type == 0 {
			f.Secret = hash
			if e = tx.SaveFactor(f); e != nil {
				return e
			}
			return s.invalidate(tx)
		}
	}
	return ErrInvalid
}
func (s *Service) invalidate(tx Transaction) error {
	if setup, ok := tx.(FactorSetupTransaction); ok {
		if e := setup.CancelFactorSetup(); e != nil {
			return e
		}
	}
	a := tx.Authority().Account()
	a.Version++
	if e := tx.Authority().SaveAccount(a); e != nil {
		return e
	}
	return tx.CancelChallenges("")
}
func (s *Service) Start(ctx context.Context, r StartRequest, peer string) (Challenge, error) {
	var out Challenge
	if r.Account == "" || len(r.Account) > 256 || len(r.DeviceID) > 200 || len(r.DeviceName) > 200 || len(r.UserAgent) > 512 || r.Platform < 0 || r.Platform > 6 {
		return out, ErrInput
	}
	if e := s.limit(ctx, "login-peer", peer, 60, 10*time.Minute); e != nil {
		return out, e
	}
	if e := s.limit(ctx, "login-account", strings.ToLower(r.Account), 20, 10*time.Minute); e != nil {
		return out, e
	}
	id, e := s.store.Lookup(ctx, r.Account)
	if e != nil {
		return out, known(e)
	}
	e = s.store.Within(ctx, id, func(tx Transaction) error {
		if e := s.active(tx, true); e != nil {
			return e
		}
		v, e := tx.Credentials()
		if e != nil {
			return e
		}
		if v.VerifiedAt == nil {
			return ErrUnverified
		}
		fs, e := tx.Factors()
		if e != nil {
			return e
		}
		out, e = s.makeChallenge(id, tx.Authority().Account().Version, "login", r.DeviceName, r.Platform, 5*time.Minute)
		if e != nil {
			return e
		}
		for _, f := range fs {
			if f.Type == 3 && f.EnabledAt != nil {
				out.RequireSecond = true
			}
		}
		out.DeviceID = r.DeviceID
		out.IPAddress = peer
		out.UserAgent = r.UserAgent
		out = state(out, out.ID)
		return tx.SaveChallenge(out)
	})
	return out, known(e)
}
func (s *Service) challenge(ctx context.Context, raw string, fn func(Transaction, Challenge) error) error {
	if !validToken(raw) {
		return ErrInvalid
	}
	id, e := s.store.FindChallenge(ctx, digest(raw))
	if e != nil {
		return known(e)
	}
	return known(s.store.Within(ctx, id, func(tx Transaction) error {
		if e := s.active(tx, true); e != nil {
			return e
		}
		c, e := tx.Challenge(digest(raw))
		if e != nil {
			return e
		}
		if c.Purpose != "login" || c.Version != tx.Authority().Account().Version || !s.now().Before(c.ExpiredAt) {
			return ErrInvalid
		}
		return fn(tx, c)
	}))
}
func (s *Service) Challenge(ctx context.Context, raw string) (Challenge, error) {
	var out Challenge
	e := s.challenge(ctx, raw, func(tx Transaction, c Challenge) error { out = state(c, raw); return nil })
	return out, e
}
func (s *Service) ChallengeFactors(ctx context.Context, raw string) ([]Factor, error) {
	out := []Factor{}
	e := s.challenge(ctx, raw, func(tx Transaction, c Challenge) error {
		fs, e := tx.Factors()
		if e != nil {
			return e
		}
		for _, f := range fs {
			if f.EnabledAt != nil && (f.Type == 0 || c.RequireSecond && (f.Type == 3 || f.Type == 5)) {
				out = append(out, cleanFactor(f))
			}
		}
		return nil
	})
	return out, e
}
func (s *Service) PrepareFactor(ctx context.Context, raw, fid string) error {
	return s.challenge(ctx, raw, func(tx Transaction, c Challenge) error {
		if c.Completed {
			return ErrInvalid
		}
		fs, e := tx.Factors()
		if e != nil {
			return e
		}
		for _, f := range fs {
			if f.ID == fid && f.EnabledAt != nil && (f.Type == 0 || f.Type == 3 || f.Type == 5) {
				return nil
			}
		}
		return ErrInvalid
	})
}
func (s *Service) VerifyFactor(ctx context.Context, raw, fid, password, peer string) (Challenge, error) {
	var out Challenge
	if !validToken(raw) || !authority.ValidID(fid) || len(password) < 1 || len(password) > 1024 {
		return out, ErrInvalid
	}
	if e := s.limit(ctx, "verify-peer", peer, 60, 10*time.Minute); e != nil {
		return out, e
	}
	if e := s.limit(ctx, "verify-challenge", raw, 8, 10*time.Minute); e != nil {
		return out, e
	}
	id, e := s.store.FindChallenge(ctx, digest(raw))
	if e != nil {
		return out, known(e)
	}
	if e = s.limit(ctx, "verify-account", id, 20, 10*time.Minute); e != nil {
		return out, e
	}
	e = s.challenge(ctx, raw, func(tx Transaction, c Challenge) error {
		if c.Completed {
			return ErrInvalid
		}
		fs, e := tx.Factors()
		if e != nil {
			return e
		}
		var selected *Factor
		for i := range fs {
			if fs[i].ID == fid && fs[i].EnabledAt != nil {
				selected = &fs[i]
				break
			}
		}
		if selected == nil {
			checkPassword(s.dummy, password)
			return ErrInvalid
		}
		f := *selected
		switch f.Type {
		case 0:
			if c.PasswordOK || !checkPassword(f.Secret, password) {
				return ErrInvalid
			}
			c.PasswordOK = true
			c.AuthMethods = append(c.AuthMethods, "pwd")
		case 3:
			if c.SecondOK || !c.RequireSecond {
				return ErrInvalid
			}
			secret, e := s.open(f.Secret, id, f.ID)
			if e != nil {
				return e
			}
			counter, ok := checkTOTP(secret, password, s.now(), f.LastCounter)
			if !ok {
				return ErrInvalid
			}
			f.LastCounter = counter
			if e = tx.SaveFactor(f); e != nil {
				return e
			}
			c.SecondOK = true
			c.AuthMethods = append(c.AuthMethods, "otp")
		case 5:
			if c.SecondOK || !c.RequireSecond || !validToken(password) || subtle.ConstantTimeCompare(f.Secret, digest(password)) != 1 {
				return ErrInvalid
			}
			if e = tx.DeleteFactor(f.ID); e != nil {
				return e
			}
			c.SecondOK = true
			c.AuthMethods = append(c.AuthMethods, "recovery")
		default:
			return ErrUnsupported
		}
		c.UpdatedAt = s.now()
		c.BlacklistFactors = append(c.BlacklistFactors, f.ID)
		out = state(c, raw)
		if out.StepRemain == 0 {
			if authority.ValidCredentialMethods(c.AuthMethods) {
				e = s.authority.CreateVerifiedLoginCodeTransaction(ctx, tx.Authority(), raw, c.DeviceName, []string{"*"}, c.AuthMethods, s.now())
			} else {
				// A pre-upgrade partially verified challenge may lack method
				// metadata. Preserve its normal login without inventing assurance.
				e = s.authority.CreateLoginCodeTransaction(ctx, tx.Authority(), raw, c.DeviceName, []string{"*"})
			}
			if e != nil {
				return e
			}
			c.Completed = true
			out.Completed = true
		}
		return tx.SaveChallenge(c)
	})

	if errors.Is(e, ErrInvalid) {
		recordErr := s.store.Within(ctx, id, func(tx Transaction) error {
			c, err := tx.Challenge(digest(raw))
			if err != nil {
				return err
			}
			if c.Completed || c.Purpose != "login" || c.Version != tx.Authority().Account().Version || !s.now().Before(c.ExpiredAt) {
				return nil
			}
			c.FailedAttempts++
			c.UpdatedAt = s.now()
			return tx.SaveChallenge(c)
		})
		if recordErr != nil {
			return Challenge{}, known(recordErr)
		}
	}
	return out, known(e)
}
func (s *Service) authenticated(ctx context.Context, token string, recent bool, fn func(Transaction, authority.Principal) error) error {
	p, e := s.verifier.Authenticate(ctx, token)
	if e != nil {
		return e
	}
	if p.Session.Type != 0 {
		return authority.ErrForbidden
	}
	if recent {
		if e = s.limit(ctx, "factor-mutation", p.Account.ID, 20, 10*time.Minute); e != nil {
			return e
		}
	}
	ctx = s.store.Bind(ctx, p)
	var committed error
	err := s.store.Within(ctx, p.Account.ID, func(tx Transaction) error {
		fresh, e := tx.Authenticate()
		if e != nil {
			return e
		}
		if fresh.Session.Type != 0 {
			return authority.ErrForbidden
		}
		if recent && (s.now().Sub(fresh.Session.CreatedAt) > 10*time.Minute || s.now().Before(fresh.Session.CreatedAt)) {
			return ErrRecent
		}
		err := fn(tx, fresh)
		var failure committedSetupFailure
		if errors.As(err, &failure) {
			committed = failure.err
			return nil
		}
		return err
	})
	if err == nil && committed != nil {
		return committed
	}
	return known(err)
}
func (s *Service) Factors(ctx context.Context, token string) ([]Factor, error) {
	out := []Factor{}
	e := s.authenticated(ctx, token, false, func(tx Transaction, p authority.Principal) error {
		fs, e := tx.Factors()
		if e != nil {
			return e
		}
		for _, f := range fs {
			out = append(out, cleanFactor(f))
		}
		return nil
	})
	return out, e
}
func (s *Service) ChangePassword(ctx context.Context, token, old, password string) error {
	if !validPassword(password) || len(old) > 1024 {
		return ErrInput
	}
	return s.authenticated(ctx, token, true, func(tx Transaction, p authority.Principal) error {
		fs, e := tx.Factors()
		if e != nil {
			return e
		}
		for _, f := range fs {
			if f.Type == 0 && checkPassword(f.Secret, old) {
				hash, e := hashPassword(password)
				if e != nil {
					return e
				}
				return s.replacePassword(tx, hash)
			}
		}
		return ErrInvalid
	})
}
func (s *Service) CreateFactor(ctx context.Context, token string, typ int, secret string) (Factor, error) {
	if typ == 4 {
		return s.mutatePaymentPIN(ctx, token, "create", "", secret)
	}
	var out Factor
	if typ != 3 && typ != 5 {
		return out, ErrUnsupported
	}
	if secret != "" {
		return out, ErrInput
	}
	e := s.sensitiveFactor(ctx, token, func(tx Transaction, p authority.Principal) error {
		fs, e := tx.Factors()
		if e != nil {
			return e
		}
		var recovery Factor
		hasTOTP := false
		for _, f := range fs {
			if f.Type == typ {
				return ErrConflict
			}
			if f.Type == 5 {
				recovery = f
			}
			if f.Type == 3 {
				hasTOTP = true
			}
		}
		setup, ok := tx.(FactorSetupTransaction)
		if !ok {
			return authority.ErrUnavailable
		}
		policy, e := s.factorPolicy(tx, fs)
		if e != nil {
			return e
		}
		receipt, re := setup.FactorSetup()
		if re != nil && !errors.Is(re, authority.ErrNotFound) {
			return re
		}
		continuation := typ == 3 && re == nil && receipt.State == "recovery" && s.setupValid(receipt, p, policy) && receipt.RecoveryID == recovery.ID && receipt.RecoveryRevision == recovery.Revision
		var assurance authority.CredentialAssurance
		if !continuation {
			assurance, e = s.freshFactorAssurance(tx, p)
			if e != nil {
				return e
			}
			if !(typ == 5 && hasTOTP) && re == nil && (receipt.State == "recovery" || receipt.State == "pending") && s.now().Before(receipt.ExpiresAt) && receipt.PolicyHash == policy {
				return ErrConflict
			}
		}
		id, e := newID()
		if e != nil {
			return e
		}
		now := s.now()
		out = Factor{ID: id, Type: typ, Trustworthy: 1, CreatedAt: now, LastCounter: -1}
		if typ == 3 {
			raw, e := random(20)
			if e != nil {
				return e
			}
			out.Secret, e = s.seal(raw, p.Account.ID, id)
			if e != nil {
				return e
			}
			end := now.Add(15 * time.Minute)
			out.PendingUntil = &end
			uri := url.URL{Scheme: "otpauth", Host: "totp", Path: "/" + s.cfg.Issuer + ":" + p.Account.Name}
			q := uri.Query()
			q.Set("secret", totpSecret(raw))
			q.Set("issuer", s.cfg.Issuer)
			q.Set("algorithm", "SHA1")
			q.Set("digits", "6")
			q.Set("period", "30")
			uri.RawQuery = q.Encode()
			out.CreatedResponse = map[string]string{"uri": uri.String()}
		} else {
			raw, e := newSecret()
			if e != nil {
				return e
			}
			out.Secret = digest(raw)
			out.EnabledAt = &now
			out.CreatedResponse = map[string]string{"recovery_code": raw}
		}
		// A recovery replacement while TOTP exists is a separate sensitive mutation.
		if typ == 5 && hasTOTP {
			if e = tx.SaveFactor(out); e != nil {
				return e
			}
			out.Revision = 1
			if e = s.consumeFactorAssurance(tx, p, assurance, "create", out); e != nil {
				return e
			}
			return s.invalidate(tx)
		}
		if e = setup.SaveSetupFactor(out); e != nil {
			return e
		}
		out.Revision = 1
		if !continuation {
			if e = s.consumeFactorAssurance(tx, p, assurance, "create", out); e != nil {
				return e
			}
			receipt = FactorSetup{SessionID: p.Session.ID, AccountVersion: p.Account.Version, AuthorizationEpoch: p.Session.AuthorizationEpoch, AuthTime: assurance.AuthTime, ExpiresAt: assurance.ExpiresAt, RecoveryID: recovery.ID, RecoveryRevision: recovery.Revision}
		} else {
			if _, e = tx.Authenticate(); e != nil {
				return e
			}
			if !s.now().Before(receipt.ExpiresAt) {
				return ErrReauthentication
			}
		}
		fs, e = tx.Factors()
		if e != nil {
			return e
		}
		receipt.PolicyHash, e = s.factorPolicy(tx, fs)
		if e != nil {
			return e
		}
		if typ == 5 {
			receipt.RecoveryID = out.ID
			receipt.RecoveryRevision = out.Revision
			receipt.State = "recovery"
		} else {
			receipt.TOTPID = out.ID
			receipt.TOTPRevision = out.Revision
			receipt.State = "pending"
			if e = setup.SavePendingEnrollment(PendingEnrollment{FactorID: out.ID, FactorRevision: out.Revision, AccountVersion: p.Account.Version, PolicyHash: receipt.PolicyHash, PendingUntil: *out.PendingUntil, Valid: true}); e != nil {
				return e
			}
		}
		return setup.SaveFactorSetup(receipt)
	})
	if e != nil {
		return Factor{}, e
	}
	out.Secret = nil
	return out, nil
}
func (s *Service) EnableFactor(ctx context.Context, token, fid, code string) (Factor, error) {
	if yes, e := s.isPaymentPIN(ctx, token, fid); e != nil {
		return Factor{}, e
	} else if yes {
		return s.mutatePaymentPIN(ctx, token, "enable", fid, code)
	}
	var out Factor
	e := s.sensitiveFactor(ctx, token, func(tx Transaction, p authority.Principal) error {
		fs, e := tx.Factors()
		if e != nil {
			return e
		}
		for _, f := range fs {
			if f.ID != fid {
				continue
			}
			if f.Type != 3 && f.Type != 5 {
				return ErrUnsupported
			}
			if f.EnabledAt != nil {
				return ErrInvalid
			}
			if f.Type == 5 {
				if code != "" {
					return ErrInput
				}
				a, e := s.freshFactorAssurance(tx, p)
				if e != nil {
					return e
				}
				raw, e := newSecret()
				if e != nil {
					return e
				}
				now := s.now()
				f.Secret = digest(raw)
				f.EnabledAt = &now
				f.PendingUntil = nil
				if e = tx.SaveFactor(f); e != nil {
					return e
				}
				if e = s.consumeFactorAssurance(tx, p, a, "enable", f); e != nil {
					return e
				}
				if e = s.invalidate(tx); e != nil {
					return e
				}
				out = cleanFactor(f)
				out.CreatedResponse = map[string]string{"recovery_code": raw}
				return nil
			}
			if f.PendingUntil == nil || !s.now().Before(*f.PendingUntil) {
				return ErrInvalid
			}
			setup, ok := tx.(FactorSetupTransaction)
			if !ok {
				return authority.ErrUnavailable
			}
			pending, e := setup.PendingEnrollment(fid)
			if errors.Is(e, authority.ErrNotFound) {
				return ErrInvalid
			}
			if e != nil {
				return e
			}
			policy, e := s.factorPolicy(tx, fs)
			if e != nil {
				return e
			}
			if !pending.Valid || pending.AccountVersion != p.Account.Version || pending.FactorRevision != f.Revision || pending.PolicyHash != policy || !pending.PendingUntil.Equal(*f.PendingUntil) {
				return rejectSetup(setup, ErrInvalid)
			}
			receipt, e := setup.FactorSetup()
			if e != nil {
				return e
			}
			// Revocation/security generation changes invalidate the enrollment itself;
			// mere receipt expiry may be recovered by a separate fresh login and OTP.
			origin, e := tx.Authority().Session(receipt.SessionID)
			// Disable deliberately advances the account version before rebinding its
			// pending metadata. Only that version mismatch is expected; subsequent
			// origin revocation or authorization changes must still invalidate it.
			if e != nil || origin.RevokedAt != nil || origin.AuthorizationEpoch != receipt.AuthorizationEpoch || receipt.State != "disabled" && origin.AccountVersion != pending.AccountVersion {
				return rejectSetup(setup, ErrInvalid)
			}
			continuation := receipt.State == "pending" && receipt.TOTPID == fid && receipt.TOTPRevision == f.Revision && s.setupValid(receipt, p, policy)
			var a authority.CredentialAssurance
			if !continuation {
				a, e = s.freshFactorAssurance(tx, p)
				if e != nil {
					return e
				}
			}
			raw, e := s.open(f.Secret, p.Account.ID, fid)
			if e != nil {
				return e
			}
			counter, ok := checkTOTP(raw, code, s.now(), f.LastCounter)
			if !ok {
				return ErrInvalid
			}
			now := s.now()
			f.EnabledAt = &now
			f.PendingUntil = nil
			f.LastCounter = counter
			if e = setup.SaveSetupFactor(f); e != nil {
				return e
			}
			if continuation {
				if _, e = tx.Authenticate(); e != nil {
					return e
				}
				if !s.now().Before(receipt.ExpiresAt) {
					return ErrReauthentication
				}
			} else {
				if e = s.consumeFactorAssurance(tx, p, a, "enable", f); e != nil {
					return e
				}
			}
			pending.Valid = false
			if e = setup.SavePendingEnrollment(pending); e != nil {
				return e
			}
			receipt.State = "complete"
			if e = setup.SaveFactorSetup(receipt); e != nil {
				return e
			}
			out = cleanFactor(f)
			return s.invalidate(tx)
		}
		return ErrInvalid
	})
	if e != nil {
		return Factor{}, e
	}
	return out, nil
}

// EnableFactorBody preserves the authored bodyless recovery-code request while
// keeping missing-body rejection for factors that require a verification code.
func (s *Service) EnableFactorBody(ctx context.Context, token, fid, code string, present bool) (Factor, error) {
	if !present {
		fs, e := s.Factors(ctx, token)
		if e != nil {
			return Factor{}, e
		}
		bodyless := false
		for _, f := range fs {
			if f.ID == fid && f.Type == 5 {
				bodyless = true
				break
			}
		}
		if !bodyless {
			return Factor{}, ErrInput
		}
	}
	return s.EnableFactor(ctx, token, fid, code)
}
func (s *Service) DeleteFactor(ctx context.Context, token, fid string) error {
	if yes, e := s.isPaymentPIN(ctx, token, fid); e != nil {
		return e
	} else if yes {
		_, e = s.mutatePaymentPIN(ctx, token, "delete", fid, "")
		return e
	}
	return s.sensitiveFactor(ctx, token, func(tx Transaction, p authority.Principal) error {
		fs, e := tx.Factors()
		if e != nil {
			return e
		}
		for _, f := range fs {
			if f.ID != fid {
				continue
			}
			if f.Type == 0 {
				return authority.ErrForbidden
			}
			a, e := s.freshFactorAssurance(tx, p)
			if e != nil {
				return e
			}
			if e = tx.DeleteFactor(fid); e != nil {
				return e
			}
			if e = s.consumeFactorAssurance(tx, p, a, "delete", f); e != nil {
				return e
			}
			return s.invalidate(tx)
		}
		return ErrInvalid
	})
}

func (s *Service) DisableFactor(ctx context.Context, token, fid string) error {
	if yes, e := s.isPaymentPIN(ctx, token, fid); e != nil {
		return e
	} else if yes {
		_, e = s.mutatePaymentPIN(ctx, token, "disable", fid, "")
		return e
	}
	return s.sensitiveFactor(ctx, token, func(tx Transaction, p authority.Principal) error {
		fs, e := tx.Factors()
		if e != nil {
			return e
		}
		for _, f := range fs {
			if f.ID != fid {
				continue
			}
			if f.Type != 3 && f.Type != 5 {
				return ErrUnsupported
			}
			if f.EnabledAt == nil {
				return ErrInvalid
			}
			a, e := s.freshFactorAssurance(tx, p)
			if e != nil {
				return e
			}
			f.EnabledAt = nil
			if f.Type == 3 {
				end := s.now().Add(15 * time.Minute)
				f.PendingUntil = &end
			} else {
				f.PendingUntil = nil
			}
			if e = tx.SaveFactor(f); e != nil {
				return e
			}
			if e = s.consumeFactorAssurance(tx, p, a, "disable", f); e != nil {
				return e
			}
			if e = s.invalidate(tx); e != nil {
				return e
			}
			if f.Type == 3 {
				setup, ok := tx.(FactorSetupTransaction)
				if !ok {
					return authority.ErrUnavailable
				}
				fs, e := tx.Factors()
				if e != nil {
					return e
				}
				policy, e := s.factorPolicy(tx, fs)
				if e != nil {
					return e
				}
				f.Revision++
				pending := PendingEnrollment{FactorID: f.ID, FactorRevision: f.Revision, AccountVersion: tx.Authority().Account().Version, PolicyHash: policy, PendingUntil: *f.PendingUntil, Valid: true}
				if e = setup.SavePendingEnrollment(pending); e != nil {
					return e
				}
				return setup.SaveFactorSetup(FactorSetup{SessionID: p.Session.ID, AccountVersion: pending.AccountVersion, AuthorizationEpoch: p.Session.AuthorizationEpoch, AuthTime: a.AuthTime, ExpiresAt: a.ExpiresAt, PolicyHash: policy, TOTPID: f.ID, TOTPRevision: f.Revision, State: "disabled"})
			}
			return nil
		}
		return ErrInvalid
	})
}
