// SPDX-License-Identifier: AGPL-3.0-only
package notabledaypg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/notabledays"
	"strings"
)

// EventMediaStore participates in the calendar transaction and never owns bytes,
// quotas, or identities. Apply runs after the actor and event locks; it may lock
// canonical files in UUID order. Load and Remove do not take additional locks.
type EventMediaStore interface {
	Apply(context.Context, *sql.Tx, string, string, notabledays.EventPatch) error
	Load(context.Context, *sql.Tx, string, *notabledays.Event) error
	Validate(context.Context, *sql.Tx, []notabledays.Event) error
	Remove(context.Context, *sql.Tx, string) error
	Ready(context.Context) error
}

func (r *Repository) WithMedia(m EventMediaStore) *Repository { r.media = m; return r }

func normalizeMediaPatch(p notabledays.EventPatch, configured bool) (notabledays.EventPatch, error) {
	if !configured && (p.IconID != nil || p.BackgroundID != nil || p.IconPresent || p.BackgroundPresent) {
		return p, notabledays.ErrUnsupported
	}
	for _, dst := range []**string{&p.IconID, &p.BackgroundID} {
		if *dst == nil {
			continue
		}
		if !authority.ValidID(strings.ToLower(**dst)) {
			return p, notabledays.ErrInvalid
		}
		value := strings.ToLower(**dst)
		*dst = &value
	}
	return p, nil
}

// ReadableEventsSQL is the shared current audience query for internal composed
// readers. It fixes aliases e/a and reserves $1 for the viewer (empty is anonymous).
// Parent adapters add their reference predicate to this same SQL statement.
const ReadableEventsSQL = eventBase

const activeCalendarOwner = `NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=statement_timestamp())AND(p.expires_at IS NULL OR p.expires_at>statement_timestamp()))`

// CanReadEventInTransaction is the common current calendar audience policy for
// attachment projection and canonical media byte authorization. Knowing an ID,
// subscribing, or having read a file before does not grant lasting access.
func CanReadEventInTransaction(ctx context.Context, tx *sql.Tx, viewer, id string) (bool, error) {
	if tx == nil {
		return false, notabledays.ErrUnavailable
	}
	if !authority.ValidID(id) || viewer != "" && !authority.ValidID(viewer) {
		return false, nil
	}
	var allowed bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1`+eventBase+` AND e.id=$2::uuid)`, viewer, id).Scan(&allowed)
	return allowed, e
}
