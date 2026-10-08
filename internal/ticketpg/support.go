// SPDX-License-Identifier: AGPL-3.0-only
package ticketpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/tickets"
	"strings"
	"time"
)

const ticketFields = `t.id::text,t.creator_id::text,t.assignee_id::text,t.title,t.type,t.priority,t.status,t.resources,t.created_at,t.updated_at,t.resolved_at,t.deleted_at`

func scanTicket(s interface{ Scan(...any) error }) (t tickets.Ticket, e error) {
	var b []byte
	e = s.Scan(&t.ID, &t.CreatorID, &t.AssigneeID, &t.Title, &t.Type, &t.Priority, &t.Status, &b, &t.CreatedAt, &t.UpdatedAt, &t.ResolvedAt, &t.DeletedAt)
	if e == nil {
		e = json.Unmarshal(b, &t.Resources)
	}
	t.Messages = []tickets.Message{}
	return t, mapped(e)
}
func loadTicket(ctx context.Context, tx *sql.Tx, id string, lock bool) (tickets.Ticket, error) {
	if !authority.ValidID(id) {
		return tickets.Ticket{}, tickets.ErrNotFound
	}
	q := `SELECT ` + ticketFields + ` FROM owned_ticket_cases t WHERE t.id=$1 AND t.deleted_at IS NULL AND EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id=t.creator_id AND NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=statement_timestamp())AND(p.expires_at IS NULL OR p.expires_at>statement_timestamp())))`
	if lock {
		q += ` FOR UPDATE OF t`
	}
	t, e := scanTicket(tx.QueryRowContext(ctx, q, id))
	if e != nil {
		return t, e
	}
	if _, e = account(ctx, tx, t.CreatorID); e != nil {
		return tickets.Ticket{}, e
	}
	return t, nil
}
func (r *Repository) hydrateTicket(ctx context.Context, tx *sql.Tx, actor string, t *tickets.Ticket) error {
	var e error
	t.Creator, e = account(ctx, tx, t.CreatorID)
	if e != nil {
		return e
	}
	if t.AssigneeID != nil {
		a, e := account(ctx, tx, *t.AssigneeID)
		if e == nil {
			t.Assignee = &a
		} else if !errors.Is(e, tickets.ErrNotFound) {
			return e
		}
	}
	rows, e := tx.QueryContext(ctx, `SELECT m.id::text,m.ticket_id::text,m.sender_id::text,m.content,m.created_at,a.name,a.nick FROM owned_ticket_messages m JOIN owned_authority_accounts a ON a.id=m.sender_id WHERE m.ticket_id=$1 ORDER BY m.created_at,m.id LIMIT 200`, t.ID)
	if e != nil {
		return e
	}
	for rows.Next() {
		var m tickets.Message
		if e = rows.Scan(&m.ID, &m.TicketID, &m.SenderID, &m.Content, &m.CreatedAt, &m.Sender.Name, &m.Sender.Nick); e != nil {
			rows.Close()
			return e
		}
		m.Sender.ID = m.SenderID
		m.Files = []any{}
		t.Messages = append(t.Messages, m)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if r.media != nil {
		for i := range t.Messages {
			if e = r.media.Load(ctx, tx, actor, &t.Messages[i]); e != nil {
				return e
			}
		}
	}
	t.MessagesTotal = len(t.Messages)
	return nil
}
func (r *Repository) support(ctx context.Context, tx *sql.Tx, actor string, c tickets.Command, now time.Time) (out tickets.Result, e error) {
	admin := false
	if c.Operation != "ticket.create" && !strings.HasPrefix(c.Operation, "oncall.") {
		admin, e = r.isAdmin(ctx, tx, actor)
		if e != nil {
			return out, e
		}
	}
	if strings.HasPrefix(c.Operation, "oncall.") {
		if c.Operation == "oncall.list" {
			rows, e := tx.QueryContext(ctx, `SELECT o.id::text,o.account_id::text,o.created_at,a.name,a.nick FROM owned_ticket_on_call o JOIN owned_authority_accounts a ON a.id=o.account_id WHERE NOT a.disabled ORDER BY o.created_at,o.id LIMIT 100`)
			if e != nil {
				return out, e
			}
			defer rows.Close()
			v := []tickets.OnCall{}
			for rows.Next() {
				var x tickets.OnCall
				if e = rows.Scan(&x.ID, &x.AccountID, &x.CreatedAt, &x.Account.Name, &x.Account.Nick); e != nil {
					return out, e
				}
				x.Account.ID = x.AccountID
				v = append(v, x)
			}
			out.Value = v
			return out, rows.Err()
		}
		if c.AccountID == nil || !authority.ValidID(*c.AccountID) {
			return out, tickets.ErrInvalid
		}
		if _, e = tx.ExecContext(ctx, `LOCK TABLE owned_ticket_on_call IN SHARE ROW EXCLUSIVE MODE`); e != nil {
			return out, e
		}
		if c.Operation == "oncall.remove" {
			result, e := tx.ExecContext(ctx, `DELETE FROM owned_ticket_on_call WHERE account_id=$1`, *c.AccountID)
			if e != nil {
				return out, e
			}
			n, _ := result.RowsAffected()
			out.Changed = n > 0
			out.ResourceID = *c.AccountID
			return out, nil
		}
		a, e := account(ctx, tx, *c.AccountID)
		if e != nil {
			return out, e
		}
		id, e := uuid()
		if e != nil {
			return out, e
		}
		var prior tickets.OnCall
		lookup := tx.QueryRowContext(ctx, `SELECT id::text,account_id::text,created_at FROM owned_ticket_on_call WHERE account_id=$1`, a.ID).Scan(&prior.ID, &prior.AccountID, &prior.CreatedAt)
		if lookup == nil {
			prior.Account = a
			out.Value = prior
			return out, nil
		}
		if !errors.Is(lookup, sql.ErrNoRows) {
			return out, lookup
		}
		var count int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_ticket_on_call`).Scan(&count); e != nil {
			return out, e
		}
		if count >= 100 {
			return out, tickets.ErrLimit
		}
		x := tickets.OnCall{Account: a}
		e = tx.QueryRowContext(ctx, `INSERT INTO owned_ticket_on_call(id,account_id,created_at)VALUES($1,$2,$3) ON CONFLICT(account_id)DO UPDATE SET account_id=excluded.account_id RETURNING id::text,account_id::text,created_at`, id, a.ID, now).Scan(&x.ID, &x.AccountID, &x.CreatedAt)
		out.Value = x
		out.Changed = e == nil && x.ID == id
		out.ResourceID = x.ID
		return out, e
	}
	if c.Operation == "ticket.create" {
		if c.TicketCreate == nil {
			return out, tickets.ErrInvalid
		}
		v := *c.TicketCreate
		if e = tickets.ValidateTicket(v); e != nil {
			return out, e
		}
		hash, e := digest(v)
		if e != nil {
			return out, e
		}
		if c.IdempotencyKey != "" {
			var id string
			var prior sql.NullString
			e = tx.QueryRowContext(ctx, `SELECT id::text,request_hash FROM owned_ticket_cases WHERE creator_id=$1 AND request_key=$2`, actor, c.IdempotencyKey).Scan(&id, &prior)
			if e == nil {
				if !prior.Valid || prior.String != hash {
					return out, tickets.ErrConflict
				}
				t, e := loadTicket(ctx, tx, id, false)
				if e != nil {
					return out, e
				}
				e = r.hydrateTicket(ctx, tx, actor, &t)
				out.Value = t
				return out, e
			}
			if !errors.Is(e, sql.ErrNoRows) {
				return out, e
			}
		}
		id, e := uuid()
		if e != nil {
			return out, e
		}
		resources := v.Resources
		if resources == nil {
			resources = []*string{}
		}
		b, e := marshal(resources)
		if e != nil {
			return out, e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_ticket_cases(id,creator_id,title,type,priority,status,resources,created_at,updated_at,request_key,request_hash)VALUES($1,$2,$3,$4,$5,0,$6,$7,$7,$8,$9)`, id, actor, v.Title, v.Type, v.Priority, b, now, nullable(c.IdempotencyKey), hash)
		if e != nil {
			return out, e
		}
		if _, e = r.addMessage(ctx, tx, actor, id, v.Content, v.FileIDs, "", now); e != nil {
			return out, e
		}
		t, e := loadTicket(ctx, tx, id, false)
		if e != nil {
			return out, e
		}
		e = r.hydrateTicket(ctx, tx, actor, &t)
		out.Value = t
		out.Changed = true
		out.ResourceID = id
		return out, e
	}
	if c.Operation == "ticket.list" || c.Operation == "ticket.me" || c.Operation == "ticket.count" {
		q := c.Query
		if !tickets.ValidQuery(q) {
			return out, tickets.ErrInvalid
		}
		if c.Operation == "ticket.me" {
			q.CreatorID = actor
			q.AssigneeID = ""
		}
		scoped := (q.CreatorID != "" || q.AssigneeID != "") && (q.CreatorID == "" || q.CreatorID == actor) && (q.AssigneeID == "" || q.AssigneeID == actor)
		if !admin && !scoped {
			return out, tickets.ErrForbidden
		}
		for _, p := range []*int{q.Type, q.Status, q.Priority} {
			if p != nil && (*p < 0 || *p > 3) {
				return out, tickets.ErrUnsupported
			}
		}
		where := ` FROM owned_ticket_cases t WHERE t.deleted_at IS NULL AND EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id=t.creator_id AND NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=statement_timestamp())AND(p.expires_at IS NULL OR p.expires_at>statement_timestamp()))) AND ($1::text='' OR t.creator_id=NULLIF($1::text,'')::uuid) AND ($2::text='' OR t.assignee_id=NULLIF($2::text,'')::uuid) AND ($3::integer IS NULL OR t.type=$3) AND ($4::integer IS NULL OR t.status=$4) AND ($5::integer IS NULL OR t.priority=$5)`
		var total int64
		if e = tx.QueryRowContext(ctx, `SELECT count(*)`+where, q.CreatorID, q.AssigneeID, q.Type, q.Status, q.Priority).Scan(&total); e != nil {
			return out, e
		}
		out.Total = &total
		if c.Operation == "ticket.count" {
			out.Value = map[string]int64{"count": total}
			return out, nil
		}
		rows, e := tx.QueryContext(ctx, `SELECT `+ticketFields+where+` ORDER BY t.created_at DESC,t.priority DESC,t.id LIMIT $6 OFFSET $7`, q.CreatorID, q.AssigneeID, q.Type, q.Status, q.Priority, q.Take, q.Offset)
		if e != nil {
			return out, e
		}
		items := []tickets.Ticket{}
		for rows.Next() {
			v, e := scanTicket(rows)
			if e != nil {
				rows.Close()
				return out, e
			}
			items = append(items, v)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return out, e
		}
		for i := range items {
			if e = r.hydrateTicket(ctx, tx, actor, &items[i]); e != nil {
				return out, e
			}
		}
		out.Value = items
		return out, nil
	}
	t, e := loadTicket(ctx, tx, c.ID, c.Operation != "ticket.get")
	if e != nil {
		return out, e
	}
	if c.Operation == "ticket.get" {
		if !admin && t.CreatorID != actor && (t.AssigneeID == nil || *t.AssigneeID != actor) {
			return out, tickets.ErrNotFound
		}
		e = r.hydrateTicket(ctx, tx, actor, &t)
		out.Value = t
		return out, e
	}
	if c.Operation == "ticket.message" {
		if !admin && t.CreatorID != actor {
			return out, tickets.ErrForbidden
		}
		if c.Message == nil {
			return out, tickets.ErrInvalid
		}
		if !tickets.ValidFileIDs(c.Message.FileIDs) || !tickets.Text(c.Message.Content, 1, 16384) {
			return out, tickets.ErrInvalid
		}
		m, e := r.addMessage(ctx, tx, actor, t.ID, c.Message.Content, c.Message.FileIDs, c.IdempotencyKey, now)
		out.Value = m
		out.Changed = e == nil && !m.Replayed
		out.ResourceID = m.ID
		return out, e
	}
	if !admin {
		return out, tickets.ErrForbidden
	}
	switch c.Operation {
	case "ticket.delete":
		if r.media != nil {
			if e = r.media.RemoveTicket(ctx, tx, t.ID); e != nil {
				return out, e
			}
		}
		_, e = tx.ExecContext(ctx, `UPDATE owned_ticket_cases SET deleted_at=$2,updated_at=$2 WHERE id=$1`, t.ID, now)
		out.Changed = e == nil
		out.ResourceID = t.ID
		return out, e
	case "ticket.update":
		v := c.TicketUpdate
		if v == nil {
			return out, tickets.ErrInvalid
		}
		if v.Title != nil {
			if !tickets.Text(*v.Title, 3, 256) {
				return out, tickets.ErrInvalid
			}
			t.Title = *v.Title
		}
		if v.Type != nil {
			if *v.Type < 0 || *v.Type > 3 {
				return out, tickets.ErrUnsupported
			}
			t.Type = *v.Type
		}
		if v.Priority != nil {
			if *v.Priority < 0 || *v.Priority > 3 {
				return out, tickets.ErrUnsupported
			}
			t.Priority = *v.Priority
		}
		if v.Resources != nil {
			if !tickets.ValidResources(*v.Resources) {
				return out, tickets.ErrInvalid
			}
			t.Resources = *v.Resources
		}
	case "ticket.status":
		if c.State == nil || *c.State < 0 || *c.State > 3 {
			return out, tickets.ErrUnsupported
		}
		if t.Status != *c.State {
			t.Status = *c.State
			if t.Status == tickets.Resolved || t.Status == tickets.Closed {
				t.ResolvedAt = &now
			}
		}
	case "ticket.assign":
		if c.AccountID != nil {
			if !authority.ValidID(*c.AccountID) {
				return out, tickets.ErrInvalid
			}
			if _, e = account(ctx, tx, *c.AccountID); e != nil {
				return out, e
			}
		}
		t.AssigneeID = c.AccountID
	default:
		return out, tickets.ErrUnsupported
	}
	if t.Resources == nil {
		t.Resources = []*string{}
	}
	b, e := marshal(t.Resources)
	if e != nil {
		return out, e
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_ticket_cases SET title=$2,type=$3,priority=$4,status=$5,resources=$6,assignee_id=$7,resolved_at=$8,updated_at=$9 WHERE id=$1`, t.ID, t.Title, t.Type, t.Priority, t.Status, b, t.AssigneeID, t.ResolvedAt, now)
	if e != nil {
		return out, e
	}
	t.UpdatedAt = now
	e = r.hydrateTicket(ctx, tx, actor, &t)
	out.Value = t
	out.ResourceID = t.ID
	out.Changed = true
	return out, e
}
func (r *Repository) addMessage(ctx context.Context, tx *sql.Tx, actor, id, content string, files []string, key string, now time.Time) (m tickets.Message, e error) {
	if len(files) > 0 && r.media == nil {
		return m, tickets.ErrUnsupported
	}
	var identity any = content
	if len(files) > 0 {
		identity = struct {
			Content string
			Files   []string
		}{content, files}
	}
	hash, e := digest(identity)
	if e != nil {
		return m, e
	}
	if key != "" {
		var prior sql.NullString
		e = tx.QueryRowContext(ctx, `SELECT id::text,content,created_at,request_hash FROM owned_ticket_messages WHERE sender_id=$1 AND ticket_id=$2 AND request_key=$3`, actor, id, key).Scan(&m.ID, &m.Content, &m.CreatedAt, &prior)
		if e == nil {
			if !prior.Valid || prior.String != hash {
				return m, tickets.ErrConflict
			}
			m.Replayed = true
			m.TicketID = id
			m.SenderID = actor
			m.Files = []any{}
			m.Sender, e = account(ctx, tx, actor)
			if e == nil && r.media != nil {
				e = r.media.Load(ctx, tx, actor, &m)
			}
			return m, e
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return m, e
		}
	}
	var n int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_ticket_messages WHERE ticket_id=$1`, id).Scan(&n); e != nil {
		return m, e
	}
	if n >= 200 {
		return m, tickets.ErrLimit
	}
	m.ID, e = uuid()
	if e != nil {
		return m, e
	}
	m.TicketID = id
	m.SenderID = actor
	m.Content = content
	m.CreatedAt = now
	m.Files = []any{}
	m.Sender, e = account(ctx, tx, actor)
	if e != nil {
		return m, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_ticket_messages(id,ticket_id,sender_id,content,created_at,request_key,request_hash)VALUES($1,$2,$3,$4,$5,$6,$7)`, m.ID, id, actor, content, now, nullable(key), hash)
	if e != nil {
		return m, e
	}
	if len(files) > 0 {
		if e = r.media.Apply(ctx, tx, actor, m.ID, files); e != nil {
			return m, e
		}
	}
	if r.media != nil {
		if e = r.media.Load(ctx, tx, actor, &m); e != nil {
			return m, e
		}
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_ticket_cases SET updated_at=$2 WHERE id=$1`, id, now)
	return m, e
}
