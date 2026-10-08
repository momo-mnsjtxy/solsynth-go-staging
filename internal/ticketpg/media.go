// SPDX-License-Identifier: AGPL-3.0-only
package ticketpg

import (
	"context"
	"database/sql"
	"errors"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/tickets"
)

// MediaStore joins the existing ticket transaction. Canonical file ownership,
// quota and bytes remain in the media repository.
type MediaStore interface {
	Ready(context.Context) error
	Apply(context.Context, *sql.Tx, string, string, []string) error
	Load(context.Context, *sql.Tx, string, *tickets.Message) error
	RemoveTicket(context.Context, *sql.Tx, string) error
}

func (r *Repository) WithMedia(m MediaStore) *Repository { r.media = m; return r }

// CanReadTicketInTransaction is also used after canonical media's file lock.
// Preflight validates the original proof without adding a lock or transaction.
// The media caller already holds its actor lock; an asserted UUID is insufficient.
func (r *Repository) CanReadTicketInTransaction(ctx context.Context, tx *sql.Tx, actor, id string) (bool, error) {
	if r == nil || r.db == nil || r.guard == nil || tx == nil {
		return false, tickets.ErrUnavailable
	}
	if actor == "" {
		return false, nil
	}
	if _, e := r.guard.Preflight(ctx, r.db, tx, actor); e != nil {
		return false, e
	}
	t, e := loadTicket(ctx, tx, id, false)
	if errors.Is(e, tickets.ErrNotFound) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if t.CreatorID == actor || t.AssigneeID != nil && *t.AssigneeID == actor {
		return true, nil
	}
	_, e = r.guard.Preflight(ctx, r.db, tx, actor, "tickets.admin")
	if errors.Is(e, authority.ErrForbidden) {
		return false, nil
	}
	return e == nil, e
}

func (r *Repository) recheckTicket(ctx context.Context, tx *sql.Tx, actor string, c tickets.Command, permission string) error {
	if permission == "" {
		if _, e := r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
			return e
		}
	} else if _, e := r.guard.Authorize(ctx, r.db, tx, actor, permission); e != nil {
		return e
	}
	needAdmin := c.Operation == "ticket.update" || c.Operation == "ticket.delete" || c.Operation == "ticket.assign" || c.Operation == "ticket.status"
	if c.Operation == "ticket.message" {
		var creator string
		if e := tx.QueryRowContext(ctx, `SELECT creator_id::text FROM owned_ticket_cases WHERE id=$1::uuid AND deleted_at IS NULL`, c.ID).Scan(&creator); e != nil {
			return mapped(e)
		}
		needAdmin = creator != actor
	}
	if needAdmin {
		ok, e := r.isAdmin(ctx, tx, actor)
		if e != nil {
			return e
		}
		if !ok {
			return tickets.ErrForbidden
		}
	}
	if c.Operation == "ticket.get" {
		ok, e := r.CanReadTicketInTransaction(ctx, tx, actor, c.ID)
		if e != nil {
			return e
		}
		if !ok {
			return tickets.ErrNotFound
		}
	}
	return nil
}
