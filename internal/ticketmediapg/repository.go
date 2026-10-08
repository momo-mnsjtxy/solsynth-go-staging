// SPDX-License-Identifier: AGPL-3.0-only
package ticketmediapg

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"sort"
	"strings"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/tickets"
)

//go:embed schema.sql
var Schema string

type TicketAccess interface {
	CanReadTicketInTransaction(context.Context, *sql.Tx, string, string) (bool, error)
}
type Repository struct {
	db     *sql.DB
	access TicketAccess
}

func New(db *sql.DB, access TicketAccess) *Repository { return &Repository{db: db, access: access} }
func (r *Repository) Ready(ctx context.Context) error {
	if r == nil || r.db == nil || r.access == nil {
		return tickets.ErrUnavailable
	}
	_, e := r.db.ExecContext(ctx, `SELECT message_id,file_id,position FROM owned_ticket_media LIMIT 0; SELECT id,ticket_id,sender_id FROM owned_ticket_messages LIMIT 0`)
	return e
}

const fileFields = `f.id::text,f.account_id::text,f.name,f.mime_type,f.size,f.sha256,f.is_public,f.state,f.created_at,f.updated_at,f.usage`
const activeOwner = `NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=statement_timestamp())AND(p.expires_at IS NULL OR p.expires_at>statement_timestamp()))`

func scan(row interface{ Scan(...any) error }) (f media.File, e error) {
	e = row.Scan(&f.ID, &f.AccountID, &f.Name, &f.MIMEType, &f.Size, &f.SHA256, &f.Public, &f.State, &f.CreatedAt, &f.UpdatedAt, &f.Usage)
	return
}

// Apply only accepts files actually owned by the message author. Read grants,
// assignment and operator permission never become redistribution authority.
func (r *Repository) Apply(ctx context.Context, tx *sql.Tx, actor, message string, ids []string) error {
	if r == nil || r.db == nil || r.access == nil || tx == nil {
		return tickets.ErrUnavailable
	}
	if !authority.ValidID(actor) || !authority.ValidID(message) || !tickets.ValidFileIDs(ids) {
		return tickets.ErrInvalid
	}
	ordered := append([]string(nil), ids...)
	for i := range ordered {
		ordered[i] = strings.ToLower(ordered[i])
	}
	sort.Strings(ordered)
	for _, id := range ordered {
		f, e := scan(tx.QueryRowContext(ctx, `SELECT `+fileFields+` FROM owned_media_files f WHERE f.id=$1::uuid FOR SHARE`, id))
		if errors.Is(e, sql.ErrNoRows) {
			return tickets.ErrNotFound
		}
		if e != nil {
			return e
		}
		if f.AccountID != actor {
			return tickets.ErrForbidden
		}
		if f.State != "ready" {
			return tickets.ErrNotFound
		}
		var active bool
		if e = tx.QueryRowContext(ctx, `SELECT `+activeOwner+` FROM owned_authority_accounts a WHERE a.id=$1::uuid`, actor).Scan(&active); e != nil {
			return e
		}
		if !active {
			return tickets.ErrNotFound
		}
	}
	for i, id := range ids {
		if _, e := tx.ExecContext(ctx, `INSERT INTO owned_ticket_media(message_id,file_id,position)VALUES($1::uuid,$2::uuid,$3::integer)`, message, id, i); e != nil {
			return e
		}
	}
	return nil
}
func (r *Repository) Load(ctx context.Context, tx *sql.Tx, actor string, m *tickets.Message) error {
	if r == nil || r.db == nil || r.access == nil || tx == nil || m == nil {
		return tickets.ErrUnavailable
	}
	m.Files = []any{}
	allowed, e := r.access.CanReadTicketInTransaction(ctx, tx, actor, m.TicketID)
	if e != nil {
		return e
	}
	if !allowed {
		return tickets.ErrNotFound
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+fileFields+` FROM owned_ticket_media l JOIN owned_ticket_messages m ON m.id=l.message_id JOIN owned_media_files f ON f.id=l.file_id AND f.account_id=m.sender_id JOIN owned_authority_accounts a ON a.id=f.account_id WHERE l.message_id=$1::uuid AND f.state='ready' AND `+activeOwner+` ORDER BY l.position`, m.ID)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		f, e := scan(rows)
		if e != nil {
			return e
		}
		m.Files = append(m.Files, media.LegacyFile(f))
	}
	return rows.Err()
}
func (r *Repository) RemoveTicket(ctx context.Context, tx *sql.Tx, ticket string) error {
	if r == nil || tx == nil {
		return tickets.ErrUnavailable
	}
	_, e := tx.ExecContext(ctx, `DELETE FROM owned_ticket_media l USING owned_ticket_messages m WHERE l.message_id=m.id AND m.ticket_id=$1::uuid`, ticket)
	return e
}
func (r *Repository) CanReadInTransaction(ctx context.Context, tx *sql.Tx, actor, id string) (bool, error) {
	if r == nil || r.db == nil || r.access == nil || tx == nil {
		return false, media.ErrUnavailable
	}
	if actor == "" || !authority.ValidID(actor) || !authority.ValidID(id) {
		return false, nil
	}
	rows, e := tx.QueryContext(ctx, `SELECT DISTINCT m.ticket_id::text FROM owned_ticket_media l JOIN owned_ticket_messages m ON m.id=l.message_id JOIN owned_media_files f ON f.id=l.file_id AND f.account_id=m.sender_id JOIN owned_authority_accounts a ON a.id=f.account_id WHERE l.file_id=$1::uuid AND f.state='ready' AND `+activeOwner, id)
	if e != nil {
		return false, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return false, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return false, e
	}
	for _, ticket := range ids {
		allowed, e := r.access.CanReadTicketInTransaction(ctx, tx, actor, ticket)
		if e != nil || allowed {
			return allowed, e
		}
	}
	return false, nil
}
