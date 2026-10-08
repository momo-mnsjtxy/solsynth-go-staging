// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"solsynth.local/go-migration/ports/messager/rooms"
	"time"
)

// Schema is explicitly installed by the operator/guarded disposable runner.
//
//go:embed schema.sql
var Schema string

type Repository struct {
	db     *sql.DB
	policy rooms.Policy
}

var _ rooms.Repository = (*Repository)(nil)

func New(db *sql.DB, p rooms.Policy) (*Repository, error) {
	if db == nil {
		return nil, rooms.ErrUnavailable
	}
	if e := p.Validate(); e != nil {
		return nil, e
	}
	return &Repository{db: db, policy: p}, nil
}
func id() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
func valid(ids ...string) error {
	for _, id := range ids {
		if !rooms.ValidID(id) {
			return rooms.ErrInvalid
		}
	}
	return nil
}
func (r *Repository) begin(ctx context.Context, readonly bool) (*sql.Tx, error) {
	if r == nil || r.db == nil {
		return nil, rooms.ErrUnavailable
	}
	isolation := sql.LevelReadCommitted
	if readonly {
		isolation = sql.LevelRepeatableRead
	}
	return r.db.BeginTx(ctx, &sql.TxOptions{Isolation: isolation, ReadOnly: readonly})
}
func account(ctx context.Context, tx *sql.Tx, actor string) error {
	if e := valid(actor); e != nil {
		return e
	}
	var active bool
	if e := tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1`, actor).Scan(&active); errors.Is(e, sql.ErrNoRows) {
		return rooms.ErrNotFound
	} else if e != nil {
		return e
	}
	if !active {
		return rooms.ErrForbidden
	}
	return nil
}

const roomColumns = `id::text,owned_owner_id::text,owned_name,owned_description,is_public,owned_is_community,encryption_mode,owned_created_at,owned_updated_at,deleted_at`

type scanner interface{ Scan(...any) error }

func scanRoom(s scanner) (rooms.Room, error) {
	var v rooms.Room
	e := s.Scan(&v.ID, &v.AccountID, &v.Name, &v.Description, &v.IsPublic, &v.IsCommunity, &v.EncryptionMode, &v.CreatedAt, &v.UpdatedAt, &v.DeletedAt)
	if errors.Is(e, sql.ErrNoRows) {
		e = rooms.ErrNotFound
	}
	return v, e
}
func (r *Repository) room(ctx context.Context, tx *sql.Tx, rid string, lock bool) (rooms.Room, error) {
	q := `SELECT ` + roomColumns + ` FROM chat_rooms WHERE id=$1 AND deleted_at IS NULL AND owned_owner_id IS NOT NULL`
	if lock {
		q += ` FOR UPDATE`
	}
	v, e := scanRoom(tx.QueryRowContext(ctx, q, rid))
	if e == nil && v.EncryptionMode != r.policy.NoneEncryptionMode {
		e = rooms.ErrUnsupported
	}
	return v, e
}

const memberColumns = `id::text,chat_room_id::text,account_id::text,notify,joined_at,leave_at,last_read_at`

func scanMember(s scanner) (rooms.Member, error) {
	var v rooms.Member
	e := s.Scan(&v.ID, &v.ChatRoomID, &v.AccountID, &v.Notify, &v.JoinedAt, &v.LeaveAt, &v.LastReadAt)
	if errors.Is(e, sql.ErrNoRows) {
		e = rooms.ErrNotFound
	}
	return v, e
}
func member(ctx context.Context, tx *sql.Tx, rid, actor string, active bool) (rooms.Member, error) {
	q := `SELECT ` + memberColumns + ` FROM chat_members WHERE chat_room_id=$1 AND account_id=$2 AND deleted_at IS NULL`
	if active {
		q += ` AND joined_at IS NOT NULL AND leave_at IS NULL AND NOT owned_banned`
	}
	return scanMember(tx.QueryRowContext(ctx, q, rid, actor))
}
func requireMember(ctx context.Context, tx *sql.Tx, rid, actor string) (rooms.Member, error) {
	m, e := member(ctx, tx, rid, actor, true)
	if errors.Is(e, rooms.ErrNotFound) {
		e = rooms.ErrForbidden
	}
	return m, e
}
func readable(ctx context.Context, tx *sql.Tx, v rooms.Room, actor string) error {
	if v.IsPublic {
		return nil
	}
	if actor == "" {
		return rooms.ErrForbidden
	}
	_, e := requireMember(ctx, tx, v.ID, actor)
	return e
}
func page(take, offset int) error {
	if take < 1 || take > 100 || offset < 0 || offset > 100000 {
		return rooms.ErrInvalid
	}
	return nil
}

func (r *Repository) Create(ctx context.Context, actor string, p rooms.RoomPatch) (rooms.Room, error) {
	if e := valid(actor); e != nil {
		return rooms.Room{}, e
	}
	if e := rooms.ValidateRoom(p, true, r.policy); e != nil {
		return rooms.Room{}, e
	}
	tx, e := r.begin(ctx, false)
	if e != nil {
		return rooms.Room{}, e
	}
	defer tx.Rollback()
	if e = account(ctx, tx, actor); e != nil {
		return rooms.Room{}, e
	}
	rid, e := id()
	if e != nil {
		return rooms.Room{}, e
	}
	mid, e := id()
	if e != nil {
		return rooms.Room{}, e
	}
	public, community := false, false
	description := ""
	if p.IsPublic != nil {
		public = *p.IsPublic
	}
	if p.IsCommunity != nil {
		community = *p.IsCommunity
	}
	if p.Description != nil {
		description = *p.Description
	}
	if community && !public {
		return rooms.Room{}, rooms.ErrInvalid
	}
	v, e := scanRoom(tx.QueryRowContext(ctx, `INSERT INTO chat_rooms(id,is_public,encryption_mode,owned_owner_id,owned_name,owned_description,owned_is_community) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING `+roomColumns, rid, public, r.policy.NoneEncryptionMode, actor, *p.Name, description, community))
	if e != nil {
		return v, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO chat_members(id,chat_room_id,account_id,notify,joined_at,last_read_at) VALUES($1,$2,$3,$4,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, mid, rid, actor, r.policy.NotifyAll)
	if e != nil {
		return v, e
	}
	e = tx.Commit()
	return v, e
}
func (r *Repository) Get(ctx context.Context, actor, rid string) (rooms.Room, error) {
	if e := valid(rid); e != nil {
		return rooms.Room{}, e
	}
	if actor != "" {
		if e := valid(actor); e != nil {
			return rooms.Room{}, e
		}
	}
	tx, e := r.begin(ctx, true)
	if e != nil {
		return rooms.Room{}, e
	}
	defer tx.Rollback()
	v, e := r.room(ctx, tx, rid, false)
	if e == nil {
		e = readable(ctx, tx, v, actor)
	}
	if e != nil {
		return rooms.Room{}, e
	}
	return v, tx.Commit()
}
func (r *Repository) List(ctx context.Context, actor string, take, offset int) ([]rooms.Room, error) {
	if e := valid(actor); e != nil {
		return nil, e
	}
	if e := page(take, offset); e != nil {
		return nil, e
	}
	tx, e := r.begin(ctx, true)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT `+roomColumns+` FROM chat_rooms WHERE owned_owner_id IS NOT NULL AND deleted_at IS NULL AND encryption_mode=$2 AND EXISTS(SELECT 1 FROM chat_members m WHERE m.chat_room_id=chat_rooms.id AND m.account_id=$1 AND m.joined_at IS NOT NULL AND m.leave_at IS NULL AND m.deleted_at IS NULL AND NOT m.owned_banned) ORDER BY owned_updated_at DESC,id LIMIT $3 OFFSET $4`, actor, r.policy.NoneEncryptionMode, take, offset)
	if e != nil {
		return nil, e
	}
	out := []rooms.Room{}
	for rows.Next() {
		v, e := scanRoom(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	return out, tx.Commit()
}
func (r *Repository) Update(ctx context.Context, actor, rid string, p rooms.RoomPatch) (rooms.Room, error) {
	if e := valid(actor, rid); e != nil {
		return rooms.Room{}, e
	}
	if e := rooms.ValidateRoom(p, false, r.policy); e != nil {
		return rooms.Room{}, e
	}
	tx, e := r.begin(ctx, false)
	if e != nil {
		return rooms.Room{}, e
	}
	defer tx.Rollback()
	v, e := r.room(ctx, tx, rid, true)
	if e != nil {
		return v, e
	}
	if v.AccountID != actor {
		return rooms.Room{}, rooms.ErrForbidden
	}
	if _, e = requireMember(ctx, tx, rid, actor); e != nil {
		return rooms.Room{}, e
	}
	if p.Name != nil {
		v.Name = *p.Name
	}
	if p.Description != nil {
		v.Description = *p.Description
	}
	if p.IsPublic != nil {
		v.IsPublic = *p.IsPublic
	}
	if p.IsCommunity != nil {
		v.IsCommunity = *p.IsCommunity
	}
	if v.IsCommunity && !v.IsPublic {
		return rooms.Room{}, rooms.ErrInvalid
	}
	v, e = scanRoom(tx.QueryRowContext(ctx, `UPDATE chat_rooms SET owned_name=$2,owned_description=$3,is_public=$4,owned_is_community=$5,owned_updated_at=clock_timestamp() WHERE id=$1 RETURNING `+roomColumns, rid, v.Name, v.Description, v.IsPublic, v.IsCommunity))
	if e != nil {
		return v, e
	}
	return v, tx.Commit()
}
func (r *Repository) Delete(ctx context.Context, actor, rid string) error {
	if e := valid(actor, rid); e != nil {
		return e
	}
	tx, e := r.begin(ctx, false)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	v, e := r.room(ctx, tx, rid, true)
	if e != nil {
		return e
	}
	if v.AccountID != actor {
		return rooms.ErrForbidden
	}
	if _, e = requireMember(ctx, tx, rid, actor); e != nil {
		return e
	}
	for _, q := range []string{`UPDATE chat_messages SET deleted_at=clock_timestamp(),updated_at=clock_timestamp() WHERE chat_room_id=$1 AND deleted_at IS NULL`, `UPDATE chat_members SET deleted_at=clock_timestamp(),owned_updated_at=clock_timestamp() WHERE chat_room_id=$1 AND deleted_at IS NULL`, `UPDATE chat_rooms SET deleted_at=clock_timestamp(),owned_updated_at=clock_timestamp() WHERE id=$1`} {
		if _, e = tx.ExecContext(ctx, q, rid); e != nil {
			return e
		}
	}
	return tx.Commit()
}

// timestamp uses the DB clock after acquiring the room lock, so a blocked send
// cannot be stamped before a read marker that committed while it was waiting.
func timestamp(ctx context.Context, tx *sql.Tx) (time.Time, error) {
	var now time.Time
	e := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return now.UTC(), e
}
