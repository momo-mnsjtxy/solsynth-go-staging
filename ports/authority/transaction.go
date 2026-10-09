// SPDX-License-Identifier: AGPL-3.0-only
package authority

import (
	"context"
	"reflect"
)

// AuthenticateTransaction rechecks an original, unmodified proof using the
// caller's locked authority snapshot. The trusted storage adapter must acquire
// the canonical account lock before calling and retain it through domain commit.
// This method performs no separate database read and never accepts bearer bytes.
func (v *Verifier) AuthenticateTransaction(ctx context.Context, p Principal, tx Transaction) (Principal, error) {
	return v.checkTransaction(ctx, p, tx, false, nil, "")
}

// AuthorizeTransaction additionally checks current scopes, grants and
// punishments. An empty permission list denies, just like AuthorizePrincipal.
func (v *Verifier) AuthorizeTransaction(ctx context.Context, p Principal, tx Transaction, permissions ...string) (Principal, error) {
	return v.checkTransaction(ctx, p, tx, true, permissions, "")
}

// AuthorizeTransactionWithGrant preserves ordinary permission semantics while
// requiring a current matching grant for one sensitive permission. Superuser
// fallback cannot satisfy that grant. Proof, scopes, denies and both policies
// use the same fresh canonical account/session snapshot and clock observation.
func (v *Verifier) AuthorizeTransactionWithGrant(ctx context.Context, p Principal, tx Transaction, requiredGrant string, permissions ...string) (Principal, error) {
	if requiredGrant == "" {
		return Principal{}, ErrForbidden
	}
	keys := append(append([]string(nil), permissions...), requiredGrant)
	return v.checkTransaction(ctx, p, tx, true, keys, requiredGrant)
}

func (v *Verifier) checkTransaction(ctx context.Context, p Principal, tx Transaction, authorize bool, permissions []string, requiredGrant string) (Principal, error) {
	if v == nil || v.service == nil || tx == nil {
		return Principal{}, ErrUnavailable
	}
	s := v.service
	if p.proof == nil || p.proof.service != s || !reflect.DeepEqual(p.Account, p.proof.account) || !reflect.DeepEqual(p.Session, p.proof.session) {
		return Principal{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return Principal{}, ErrUnavailable
	}
	now := s.cfg.Now().UTC()
	fresh, err := s.check(tx, p.proof.claims, now)
	if err != nil {
		return Principal{}, known(err)
	}
	if authorize {
		if err = policy(permissions)(tx, fresh, now); err != nil {
			return Principal{}, known(err)
		}
	}
	if requiredGrant != "" {
		account := fresh.Account
		account.Superuser = false
		if err = evaluatePolicy(tx, account, fresh.Session, now, []string{requiredGrant}); err != nil {
			return Principal{}, known(err)
		}
	}
	// Return a safe projection, not a new reusable verification capability.
	// The caller retains the original signed proof for any later operation.
	fresh.Session.RefreshHash = nil
	fresh.Session.PreviousHash = nil
	fresh.Session.SealedPair = nil
	fresh.Session.Scopes = append([]string(nil), fresh.Session.Scopes...)
	return fresh, nil
}
