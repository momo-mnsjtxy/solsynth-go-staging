// SPDX-License-Identifier: AGPL-3.0-only
package signinpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
)

type RegistrationAdmission interface {
	LockRegistrationAdmission(context.Context, *sql.DB, *sql.Tx, string, string) error
	AdmitRegistration(context.Context, *sql.DB, *sql.Tx, string, string) error
}
type ContactActivation interface {
	ContactVerified(context.Context, *sql.DB, *sql.Tx, signin.Challenge) error
}

func (r *Repository) WithAffiliation(v RegistrationAdmission) *Repository { r.admission = v; return r }
func (r *Repository) WithActivation(v ContactActivation) *Repository      { r.activation = v; return r }

// CompleteContactVerification is invoked only by the sign-in challenge consumer,
// after persisting verification and before consuming that same one-use proof.
func (t *transaction) CompleteContactVerification(c signin.Challenge) error {
	if c.AccountID != t.canonical.Account().ID || c.Version != t.canonical.Account().Version {
		return authority.ErrInvalid
	}
	if t.repo.activation == nil {
		return nil
	}
	return t.repo.activation.ContactVerified(t.ctx, t.repo.db, t.tx, c)
}
