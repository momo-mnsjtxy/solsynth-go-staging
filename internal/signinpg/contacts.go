// SPDX-License-Identifier: AGPL-3.0-only
package signinpg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"solsynth.local/go-migration/internal/auditcapture"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
	"sort"
	"time"
)

// All email-ownership writers, including registration, take these advisory
// locks before checking primary credentials and verified secondary contacts.
func lockEmails(ctx context.Context, tx *sql.Tx, emails ...string) error {
	keys := []int64{}
	seen := map[int64]bool{}
	for _, email := range emails {
		h := sha256.Sum256([]byte("owned-canonical-email\x00" + email))
		key := int64(binary.BigEndian.Uint64(h[:8]))
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, key := range keys {
		if _, e := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1::bigint)`, key); e != nil {
			return e
		}
	}
	return nil
}
func emailOwned(ctx context.Context, tx *sql.Tx, email, actor string) (bool, error) {
	var used bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_authority_credentials WHERE email=$1::text AND account_id::text<>$2::text)OR EXISTS(SELECT 1 FROM owned_authority_contacts WHERE type=0 AND content=$1::text AND verified_at IS NOT NULL AND deleted_at IS NULL AND account_id::text<>$2::text)`, email, actor).Scan(&used)
	return used, e
}
func contactID() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}

const contactFields = `m.id::text,m.account_id::text,m.type,COALESCE(c.email,m.content),COALESCE(c.verified_at,m.verified_at),m.credential_account_id IS NOT NULL,m.is_public,m.created_at,GREATEST(m.updated_at,COALESCE(c.verified_at,m.updated_at)),m.deleted_at`

type contactScanner interface{ Scan(...any) error }

func scanContact(row contactScanner) (v signin.Contact, e error) {
	e = row.Scan(&v.ID, &v.AccountID, &v.Type, &v.Content, &v.VerifiedAt, &v.IsPrimary, &v.IsPublic, &v.CreatedAt, &v.UpdatedAt, &v.DeletedAt)
	if e == sql.ErrNoRows {
		e = authority.ErrNotFound
	}
	return
}
func (t *transaction) AuthorizeContacts() error {
	if t.repo.guard == nil {
		return authority.ErrUnavailable
	}
	_, e := t.repo.guard.Authorize(t.ctx, t.repo.db, t.tx, t.canonical.Account().ID, "account.contacts.manage")
	return e
}
func (t *transaction) Contact(id string) (signin.Contact, error) {
	if !authority.ValidID(id) {
		return signin.Contact{}, signin.ErrInput
	}
	return scanContact(t.tx.QueryRowContext(t.ctx, `SELECT `+contactFields+` FROM owned_authority_contacts m LEFT JOIN owned_authority_credentials c ON c.account_id=m.credential_account_id WHERE m.id=$1::uuid AND m.account_id=$2::uuid AND m.deleted_at IS NULL`, id, t.canonical.Account().ID))
}
func (t *transaction) Contacts() (out []signin.Contact, e error) {
	out = []signin.Contact{}
	rows, e := t.tx.QueryContext(t.ctx, `SELECT `+contactFields+` FROM owned_authority_contacts m LEFT JOIN owned_authority_credentials c ON c.account_id=m.credential_account_id WHERE m.account_id=$1::uuid AND m.deleted_at IS NULL ORDER BY m.credential_account_id IS NOT NULL DESC,m.created_at,m.id LIMIT 33`, t.canonical.Account().ID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		v, e := scanContact(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
		if len(out) > 32 {
			return nil, signin.ErrConflict
		}
	}
	return out, rows.Err()
}
func (t *transaction) contactAudit(id, action string, now time.Time) error {
	var record int64
	actor := t.canonical.Account().ID
	if e := t.tx.QueryRowContext(t.ctx, `INSERT INTO owned_authority_contact_audit(account_id,contact_id,action,occurred_at)VALUES($1::uuid,$2::uuid,$3::text,$4::timestamptz)RETURNING id`, actor, id, action, now).Scan(&record); e != nil {
		return e
	}
	return auditcapture.Record(t.ctx, t.tx, actor, "owned_authority_contact_audit", record)
}
func (t *transaction) AddContact(input signin.ContactInput, now time.Time) (out signin.Contact, e error) {
	if e = signin.ValidateContact(&input); e != nil {
		return
	}
	actor := t.canonical.Account().ID
	if input.Type == 0 {
		if e = lockEmails(t.ctx, t.tx, input.Content); e != nil {
			return
		}
		var used bool
		used, e = emailOwned(t.ctx, t.tx, input.Content, "")
		if e != nil {
			return
		}
		if used {
			return out, signin.ErrConflict
		}
	}
	if e = t.AuthorizeContacts(); e != nil {
		return out, e
	}
	var n int
	if e = t.tx.QueryRowContext(t.ctx, `SELECT count(*) FROM owned_authority_contacts WHERE account_id=$1::uuid AND deleted_at IS NULL`, actor).Scan(&n); e != nil {
		return
	}
	if n >= 32 {
		return out, signin.ErrConflict
	}
	id, e := contactID()
	if e != nil {
		return out, e
	}
	_, e = t.tx.ExecContext(t.ctx, `INSERT INTO owned_authority_contacts(id,account_id,type,content,created_at,updated_at)VALUES($1::uuid,$2::uuid,$3::integer,$4::text,$5::timestamptz,$5::timestamptz)`, id, actor, input.Type, input.Content, now)
	if e != nil {
		return out, conflict(e)
	}
	if e = t.contactAudit(id, "contact.create", now); e != nil {
		return
	}
	return t.Contact(id)
}
func (t *transaction) CancelContactChallenges(id string) error {
	_, e := t.tx.ExecContext(t.ctx, `UPDATE owned_authority_challenges SET completed=true WHERE account_id=$1::uuid AND purpose='verify_contact' AND contact_id=$2::uuid`, t.canonical.Account().ID, id)
	return e
}
func (t *transaction) DeleteContact(id string, now time.Time) error {
	c, e := t.Contact(id)
	if e != nil {
		return e
	}
	if c.IsPrimary {
		return signin.ErrConflict
	}
	if c.Type == 0 {
		if e = lockEmails(t.ctx, t.tx, c.Content); e != nil {
			return e
		}
	}
	if e = t.AuthorizeContacts(); e != nil {
		return e
	}
	if _, e = t.tx.ExecContext(t.ctx, `UPDATE owned_authority_contacts SET deleted_at=$2::timestamptz,updated_at=$2::timestamptz,is_public=false WHERE id=$1::uuid`, id, now); e != nil {
		return e
	}
	if e = t.CancelContactChallenges(id); e != nil {
		return e
	}
	return t.contactAudit(id, "contact.delete", now)
}
func (t *transaction) SetContactPublic(id string, public bool, now time.Time) (out signin.Contact, e error) {
	out, e = t.Contact(id)
	if e != nil {
		return
	}
	if public && out.VerifiedAt == nil {
		return out, signin.ErrUnverified
	}
	if out.IsPublic == public {
		return
	}
	if _, e = t.tx.ExecContext(t.ctx, `UPDATE owned_authority_contacts SET is_public=$2::boolean,updated_at=$3::timestamptz WHERE id=$1::uuid`, id, public, now); e != nil {
		return
	}
	if e = t.contactAudit(id, "contact.visibility", now); e != nil {
		return
	}
	return t.Contact(id)
}
func (t *transaction) VerifyContact(id string, now time.Time) error {
	c, e := t.Contact(id)
	if e != nil {
		return e
	}
	if c.Type != 0 {
		return signin.ErrUnsupported
	}
	if c.VerifiedAt != nil {
		return signin.ErrConflict
	}
	if e = lockEmails(t.ctx, t.tx, c.Content); e != nil {
		return e
	}
	used, e := emailOwned(t.ctx, t.tx, c.Content, c.AccountID)
	if e != nil {
		return e
	}
	if used {
		return signin.ErrConflict
	}
	if c.IsPrimary {
		_, e = t.tx.ExecContext(t.ctx, `UPDATE owned_authority_credentials SET verified_at=$2::timestamptz WHERE account_id=$1::uuid AND email=$3::text`, c.AccountID, now, c.Content)
	} else {
		_, e = t.tx.ExecContext(t.ctx, `UPDATE owned_authority_contacts SET verified_at=$2::timestamptz,updated_at=$2::timestamptz WHERE id=$1::uuid`, c.ID, now)
	}
	if e != nil {
		return conflict(e)
	}
	return t.contactAudit(id, "contact.verified", now)
}
func (t *transaction) SetContactPrimary(id string, now time.Time) (out signin.Contact, e error) {
	out, e = t.Contact(id)
	if e != nil {
		return
	}
	if out.Type != 0 {
		return out, signin.ErrUnsupported
	}
	if out.VerifiedAt == nil {
		return out, signin.ErrUnverified
	}
	if out.IsPrimary {
		return
	}
	var current signin.Contact
	current, e = scanContact(t.tx.QueryRowContext(t.ctx, `SELECT `+contactFields+` FROM owned_authority_contacts m JOIN owned_authority_credentials c ON c.account_id=m.credential_account_id WHERE m.account_id=$1::uuid AND m.deleted_at IS NULL`, out.AccountID))
	if e != nil {
		return
	}
	if e = lockEmails(t.ctx, t.tx, current.Content, out.Content); e != nil {
		return
	}
	used, e := emailOwned(t.ctx, t.tx, out.Content, out.AccountID)
	if e != nil {
		return out, e
	}
	if used {
		return out, signin.ErrConflict
	}
	if e = t.AuthorizeContacts(); e != nil {
		return out, e
	}
	if _, e = t.tx.ExecContext(t.ctx, `UPDATE owned_authority_credentials SET email=$2::text,verified_at=$3::timestamptz WHERE account_id=$1::uuid`, out.AccountID, out.Content, out.VerifiedAt); e != nil {
		return out, conflict(e)
	}
	if _, e = t.tx.ExecContext(t.ctx, `UPDATE owned_authority_contacts SET credential_account_id=NULL,content=$2::text,verified_at=$3::timestamptz,updated_at=$4::timestamptz WHERE id=$1::uuid`, current.ID, current.Content, current.VerifiedAt, now); e != nil {
		return out, conflict(e)
	}
	if _, e = t.tx.ExecContext(t.ctx, `UPDATE owned_authority_contacts SET credential_account_id=$2::uuid,content=NULL,verified_at=NULL,updated_at=$3::timestamptz WHERE id=$1::uuid`, out.ID, out.AccountID, now); e != nil {
		return
	}
	a := t.canonical.Account()
	a.Version++
	if e = t.canonical.SaveAccount(a); e != nil {
		return
	}
	if e = t.CancelChallenges(""); e != nil {
		return
	}
	if e = t.contactAudit(id, "contact.primary", now); e != nil {
		return
	}
	return t.Contact(id)
}
