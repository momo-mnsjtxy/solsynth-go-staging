// SPDX-License-Identifier: AGPL-3.0-only
package ticketpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
	"solsynth.local/go-migration/ports/passport/tickets"
	"strings"
)

type ActivationCompletion struct {
	PolicyID    string   `json:"policy_id"`
	Permissions []string `json:"permissions"`
}

func ValidateActivationCompletion(c ActivationCompletion) error {
	if len(c.PolicyID) < 1 || len(c.PolicyID) > 128 || strings.TrimSpace(c.PolicyID) != c.PolicyID || len(c.Permissions) > 32 {
		return tickets.ErrInvalid
	}
	for _, ch := range c.PolicyID {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || strings.ContainsRune("._-", ch)) {
			return tickets.ErrInvalid
		}
	}
	seen := map[string]bool{}
	for _, key := range c.Permissions {
		if !authority.MemberActivationPermission(key) || seen[key] {
			return tickets.ErrInvalid
		}
		seen[key] = true
	}
	return nil
}

type activationProofKey struct{}

// A completion proof is bound to its subject and source. An examiner's root
// session is never the examined account's proof; contact proof has no session.
type activationProof struct {
	subject, session, purpose, contact string
	challenge                          [32]byte
	version                            int64
}

func permitRootActivation(ctx context.Context, p authority.Principal) context.Context {
	return context.WithValue(ctx, activationProofKey{}, activationProof{subject: p.Account.ID, session: p.Session.ID})
}
func activationAssuranceFor(ctx context.Context, actor string) (activationProof, bool) {
	p, ok := ctx.Value(activationProofKey{}).(activationProof)
	if !ok || !authority.ValidID(actor) || p.subject != actor {
		return activationProof{}, false
	}
	if p.purpose == "" {
		return p, authority.ValidID(p.session)
	}
	return p, p.session == "" && (p.purpose == "verify_email" && p.contact == "" || p.purpose == "verify_contact" && authority.ValidID(p.contact))
}

// currentActivationAssurance is evaluated after eligibility waits and again
// after grant writes. The caller already holds the canonical subject lock.
func (r *Repository) currentActivationAssurance(ctx context.Context, tx *sql.Tx, actor string) (bool, error) {
	p, ok := activationAssuranceFor(ctx, actor)
	if !ok {
		return false, nil
	}
	if p.purpose == "" {
		current, e := r.guard.Authenticate(ctx, r.db, tx, actor)
		if e != nil {
			return false, e
		}
		if rootActivation(current) != nil || current.Session.ID != p.session {
			return false, authority.ErrForbidden
		}
		return true, nil
	}
	if e := r.checkContactProof(ctx, tx, p, true); e != nil {
		return false, e
	}
	return true, nil
}

func (r *Repository) checkContactProof(ctx context.Context, tx *sql.Tx, p activationProof, completion bool) error {
	var valid bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_authority_challenges c JOIN owned_authority_accounts a ON a.id=c.account_id WHERE c.hash=$1 AND c.account_id=$2 AND c.account_version=$3 AND a.version=c.account_version AND c.purpose=$4 AND c.contact_id IS NOT DISTINCT FROM $5::uuid AND NOT c.completed AND c.expires_at>$6 AND NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND (p.kind='disable_account' OR $7 AND p.kind='block_login') AND(p.starts_at IS NULL OR p.starts_at<=$6)AND(p.expires_at IS NULL OR p.expires_at>$6)) AND(NOT $7 OR EXISTS(SELECT 1 FROM owned_authority_credentials v WHERE v.account_id=a.id AND v.verified_at IS NOT NULL) AND(c.purpose='verify_email' OR EXISTS(SELECT 1 FROM owned_authority_contacts m WHERE m.id=c.contact_id AND m.account_id=a.id AND m.credential_account_id=a.id AND m.deleted_at IS NULL))))`, p.challenge[:], p.subject, p.version, p.purpose, nullable(p.contact), r.cfg.Now().UTC(), completion).Scan(&valid)
	if e != nil {
		return e
	}
	if !valid {
		return authority.ErrInvalid
	}
	return nil
}

type InviteAdmission interface {
	TestsBypassed(context.Context, *sql.Tx, string) (bool, error)
}

func (r *Repository) WithInviteAdmission(v InviteAdmission) *Repository { r.invites = v; return r }
func rootActivation(p authority.Principal) error {
	if !authority.ValidID(p.Account.ID) || !authority.ValidID(p.Session.ID) || p.Session.Type != 0 || p.Session.ParentID != "" || p.Session.ClientID != "" || p.Session.AccountID != p.Account.ID {
		return authority.ErrForbidden
	}
	return nil
}

// ActivationInTransaction joins affiliation admission under the caller's account
// locks. Enrollment can record eligibility, but only a matching canonical root
// session supplies completion proof here. Contact completion has its own entry.
func (r *Repository) ActivationInTransaction(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, request bool) (tickets.ActivationState, error) {
	if r == nil || r.db != db || tx == nil || !authority.ValidID(actor) || r.cfg.Activation == nil {
		return tickets.ActivationState{}, tickets.ErrUnavailable
	}
	p, e := r.guard.Authenticate(ctx, db, tx, actor)
	if e != nil && !errors.Is(e, authority.ErrInvalid) {
		return tickets.ActivationState{}, e
	}
	if e == nil && rootActivation(p) == nil {
		ctx = permitRootActivation(ctx, p)
	}
	return r.activation(ctx, tx, actor, request, r.cfg.Now().UTC())
}

// ContactVerified receives the canonical challenge consumed by the first-party
// sign-in transaction. A raw caller-selected subject or a root examiner cannot
// fabricate contact proof; the challenge is reread before and after completion.
func (r *Repository) ContactVerified(ctx context.Context, db *sql.DB, tx *sql.Tx, c signin.Challenge) error {
	if r == nil || r.db != db || tx == nil {
		return tickets.ErrUnavailable
	}
	if r.cfg.Activation == nil {
		return nil
	}
	if !authority.ValidID(c.AccountID) || len(c.Hash) != 32 || c.Completed || c.Version < 1 ||
		(c.Purpose != "verify_email" || c.ContactID != "") && (c.Purpose != "verify_contact" || !authority.ValidID(c.ContactID)) {
		return authority.ErrInvalid
	}
	proof := activationProof{subject: c.AccountID, purpose: c.Purpose, contact: c.ContactID, version: c.Version}
	copy(proof.challenge[:], c.Hash)
	if e := r.checkContactProof(ctx, tx, proof, false); e != nil {
		return e
	}
	if c.Purpose == "verify_contact" {
		var primary bool
		if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_authority_contacts WHERE id=$1 AND account_id=$2 AND credential_account_id=$2 AND deleted_at IS NULL)`, c.ContactID, c.AccountID).Scan(&primary); e != nil {
			return e
		}
		if !primary {
			return nil
		}
	}
	ctx = context.WithValue(ctx, activationProofKey{}, proof)
	_, e := r.activation(ctx, tx, c.AccountID, true, r.cfg.Now().UTC())
	return e
}
