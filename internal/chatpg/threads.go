// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
)

// Thread membership is immutable and distinct from an ordinary quoted reply.
func (r *Repository) validateThread(ctx context.Context, tx *sql.Tx, rid string, p rooms.TextRequest) error {
	if p.ThreadID == nil {
		return nil
	}
	var rootReply, rootThread *string
	var encryption []byte
	e := tx.QueryRowContext(ctx, `SELECT replied_message_id::text,thread_id::text,encryption_meta FROM chat_messages WHERE id=$1 AND chat_room_id=$2 AND type='text' AND deleted_at IS NULL AND EXISTS(SELECT 1 FROM chat_members sender WHERE sender.id=chat_messages.sender_id AND sender.chat_room_id=chat_messages.chat_room_id AND sender.deleted_at IS NULL) FOR SHARE`, *p.ThreadID, rid).Scan(&rootReply, &rootThread, &encryption)
	if e != nil {
		return rooms.ErrNotFound
	}
	if rootReply != nil || rootThread != nil {
		return rooms.ErrInvalid
	}
	if len(encryption) > 0 && string(encryption) != "null" {
		return rooms.ErrUnsupported
	}
	if p.RepliedMessageID != nil && *p.RepliedMessageID != *p.ThreadID {
		var thread *string
		e = tx.QueryRowContext(ctx, `SELECT thread_id::text FROM chat_messages WHERE id=$1 AND chat_room_id=$2 AND type='text' AND deleted_at IS NULL AND EXISTS(SELECT 1 FROM chat_members sender WHERE sender.id=chat_messages.sender_id AND sender.chat_room_id=chat_messages.chat_room_id AND sender.deleted_at IS NULL) FOR SHARE`, *p.RepliedMessageID, rid).Scan(&thread)
		if e != nil {
			return rooms.ErrNotFound
		}
		if thread == nil || *thread != *p.ThreadID {
			return rooms.ErrInvalid
		}
		var depth int
		e = tx.QueryRowContext(ctx, `WITH RECURSIVE ancestors AS(SELECT id,replied_message_id,0 AS depth,ARRAY[id] AS visited FROM chat_messages WHERE id=$1 UNION ALL SELECT p.id,p.replied_message_id,a.depth+1,a.visited||p.id FROM ancestors a JOIN chat_messages p ON p.id=a.replied_message_id WHERE p.chat_room_id=$2 AND p.thread_id=$3 AND a.depth<32 AND NOT p.id=ANY(a.visited)) SELECT coalesce(max(depth),0) FROM ancestors`, *p.RepliedMessageID, rid, *p.ThreadID).Scan(&depth)
		if e != nil {
			return e
		}
		if depth >= 32 {
			return rooms.ErrInvalid
		}
	}
	return nil
}
func (r *Repository) Thread(ctx context.Context, actor, rid, root string, take, offset int) (out rooms.ThreadPage, e error) {
	out.Replies = []rooms.ThreadReply{}
	if valid(rid, root) != nil || page(take, offset) != nil || offset > 100000 {
		return out, rooms.ErrInvalid
	}
	var tx *sql.Tx
	if actor == "" {
		tx, e = r.begin(ctx, false)
	} else {
		tx, e = r.beginRoomWrite(ctx, actor, rid, "", "")
	}
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	room, e := r.room(ctx, tx, rid, true)
	if e != nil {
		return out, e
	}
	if room.EncryptionMode != r.policy.NoneEncryptionMode {
		return out, rooms.ErrUnsupported
	}
	if e = r.readable(ctx, tx, room, actor); e != nil {
		return out, e
	}
	out.Root, e = textMessage(tx.QueryRowContext(ctx, `SELECT `+textColumns+` FROM chat_messages WHERE id=$1 AND chat_room_id=$2 AND type='text' AND deleted_at IS NULL AND thread_id IS NULL AND replied_message_id IS NULL AND EXISTS(SELECT 1 FROM chat_members sender WHERE sender.id=chat_messages.sender_id AND sender.chat_room_id=chat_messages.chat_room_id AND sender.deleted_at IS NULL)`, root, rid))
	if e != nil {
		return out, e
	}
	if len(out.Root.EncryptionMeta) > 0 && string(out.Root.EncryptionMeta) != "null" {
		return out, rooms.ErrUnsupported
	}
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM chat_messages WHERE chat_room_id=$1 AND thread_id=$2 AND type='text' AND deleted_at IS NULL AND (encryption_meta IS NULL OR encryption_meta='null'::jsonb) AND EXISTS(SELECT 1 FROM chat_members sender WHERE sender.id=chat_messages.sender_id AND sender.chat_room_id=chat_messages.chat_room_id AND sender.deleted_at IS NULL)`, rid, root).Scan(&out.Total); e != nil {
		return out, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+textColumns+` FROM chat_messages WHERE chat_room_id=$1 AND thread_id=$2 AND type='text' AND deleted_at IS NULL AND (encryption_meta IS NULL OR encryption_meta='null'::jsonb) AND EXISTS(SELECT 1 FROM chat_members sender WHERE sender.id=chat_messages.sender_id AND sender.chat_room_id=chat_messages.chat_room_id AND sender.deleted_at IS NULL) ORDER BY room_sequence DESC,id LIMIT $3 OFFSET $4`, rid, root, take, offset)
	if e != nil {
		return out, e
	}
	ids := []string{}
	for rows.Next() {
		v, err := textMessage(rows)
		if err != nil {
			rows.Close()
			return out, err
		}
		out.Replies = append(out.Replies, rooms.ThreadReply{Message: v})
		ids = append(ids, v.ID)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	depths := map[string]int{}
	if len(ids) > 0 {
		rows, e = tx.QueryContext(ctx, `WITH RECURSIVE ancestry AS(SELECT id AS child,id,replied_message_id AS parent,0 AS depth,ARRAY[id] AS visited FROM chat_messages WHERE id=ANY($1::uuid[]) UNION ALL SELECT a.child,p.id,p.replied_message_id,a.depth+1,a.visited||p.id FROM ancestry a JOIN chat_messages p ON p.id=a.parent WHERE p.chat_room_id=$2 AND p.thread_id=$3 AND p.type='text' AND a.depth<32 AND NOT p.id=ANY(a.visited))SELECT child::text,max(depth) FROM ancestry GROUP BY child`, ids, rid, root)
		if e != nil {
			return out, e
		}
		for rows.Next() {
			var id string
			var depth int
			if e = rows.Scan(&id, &depth); e != nil {
				rows.Close()
				return out, e
			}
			depths[id] = depth
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return out, e
		}
	}
	if e = r.loadMessage(ctx, tx, actor, &out.Root); e != nil {
		return out, e
	}
	out.Root.IsThreadRoot = out.Total > 0
	if out.Total > 2147483647 {
		return out, rooms.ErrUnavailable
	}
	out.Root.ThreadRepliesCount = int32(out.Total)
	for i := range out.Replies {
		out.Replies[i].Depth = depths[out.Replies[i].Message.ID]
		if e = r.loadMessage(ctx, tx, actor, &out.Replies[i].Message); e != nil {
			return rooms.ThreadPage{}, e
		}
		out.Replies[i].Message.Content = messager.FormatMessageContent(out.Replies[i].Message.Content, false)
	}
	out.Root.Content = messager.FormatMessageContent(out.Root.Content, false)
	if e = r.readable(ctx, tx, room, actor); e != nil {
		return rooms.ThreadPage{}, e
	}
	if actor == "" {
		e = tx.Commit()
	} else {
		e = r.commit(ctx, tx, actor, "")
	}
	if e != nil {
		return rooms.ThreadPage{}, e
	}
	return
}
func sameOptional(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
