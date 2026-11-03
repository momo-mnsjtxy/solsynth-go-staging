// SPDX-License-Identifier: AGPL-3.0-only
// Session elevation follows the licensed Socommon challenge caller contract.
package signin

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	"time"
)

var ErrNoAuthFactors = &authority.Failure{Code: "AUTH_NO_AUTH_FACTORS", Message: "No configured authentication factors can satisfy re-authentication.", Status: 403}

type SudoTransaction interface {
	SudoPolicyVersion() (int64, error)
	SaveSudoElevation(Challenge, authority.Principal, time.Time) error
	SaveSudoEmail([]byte, []byte, time.Time) error
	SudoEmail([]byte) ([]byte, time.Time, bool, error)
	ConsumeSudoEmail([]byte, []byte, time.Time) error
}

// StartSudo creates an authenticated, account/session/epoch-bound challenge.
// Password is followed by enabled OTP/recovery, or verified-email fallback.
func (s *Service) StartSudo(ctx context.Context, token, peer, userAgent string) (Challenge, error) {
	var out Challenge
	if len(userAgent) > 512 {
		userAgent = userAgent[:512]
	}
	if err := s.limit(ctx, "sudo-peer", peer, 20, 10*time.Minute); err != nil {
		return out, err
	}
	p, err := s.verifier.Authenticate(ctx, token)
	if err != nil {
		return out, err
	}
	if err = s.limit(ctx, "sudo-account", p.Account.ID, 20, 10*time.Minute); err != nil {
		return out, err
	}
	err = s.authenticated(ctx, token, false, func(tx Transaction, p authority.Principal) error {
		if p.Session.Type != 0 || p.Session.Category != "device" || p.Session.ParentID != "" || p.Session.ClientID != "" {
			return authority.ErrSudoRequired
		}
		if err := s.active(tx, true); err != nil {
			return err
		}
		credentials, err := tx.Credentials()
		if errors.Is(err, authority.ErrNotFound) {
			return ErrNoAuthFactors
		}
		if err != nil {
			return err
		}
		if credentials.VerifiedAt == nil {
			return ErrNoAuthFactors
		}
		factors, err := tx.Factors()
		if err != nil {
			return err
		}
		password, second := false, false
		for _, factor := range factors {
			if factor.EnabledAt == nil {
				continue
			}
			password = password || factor.Type == 0
			second = second || factor.Type == 3 || factor.Type == 5
		}
		email := !second && s.cfg.Delivery != nil && credentials.Email != ""
		if !password || (!second && !email) {
			return ErrNoAuthFactors
		}
		if _, ok := tx.(SudoTransaction); !ok {
			return authority.ErrSudoUnavailable
		}
		out, err = s.makeChallenge(p.Account.ID, p.Account.Version, "sudo", "Re-authentication", 0, 5*time.Minute)
		if err != nil {
			return err
		}
		writer := tx.(SudoTransaction)
		out.SudoPolicyVersion, err = writer.SudoPolicyVersion()
		if err != nil {
			return err
		}
		out.SudoSessionID = p.Session.ID
		out.SudoSessionEpoch = p.Session.Epoch
		out.SudoEmail = email
		out.RequireSecond = true
		out.IPAddress = peer
		out.UserAgent = userAgent
		out = state(out, out.ID)
		return tx.SaveChallenge(out)
	})
	return out, err
}

// Optional bearer binding leaves unauthenticated login challenges usable. A sudo
// challenge still requires this proof and revalidates it in its own transaction.
func (s *Service) challengeContext(r *http.Request) context.Context {
	ctx := r.Context()
	token, err := authority.BearerToken(r)
	if err != nil {
		return ctx
	}
	p, err := s.verifier.Authenticate(ctx, token)
	if err != nil {
		return ctx
	}
	return s.store.Bind(ctx, p)
}
func (s *Service) validateChallenge(tx Transaction, c Challenge) error {
	if (c.Purpose != "login" && c.Purpose != "sudo") || c.Version != tx.Authority().Account().Version || !s.now().Before(c.ExpiredAt) {
		return ErrInvalid
	}
	if c.Purpose == "sudo" {
		writer, ok := tx.(SudoTransaction)
		if !ok {
			return authority.ErrSudoUnavailable
		}
		policy, err := writer.SudoPolicyVersion()
		if err != nil {
			return err
		}
		if policy != c.SudoPolicyVersion {
			return ErrInvalid
		}
		p, err := tx.Authenticate()
		if err != nil {
			return err
		}
		if p.Session.Type != 0 || p.Session.Category != "device" || p.Session.ParentID != "" || p.Session.ClientID != "" || p.Account.ID != c.AccountID || p.Session.ID != c.SudoSessionID || p.Session.Epoch != c.SudoSessionEpoch {
			return ErrInvalid
		}
	}
	return nil
}
func (s *Service) sudoEmailDigest(c Challenge, email, code string) []byte {
	h := hmac.New(sha256.New, s.cfg.SealKey)
	fmt.Fprintf(h, "owned.sudo.email.v1\x00%s\x00%s\x00%s\x00%s", c.SudoSessionID, email, c.ID, code)
	return h.Sum(nil)
}
func (s *Service) prepareSudoEmail(ctx context.Context, tx Transaction, c Challenge, raw string) error {
	if c.Purpose != "sudo" || !c.SudoEmail || c.Completed || c.SecondOK || s.cfg.Delivery == nil {
		return ErrInvalid
	}
	credentials, err := tx.Credentials()
	if err != nil {
		return err
	}
	if credentials.VerifiedAt == nil {
		return ErrInvalid
	}
	writer, ok := tx.(SudoTransaction)
	if !ok {
		return authority.ErrSudoUnavailable
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return err
	}
	code := fmt.Sprintf("%06d", n.Int64())
	c.ID = raw
	if err = writer.SaveSudoEmail(c.Hash, s.sudoEmailDigest(c, credentials.Email, code), c.ExpiredAt); err != nil {
		return err
	}
	if err = s.cfg.Delivery.Send(ctx, credentials.Email, "sudo", code); err != nil {
		return ErrDelivery
	}
	return nil
}
func (s *Service) verifySudoEmail(tx Transaction, c Challenge, raw, code string) error {
	if c.Purpose != "sudo" || !c.SudoEmail || c.SecondOK || len(code) != 6 {
		return ErrInvalid
	}
	credentials, err := tx.Credentials()
	if err != nil {
		return err
	}
	if credentials.VerifiedAt == nil {
		return ErrInvalid
	}
	writer, ok := tx.(SudoTransaction)
	if !ok {
		return authority.ErrSudoUnavailable
	}
	hash, expires, used, err := writer.SudoEmail(c.Hash)
	if err != nil {
		return err
	}
	if used || !s.now().Before(expires) {
		return ErrInvalid
	}
	c.ID = raw
	expected := s.sudoEmailDigest(c, credentials.Email, code)
	if subtle.ConstantTimeCompare(hash, expected) != 1 {
		return ErrInvalid
	}
	return writer.ConsumeSudoEmail(c.Hash, expected, s.now())
}
