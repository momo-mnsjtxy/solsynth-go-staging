// SPDX-License-Identifier: AGPL-3.0-only
package authority

import (
	"context"
	"crypto/hmac"
	"errors"
	"reflect"
	"strings"
	"time"
)

func known(err error) error {
	if err == nil {
		return nil
	}
	var f *Failure
	if errors.As(err, &f) {
		return f
	}
	if errors.Is(err, ErrNotFound) {
		return ErrInvalid
	}
	return ErrUnavailable
}
func active(start, end *time.Time, now time.Time) bool {
	return (start == nil || !now.Before(*start)) && (end == nil || now.Before(*end))
}
func matches(pattern, key string) bool {
	if pattern == "*" || pattern == key {
		return true
	}
	return strings.HasSuffix(pattern, ".*") && strings.HasPrefix(key, strings.TrimSuffix(pattern, "*"))
}
func validPattern(v string) bool {
	if v == "*" {
		return true
	}
	if len(v) < 1 || len(v) > 160 {
		return false
	}
	for i, c := range v {
		if c == '*' && i == len(v)-1 && i > 0 && v[i-1] == '.' {
			continue
		}
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_') {
			return false
		}
	}
	return !strings.HasPrefix(v, ".") && !strings.Contains(v, "..") && !strings.HasSuffix(v, ".")
}
func (s *Service) accountAllowed(tx Transaction, now time.Time, login bool) error {
	a := tx.Account()
	if a.Disabled {
		return ErrDisabled
	}
	p, e := tx.Punishments()
	if e != nil {
		return e
	}
	for _, v := range p {
		if active(v.StartsAt, v.ExpiresAt, now) && (v.Kind == "disable_account" || login && v.Kind == "block_login") {
			return ErrDisabled
		}
	}
	return nil
}
func (s *Service) check(tx Transaction, c claims, now time.Time) (Principal, error) {
	a := tx.Account()
	se, e := tx.Session(c.SessionID)
	if e != nil {
		return Principal{}, e
	}
	if a.ID != c.Subject || se.AccountID != a.ID {
		return Principal{}, ErrInvalid
	}
	if e = s.accountAllowed(tx, now, false); e != nil {
		return Principal{}, e
	}
	if se.RevokedAt != nil {
		return Principal{}, ErrRevoked
	}
	if a.Version != c.Version || se.AccountVersion != a.Version || se.Epoch != c.Epoch {
		return Principal{}, ErrStale
	}
	if c.Issued > now.Unix() || c.NotBefore > now.Unix() {
		return Principal{}, ErrInvalid
	}
	if c.Expires > se.ExpiresAt.Unix() {
		return Principal{}, ErrInvalid
	}
	if !now.Before(se.ExpiresAt) {
		return Principal{}, ErrExpired
	}
	if now.Unix() >= c.Expires {
		return Principal{}, ErrExpired
	}
	return Principal{Account: a, Session: se}, nil
}
func (s *Service) with(ctx context.Context, token string, fn func(Transaction, Principal, time.Time) error) (Principal, error) {
	c, e := s.verify(token)
	if e != nil {
		return Principal{}, e
	}
	return s.withClaims(ctx, c, fn)
}

type verifiedCredential struct {
	service *Service
	claims  claims
	account Account
	session Session
}

func (s *Service) withClaims(ctx context.Context, c claims, fn func(Transaction, Principal, time.Time) error) (Principal, error) {
	var p Principal
	var read func(context.Context, string, func(Transaction) error) error
	if s.reader != nil {
		read = s.reader.ReadAccount
	} else {
		read = s.store.WithinAccount
	}
	e := read(ctx, c.Subject, func(tx Transaction) error {
		now := s.cfg.Now().UTC()
		var e error
		p, e = s.check(tx, c, now)
		if e != nil {
			return e
		}
		if fn != nil {
			return fn(tx, p, now)
		}
		return nil
	})
	if e != nil {
		return Principal{}, known(e)
	}
	p.Session.RefreshHash = nil
	p.Session.PreviousHash = nil
	p.Session.SealedPair = nil
	p.Session.Scopes = append([]string(nil), p.Session.Scopes...)
	bound := p.Session
	bound.Scopes = append([]string(nil), p.Session.Scopes...)
	p.proof = &verifiedCredential{service: s, claims: c, account: p.Account, session: bound}
	return p, nil
}
func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	return s.with(ctx, token, nil)
}
func (s *Service) Authorize(ctx context.Context, token string, permissions ...string) (Principal, error) {
	return s.with(ctx, token, policy(permissions))
}

// AuthorizePrincipal accepts only an unforgeable, service-bound verification
// proof and rechecks authority state. Altered projections are rejected.
func (s *Service) AuthorizePrincipal(ctx context.Context, p Principal, permissions ...string) (Principal, error) {
	if p.proof == nil || p.proof.service != s || !reflect.DeepEqual(p.Account, p.proof.account) || !reflect.DeepEqual(p.Session, p.proof.session) {
		return Principal{}, ErrInvalid
	}
	return s.withClaims(ctx, p.proof.claims, policy(permissions))
}
func policy(permissions []string) func(Transaction, Principal, time.Time) error {
	return func(tx Transaction, p Principal, now time.Time) error {
		if len(permissions) == 0 {
			return ErrForbidden
		}
		rules, e := tx.Rules()
		if e != nil {
			return e
		}
		punishments, e := tx.Punishments()
		if e != nil {
			return e
		}
		for _, key := range permissions {
			if !validPattern(key) || strings.Contains(key, "*") {
				return ErrForbidden
			}
			scope := false
			for _, v := range p.Session.Scopes {
				scope = scope || matches(v, key)
			}
			if !scope {
				return ErrForbidden
			}
			for _, v := range punishments {
				if v.Kind == "permission_modification" && active(v.StartsAt, v.ExpiresAt, now) && matches(v.Pattern, key) {
					return ErrForbidden
				}
			}
			allow := false
			for _, r := range rules {
				if active(r.StartsAt, r.ExpiresAt, now) && matches(r.Pattern, key) {
					if r.Deny {
						return ErrForbidden
					}
					allow = true
				}
			}
			if !allow && !p.Account.Superuser {
				return ErrForbidden
			}
		}
		return nil
	}
}
func (s *Service) newSession(tx Transaction, o SessionOptions, now time.Time) (Session, Pair, error) {
	var se Session
	var pair Pair
	if o.Type != 0 && o.Type != 1 || o.Type == 1 && o.ClientID == "" || o.Type == 0 && o.ClientID != "" || len(o.Label) > 200 || len(o.ClientID) > 200 || len(o.Scopes) == 0 || len(o.Scopes) > 100 {
		return se, pair, ErrInvalid
	}
	for _, v := range o.Scopes {
		if !validPattern(v) {
			return se, pair, ErrInvalid
		}
	}
	if e := s.accountAllowed(tx, now, true); e != nil {
		return se, pair, e
	}
	a := tx.Account()
	id, e := randomID()
	if e != nil {
		return se, pair, e
	}
	se = Session{ID: id, AccountID: a.ID, Label: o.Label, Type: o.Type, ClientID: o.ClientID, CreatedAt: now, LastGrantedAt: now, Category: "device", Epoch: 1, AccountVersion: a.Version, ExpiresAt: now.Add(s.cfg.SessionTTL), Scopes: append([]string(nil), o.Scopes...)}
	pair, e = s.mint(a, &se, now)
	if e != nil {
		return se, pair, e
	}
	return se, pair, tx.SaveSession(se)
}
func (s *Service) mint(a Account, se *Session, now time.Time) (Pair, error) {
	r, e := secret()
	if e != nil {
		return Pair{}, e
	}
	token, e := s.sign(a, *se, now)
	if e != nil {
		return Pair{}, e
	}
	se.RefreshHash = s.digest(r)
	exp := now.Add(s.cfg.AccessTTL)
	if se.ExpiresAt.Before(exp) {
		exp = se.ExpiresAt
	}
	return Pair{token, r, exp.Unix() - now.Unix(), se.ExpiresAt.Unix() - now.Unix()}, nil
}

// IssueSession is a trusted in-process issuer for a separately authenticated
// login or synthetic test fixture. There is deliberately no HTTP issue route.
func (s *Service) IssueSession(ctx context.Context, account string, o SessionOptions) (Pair, error) {
	var p Pair
	e := s.store.WithinAccount(ctx, account, func(tx Transaction) error {
		_, pair, e := s.newSession(tx, o, s.cfg.Now().UTC())
		p = pair
		return e
	})
	return p, known(e)
}

// CreateLoginCode is a trusted login-completion hook, not an authentication
// factor. Codes are one-use, hashed at rest, and expire after one minute.
func (s *Service) CreateLoginCode(ctx context.Context, account, label string, scopes []string) (string, error) {
	raw, e := secret()
	if e != nil {
		return "", e
	}
	if len(scopes) == 0 || len(scopes) > 100 || len(label) > 200 {
		return "", ErrInvalid
	}
	for _, v := range scopes {
		if !validPattern(v) {
			return "", ErrInvalid
		}
	}
	e = s.store.WithinAccount(ctx, account, func(tx Transaction) error {
		now := s.cfg.Now().UTC()
		if e := s.accountAllowed(tx, now, true); e != nil {
			return e
		}
		return tx.SaveCode(LoginCode{AccountVersion: tx.Account().Version, Hash: s.digest(raw), AccountID: account, ExpiresAt: now.Add(time.Minute), Label: label, Scopes: append([]string(nil), scopes...)})
	})
	if e != nil {
		return "", known(e)
	}
	return raw, nil
}
func (s *Service) ExchangeCode(ctx context.Context, raw string) (Pair, error) {
	var pair Pair
	if !validSecret(raw) {
		return pair, ErrInvalid
	}
	hash := s.digest(raw)
	account, e := s.store.FindCode(ctx, hash)
	if e != nil {
		return pair, known(e)
	}
	e = s.store.WithinAccount(ctx, account, func(tx Transaction) error {
		c, e := tx.Code(hash)
		if e != nil {
			return e
		}
		now := s.cfg.Now().UTC()
		if c.Used || c.AccountVersion != tx.Account().Version || !now.Before(c.ExpiresAt) {
			return ErrInvalid
		}
		_, pair, e = s.newSession(tx, SessionOptions{Label: c.Label, Scopes: c.Scopes}, now)
		if e != nil {
			return e
		}
		c.Used = true
		return tx.SaveCode(c)
	})
	return pair, known(e)
}
func (s *Service) Refresh(ctx context.Context, raw string, lane int, client string) (Pair, error) {
	var pair Pair
	if !validSecret(raw) {
		return pair, ErrRefresh
	}
	hash := s.digest(raw)
	account, id, e := s.store.FindRefresh(ctx, hash)
	if errors.Is(e, ErrNotFound) {
		return pair, ErrRefresh
	}
	if e != nil {
		return pair, known(e)
	}
	e = s.store.WithinAccount(ctx, account, func(tx Transaction) error {
		se, e := tx.Session(id)
		if e != nil {
			return e
		}
		now := s.cfg.Now().UTC()
		if se.Type != lane || se.ClientID != client || lane != 0 && lane != 1 {
			return ErrRefresh
		}
		if e = s.accountAllowed(tx, now, true); e != nil {
			return e
		}
		if se.RevokedAt != nil {
			return ErrRevoked
		}
		a := tx.Account()
		if se.AccountVersion != a.Version {
			return ErrStale
		}
		if !now.Before(se.ExpiresAt) {
			return ErrRefresh
		}
		if len(se.PreviousHash) > 0 && hmac.Equal(hash, se.PreviousHash) {
			if !now.Before(se.PreviousUntil) {
				return ErrRefresh
			}
			pair, e = s.openPair(se.SealedPair, se.ID)
			if e != nil {
				return e
			}
			c, e := s.verify(pair.Token)
			if e != nil || c.Version != a.Version || c.Epoch != se.Epoch || c.Expires <= now.Unix() {
				return ErrRefresh
			}
			pair.ExpiresIn = c.Expires - now.Unix()
			pair.RefreshExpiresIn = se.ExpiresAt.Unix() - now.Unix()
			return nil
		}
		if !hmac.Equal(hash, se.RefreshHash) {
			return ErrRefresh
		}
		se.PreviousHash = append([]byte(nil), se.RefreshHash...)
		se.PreviousUntil = now.Add(s.cfg.RefreshGrace)
		se.Epoch++
		se.LastGrantedAt = now
		se.ExpiresAt = now.Add(s.cfg.SessionTTL)
		pair, e = s.mint(a, &se, now)
		if e != nil {
			return e
		}
		se.SealedPair, e = s.sealPair(pair, se.ID)
		if e != nil {
			return e
		}
		return tx.SaveSession(se)
	})
	return pair, known(e)
}
func (s *Service) Sessions(ctx context.Context, token string, offset, take int, children bool) ([]Session, int, error) {
	if offset < 0 || take < 1 || take > 200 {
		return nil, 0, ErrInvalid
	}
	var out []Session
	total := 0
	_, e := s.with(ctx, token, func(tx Transaction, p Principal, now time.Time) error {
		if p.Session.Type != 0 {
			return ErrForbidden
		}
		rows, e := tx.Sessions()
		if e != nil {
			return e
		}
		out = []Session{}
		for _, se := range rows {
			if se.RevokedAt != nil || se.AccountVersion != p.Account.Version || !now.Before(se.ExpiresAt) || !children && se.ParentID != "" {
				continue
			}
			se.IsCurrent = se.ID == p.Session.ID
			se.RefreshHash = nil
			se.PreviousHash = nil
			se.SealedPair = nil
			se.Scopes = append([]string(nil), se.Scopes...)
			if total >= offset && len(out) < take {
				out = append(out, se)
			}
			total++
		}
		return nil
	})
	return out, total, e
}
func (s *Service) Revoke(ctx context.Context, token, target string) error {
	_, e := s.with(ctx, token, func(tx Transaction, p Principal, now time.Time) error {
		if p.Session.Type != 0 {
			return ErrForbidden
		}
		all, e := tx.Sessions()
		if e != nil {
			return e
		}
		selected := map[string]bool{}
		switch target {
		case "current":
			selected[p.Session.ID] = true
		case "other":
			for _, se := range all {
				if se.ID != p.Session.ID {
					selected[se.ID] = true
				}
			}
		default:
			if !ValidID(target) {
				return ErrSessionNotFound
			}
			if _, e := tx.Session(target); e != nil {
				if errors.Is(e, ErrNotFound) {
					return ErrSessionNotFound
				}
				return e
			}
			selected[target] = true
		}
		for changed := true; changed; {
			changed = false
			for _, se := range all {
				if selected[se.ParentID] && !selected[se.ID] {
					selected[se.ID] = true
					changed = true
				}
			}
		}
		for _, se := range all {
			if selected[se.ID] && se.RevokedAt == nil {
				se.RevokedAt = &now
				se.Epoch++
				se.RefreshHash = nil
				se.PreviousHash = nil
				se.SealedPair = nil
				if e = tx.SaveSession(se); e != nil {
					return e
				}
			}
		}
		return nil
	})
	return e
}

// InvalidateAccount is a trusted administrative hook. It invalidates all access
// AND refresh sessions; it is not exposed by the HTTP handler.
func (s *Service) InvalidateAccount(ctx context.Context, account string) error {
	return known(s.store.WithinAccount(ctx, account, func(tx Transaction) error { a := tx.Account(); a.Version++; return tx.SaveAccount(a) }))
}
