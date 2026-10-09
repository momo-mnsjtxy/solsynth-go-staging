// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/messager/rooms"
)

const reactionColumns = `id::text,message_id::text,sender_id::text,symbol,owned_attitude,owned_created_at,owned_updated_at,deleted_at`

func scanReaction(s scanner) (v rooms.Reaction, e error) {
	e = s.Scan(&v.ID, &v.MessageID, &v.SenderID, &v.Symbol, &v.Attitude, &v.CreatedAt, &v.UpdatedAt, &v.DeletedAt)
	return
}
func (r *Repository) React(ctx context.Context, actor, rid, mid string, p rooms.ReactionRequest) (*rooms.Reaction, error) {
	return r.changeReaction(ctx, actor, rid, mid, p, false)
}
func (r *Repository) RemoveReaction(ctx context.Context, actor, rid, mid, symbol string) error {
	_, e := r.changeReaction(ctx, actor, rid, mid, rooms.ReactionRequest{Symbol: symbol}, true)
	return e
}
func (r *Repository) changeReaction(ctx context.Context, actor, rid, mid string, p rooms.ReactionRequest, remove bool) (*rooms.Reaction, error) {
	if e := valid(actor, rid, mid); e != nil {
		return nil, e
	}
	if !rooms.ValidReaction(p.Symbol) {
		return nil, rooms.ErrUnsupported
	}
	tx, e := r.beginRoomWrite(ctx, actor, rid, "chat.messages.react", "")
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	m, e := r.writable(ctx, tx, actor, rid)
	if e != nil {
		return nil, e
	}
	var kind string
	e = tx.QueryRowContext(ctx, `SELECT type FROM chat_messages WHERE id=$1 AND chat_room_id=$2 AND deleted_at IS NULL`, mid, rid).Scan(&kind)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, rooms.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	if kind != "text" {
		return nil, rooms.ErrUnsupported
	}
	var exists bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_reactions WHERE message_id=$1 AND sender_id=$2 AND symbol=$3 AND deleted_at IS NULL)`, mid, m.ID, p.Symbol).Scan(&exists); e != nil {
		return nil, e
	}
	if !exists && remove {
		return nil, r.commit(ctx, tx, actor, "chat.messages.react")
	}
	now, e := timestamp(ctx, tx)
	if e != nil {
		return nil, e
	}
	var result *rooms.Reaction
	eventType := "messages.reaction.removed"
	meta := map[string]any{"message_id": mid, "symbol": p.Symbol}
	if exists {
		_, e = tx.ExecContext(ctx, `UPDATE chat_reactions SET deleted_at=$4,owned_updated_at=$4 WHERE message_id=$1 AND sender_id=$2 AND symbol=$3 AND deleted_at IS NULL`, mid, m.ID, p.Symbol, now)
	} else {
		reactionID, x := id()
		if x != nil {
			return nil, x
		}
		v, x := scanReaction(tx.QueryRowContext(ctx, `INSERT INTO chat_reactions(id,message_id,sender_id,symbol,owned_attitude,owned_created_at,owned_updated_at)VALUES($1,$2,$3,$4,$5,$6,$6)RETURNING `+reactionColumns, reactionID, mid, m.ID, p.Symbol, p.Attitude, now))
		e = x
		result = &v
		result.Sender = &m
		eventType = "messages.reaction.added"
		meta["reaction"] = map[string]any{"id": v.ID, "symbol": v.Symbol, "attitude": v.Attitude, "message_id": mid, "sender_id": m.ID}
	}
	if e != nil {
		return nil, e
	}
	var counts []byte
	e = tx.QueryRowContext(ctx, `UPDATE chat_messages SET reactions_count=(SELECT coalesce(jsonb_object_agg(symbol,total),'{}') FROM(SELECT symbol,count(*) AS total FROM chat_reactions WHERE message_id=$1 AND deleted_at IS NULL GROUP BY symbol) counts) WHERE id=$1 RETURNING reactions_count`, mid).Scan(&counts)
	if e != nil {
		return nil, e
	}
	meta["reactions_count"] = json.RawMessage(counts)
	encoded, e := json.Marshal(meta)
	if e != nil {
		return nil, e
	}
	if _, e = appendMessage(ctx, tx, rid, m.ID, eventType, nil, nil, nil, encoded, nil, nil); e != nil {
		return nil, e
	}
	return result, r.commit(ctx, tx, actor, "chat.messages.react")
}
func (r *Repository) ListReactions(ctx context.Context, actor, rid, mid string, q rooms.ReactionQuery) ([]rooms.Reaction, int, error) {
	if e := valid(rid, mid); e != nil {
		return nil, 0, e
	}
	if actor != "" {
		if e := valid(actor); e != nil {
			return nil, 0, e
		}
	}
	if page(q.Take, q.Offset) != nil || q.Symbol != "" && !rooms.ValidReaction(q.Symbol) || q.Order != "" && q.Order != "created" {
		return nil, 0, rooms.ErrInvalid
	}
	tx, e := r.begin(ctx, true)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	if actor != "" {
		if e = account(ctx, tx, actor); e != nil {
			return nil, 0, e
		}
	}
	room, e := r.room(ctx, tx, rid, false)
	if e != nil {
		return nil, 0, e
	}
	if e = r.readable(ctx, tx, room, actor); e != nil {
		return nil, 0, e
	}
	var exists bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_messages WHERE id=$1 AND chat_room_id=$2 AND deleted_at IS NULL AND type='text')`, mid, rid).Scan(&exists); e != nil {
		return nil, 0, e
	}
	if !exists {
		return nil, 0, rooms.ErrNotFound
	}
	const filter = `message_id=$1 AND deleted_at IS NULL AND ($2='' OR symbol=$2)`
	var total int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM chat_reactions WHERE `+filter, mid, q.Symbol).Scan(&total); e != nil {
		return nil, 0, e
	}
	order := `symbol,owned_created_at DESC,id`
	if q.Order == "created" {
		order = `owned_created_at DESC,id`
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+reactionColumns+` FROM chat_reactions WHERE `+filter+` ORDER BY `+order+` LIMIT $3 OFFSET $4`, mid, q.Symbol, q.Take, q.Offset)
	if e != nil {
		return nil, 0, e
	}
	out := []rooms.Reaction{}
	for rows.Next() {
		v, x := scanReaction(rows)
		if x != nil {
			rows.Close()
			return nil, 0, x
		}
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, 0, e
	}
	for i := range out {
		m, x := scanMember(tx.QueryRowContext(ctx, `SELECT `+memberColumns+` FROM chat_members WHERE id=$1 AND chat_room_id=$2 AND deleted_at IS NULL`, out[i].SenderID, rid))
		if x == nil {
			if m.AccountID != actor {
				m.LastReadAt = nil
			}
			if r.social != nil {
				a, err := canonicalAccount(ctx, tx, m.AccountID)
				if err == nil {
					m.Account = a
				} else if !errors.Is(err, sql.ErrNoRows) {
					return nil, 0, err
				}
			}
			out[i].Sender = &m
		} else if !errors.Is(x, rooms.ErrNotFound) {
			return nil, 0, x
		}
	}
	return out, total, tx.Commit()
}
