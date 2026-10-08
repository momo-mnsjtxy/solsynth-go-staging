// SPDX-License-Identifier: AGPL-3.0-only
package callpg

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager/calls"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/notifications"
	"time"
)

//go:embed schema.sql
var Schema string

type Config struct {
	NoneEncryptionMode int32
	TokenTTL, CallTTL  time.Duration
	NotificationApp    string
}
type Repository struct {
	db       *sql.DB
	guard    *authoritypg.TransactionGuard
	provider calls.Provider
	cfg      Config
}

var _ calls.Repository = (*Repository)(nil)

func New(db *sql.DB, guard *authoritypg.TransactionGuard, p calls.Provider, c Config) (*Repository, error) {
	if db == nil || guard == nil || c.TokenTTL < 15*time.Second || c.TokenTTL > 5*time.Minute || c.CallTTL < c.TokenTTL || c.CallTTL > 24*time.Hour || !notifications.ValidApp(c.NotificationApp) {
		return nil, calls.ErrInvalid
	}
	if p != nil && (!p.StrictRevocation() || p.Name() == "" || p.Endpoint() == "") {
		return nil, calls.ErrUnconfigured
	}
	return &Repository{db, guard, p, c}, nil
}
func uuid() (string, error) {
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
		if !authority.ValidID(id) {
			return calls.ErrInvalid
		}
	}
	return nil
}
func (r *Repository) begin(ctx context.Context) (*sql.Tx, error) {
	if r == nil || r.db == nil {
		return nil, calls.ErrUnavailable
	}
	return r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}
func (r *Repository) configured() error {
	if r == nil || r.provider == nil {
		return calls.ErrUnconfigured
	}
	return nil
}
func (r *Repository) auth(ctx context.Context, tx *sql.Tx, actor, permission string) (authority.Principal, error) {
	if permission != "" {
		return r.guard.Authorize(ctx, r.db, tx, actor, permission)
	}
	return r.guard.Authenticate(ctx, r.db, tx, actor)
}
func dbnow(ctx context.Context, tx *sql.Tx) (time.Time, error) {
	var now time.Time
	e := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return now.UTC(), e
}
func room(ctx context.Context, tx *sql.Tx, rid string, mode int32) (rooms.Room, error) {
	var v rooms.Room
	e := tx.QueryRowContext(ctx, `SELECT id::text,owned_owner_id::text,owned_name,owned_description,is_public,owned_is_community,encryption_mode,owned_created_at,owned_updated_at,deleted_at FROM chat_rooms WHERE id=$1 AND owned_owner_id IS NOT NULL FOR UPDATE`, rid).Scan(&v.ID, &v.AccountID, &v.Name, &v.Description, &v.IsPublic, &v.IsCommunity, &v.EncryptionMode, &v.CreatedAt, &v.UpdatedAt, &v.DeletedAt)
	if errors.Is(e, sql.ErrNoRows) || e == nil && v.DeletedAt != nil {
		return v, calls.ErrNotFound
	}
	if e == nil && v.EncryptionMode != mode {
		return v, calls.ErrUnsupported
	}
	return v, e
}
func member(ctx context.Context, tx *sql.Tx, rid, actor string) (rooms.Member, error) {
	var m rooms.Member
	e := tx.QueryRowContext(ctx, `SELECT id::text,chat_room_id::text,account_id::text,notify,joined_at,leave_at FROM chat_members WHERE chat_room_id=$1 AND account_id=$2 AND joined_at IS NOT NULL AND leave_at IS NULL AND deleted_at IS NULL AND NOT owned_banned`, rid, actor).Scan(&m.ID, &m.ChatRoomID, &m.AccountID, &m.Notify, &m.JoinedAt, &m.LeaveAt)
	if errors.Is(e, sql.ErrNoRows) {
		e = calls.ErrForbidden
	}
	return m, e
}

const fields = `id::text,room_id::text,sender_id::text,provider,provider_endpoint,session_id,state,created_at,updated_at,expires_at,ended_at`

type scanner interface{ Scan(...any) error }

func scan(s scanner) (calls.Call, error) {
	var v calls.Call
	e := s.Scan(&v.ID, &v.RoomID, &v.SenderID, &v.ProviderName, &v.ProviderEndpoint, &v.SessionID, &v.State, &v.CreatedAt, &v.UpdatedAt, &v.ExpiresAt, &v.EndedAt)
	if errors.Is(e, sql.ErrNoRows) {
		e = calls.ErrNotFound
	}
	return v, e
}
func ongoing(ctx context.Context, tx *sql.Tx, rid string) (calls.Call, error) {
	return scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM call_v1.calls WHERE room_id=$1 AND ended_at IS NULL`, rid))
}
func event(ctx context.Context, tx *sql.Tx, c calls.Call, kind, target, actor string) error {
	id, e := uuid()
	if e != nil {
		return e
	}
	data, e := json.Marshal(map[string]string{"call_id": c.ID, "room_id": c.RoomID, "account_id": actor, "state": c.State})
	if e != nil {
		return e
	}
	var to any
	if target != "" {
		to = target
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO call_v1.events(id,call_id,room_id,target_id,type,payload,created_at) VALUES($1,$2,$3,$4,$5,$6::jsonb,clock_timestamp())`, id, c.ID, c.RoomID, to, kind, string(data))
	return e
}
func (r *Repository) locked(ctx context.Context, actor, rid, permission string) (*sql.Tx, authority.Principal, rooms.Room, rooms.Member, error) {
	if e := valid(actor, rid); e != nil {
		return nil, authority.Principal{}, rooms.Room{}, rooms.Member{}, e
	}
	tx, e := r.begin(ctx)
	if e != nil {
		return nil, authority.Principal{}, rooms.Room{}, rooms.Member{}, e
	}
	p, e := r.auth(ctx, tx, actor, permission)
	var v rooms.Room
	var m rooms.Member
	if e == nil {
		v, e = room(ctx, tx, rid, r.cfg.NoneEncryptionMode)
	}
	if e == nil {
		m, e = member(ctx, tx, rid, actor)
	}
	if e != nil {
		_ = tx.Rollback()
		return nil, p, v, m, e
	}
	return tx, p, v, m, nil
}
func (r *Repository) Start(ctx context.Context, actor, rid string) (calls.Call, error) {
	return r.ensure(ctx, actor, rid, true)
}
func (r *Repository) ensure(ctx context.Context, actor, rid string, enforceBan bool) (calls.Call, error) {
	if e := r.configured(); e != nil {
		return calls.Call{}, e
	}
	tx, _, v, m, e := r.locked(ctx, actor, rid, "")
	if e != nil {
		return calls.Call{}, e
	}
	defer tx.Rollback()
	if enforceBan {
		if e = timeout(ctx, tx, m.ID); e != nil {
			return calls.Call{}, e
		}
	}
	c, e := ongoing(ctx, tx, rid)
	now, ne := dbnow(ctx, tx)
	if ne != nil {
		return c, ne
	}
	if errors.Is(e, calls.ErrNotFound) {
		id, ie := uuid()
		if ie != nil {
			return c, ie
		}
		c, e = scan(tx.QueryRowContext(ctx, `INSERT INTO call_v1.calls(id,room_id,sender_id,provider,provider_endpoint,session_id,state,created_at,updated_at,expires_at) VALUES($1,$2,$3,$4,$8,$5,'preparing',$6,$6,$7) RETURNING `+fields, id, rid, m.ID, r.provider.Name(), "call_"+id, now, now.Add(r.cfg.CallTTL), r.provider.Endpoint()))
	}
	if e != nil {
		return c, e
	}
	if e = r.checkProvider(c); e != nil {
		return c, e
	}
	if c.State == "ending" || !c.ExpiresAt.After(now) {
		return c, calls.ErrConflict
	}
	if c.State == "active" {
		op, cancel := context.WithTimeout(ctx, 5*time.Second)
		e = r.provider.EnsureRoom(op, c.SessionID)
		cancel()
		if e != nil {
			return c, calls.ErrProvider
		}
		if e = hydrate(ctx, tx, &c, v); e != nil {
			return c, e
		}
		return c, tx.Commit()
	}
	// Record intent before the external room create so a crash cannot lose its name.
	if e = tx.Commit(); e != nil {
		return c, e
	}
	tx, _, v, m, e = r.locked(ctx, actor, rid, "")
	if e != nil {
		return c, e
	}
	defer tx.Rollback()
	c, e = ongoing(ctx, tx, rid)
	if e != nil {
		return c, e
	}
	now, e = dbnow(ctx, tx)
	if e != nil {
		return c, e
	}
	if e = r.checkProvider(c); e != nil {
		return c, e
	}
	if c.State == "ending" || !c.ExpiresAt.After(now) {
		return c, calls.ErrConflict
	}
	if c.State == "preparing" {
		op, cancel := context.WithTimeout(ctx, 5*time.Second)
		e = r.provider.EnsureRoom(op, c.SessionID)
		cancel()
		if e != nil {
			return c, calls.ErrProvider
		}
		c, e = scan(tx.QueryRowContext(ctx, `UPDATE call_v1.calls SET state='active',updated_at=clock_timestamp() WHERE id=$1 RETURNING `+fields, c.ID))
		if e != nil {
			return c, e
		}
		if e = event(ctx, tx, c, "owned.call.started", "", actor); e != nil {
			return c, e
		}
	}
	if e = hydrate(ctx, tx, &c, v); e != nil {
		return c, e
	}
	return c, tx.Commit()
}

// The source's GET ensures the persistent call exists; it remains no-store.
func (r *Repository) Get(ctx context.Context, actor, rid string) (calls.Call, error) {
	return r.ensure(ctx, actor, rid, false)
}
func active(ctx context.Context, tx *sql.Tx, rid string) (calls.Call, error) {
	c, e := ongoing(ctx, tx, rid)
	if e != nil {
		return c, e
	}
	now, e := dbnow(ctx, tx)
	if e == nil && (c.State != "active" || !c.ExpiresAt.After(now)) {
		e = calls.ErrConflict
	}
	return c, e
}

func hydrate(ctx context.Context, tx *sql.Tx, c *calls.Call, v rooms.Room) error {
	var m rooms.Member
	e := tx.QueryRowContext(ctx, `SELECT id::text,chat_room_id::text,account_id::text,notify,joined_at,leave_at FROM chat_members WHERE id=$1`, c.SenderID).Scan(&m.ID, &m.ChatRoomID, &m.AccountID, &m.Notify, &m.JoinedAt, &m.LeaveAt)
	c.Room = &v
	c.Sender = &m
	return e
}

func (r *Repository) checkProvider(c calls.Call) error {
	if e := r.configured(); e != nil {
		return e
	}
	if c.ProviderName != r.provider.Name() || c.ProviderEndpoint != r.provider.Endpoint() {
		return calls.ErrUnconfigured
	}
	return nil
}

func timeout(ctx context.Context, tx *sql.Tx, mid string) error {
	var banned bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM call_v1.timeouts WHERE member_id=$1 AND timeout_until>clock_timestamp())`, mid).Scan(&banned)
	if e != nil {
		return e
	}
	if banned {
		return calls.ErrForbidden
	}
	return nil
}
