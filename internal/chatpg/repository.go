// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/passport/social"
	"time"
)

// Schema is explicitly installed by the operator/guarded disposable runner.
//
//go:embed schema.sql
var Schema string

type Repository struct {
	db             *sql.DB
	policy         rooms.Policy
	guard          TransactionGuard
	fixture        bool
	mls            bool
	social         rooms.SocialProvider
	associations   MessageAssociations
	roomMedia      RoomMedia
	textObserver   TextMessageObserver
	profilePerks   rooms.ProfileEntitlement
	voice          VoiceStorage
	voiceRetention time.Duration
}

func (r *Repository) WithSocial(p rooms.SocialProvider) *Repository { r.social = p; return r }

type TransactionGuard interface {
	Authenticate(context.Context, *sql.DB, *sql.Tx, string) (authority.Principal, error)
	Authorize(context.Context, *sql.DB, *sql.Tx, string, ...string) (authority.Principal, error)
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

func NewWithAuthority(db *sql.DB, p rooms.Policy, guard TransactionGuard) (*Repository, error) {
	r, err := New(db, p)
	if err == nil {
		r.guard = guard
	}
	return r, err
}

// NewForCompatibilityTests permits only explicitly trusted synthetic callers.
// Ordinary constructors deny mutations without original transaction proof.
func NewForCompatibilityTests(db *sql.DB, p rooms.Policy) (*Repository, error) {
	r, err := New(db, p)
	if err == nil {
		r.fixture = true
	}
	return r, err
}

func (r *Repository) beginWrite(ctx context.Context, actor, permission string) (*sql.Tx, error) {
	if r == nil || (!r.fixture && r.guard == nil) {
		return nil, rooms.ErrUnavailable
	}
	tx, err := r.begin(ctx, false)
	if err != nil {
		return nil, err
	}
	if !r.fixture {
		if permission == "" {
			_, err = r.guard.Authenticate(ctx, r.db, tx, actor)
		} else {
			_, err = r.guard.Authorize(ctx, r.db, tx, actor, permission)
		}
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
	}
	return tx, nil
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

const roomColumns = `id::text,owned_owner_id::text,owned_name,owned_description,is_public,owned_is_community,encryption_mode,owned_created_at,owned_updated_at,deleted_at,owned_type,owned_relation_guard,owned_slug`

type scanner interface{ Scan(...any) error }

func scanRoom(s scanner) (rooms.Room, error) {
	var v rooms.Room
	e := s.Scan(&v.ID, &v.AccountID, &v.Name, &v.Description, &v.IsPublic, &v.IsCommunity, &v.EncryptionMode, &v.CreatedAt, &v.UpdatedAt, &v.DeletedAt, &v.Type, &v.RelationGuard, &v.Slug)
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
	if e == nil && v.EncryptionMode != r.policy.NoneEncryptionMode && !(r.mls && v.EncryptionMode == rooms.MLSEncryptionMode) {
		e = rooms.ErrUnsupported
	}
	if e == nil && v.RelationGuard {
		if r.social == nil {
			return v, rooms.ErrUnavailable
		}
		if e = r.decorateRoom(ctx, tx, &v, ""); e != nil {
			return v, e
		}
	}
	return v, e
}

const memberColumns = `id::text,chat_room_id::text,account_id::text,notify,joined_at,leave_at,last_read_at,owned_confirmed_at,owned_timeout_until,owned_timeout_reason,owned_timeout_sender::text,owned_timeout_since,owned_nick`

func scanMember(s scanner) (rooms.Member, error) {
	var v rooms.Member
	var reason, sender sql.NullString
	var since sql.NullTime
	e := s.Scan(&v.ID, &v.ChatRoomID, &v.AccountID, &v.Notify, &v.JoinedAt, &v.LeaveAt, &v.LastReadAt, &v.ConfirmedAt, &v.TimeoutUntil, &reason, &sender, &since, &v.Nick)
	if sender.Valid && since.Valid {
		v.TimeoutCause = &rooms.TimeoutCause{SenderID: sender.String, Since: since.Time}
		if reason.Valid {
			v.TimeoutCause.Reason = &reason.String
		}
	}
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
	newRealm := ""
	if p.RealmID != nil {
		newRealm = *p.RealmID
	}
	tx, e := r.beginRoomWrite(ctx, actor, "", "chat.create", newRealm)
	if e != nil {
		return rooms.Room{}, e
	}
	defer tx.Rollback()
	if p.RealmID != nil {
		x := r.realmPermission(ctx, tx, actor, *p.RealmID, social.CanModerateChat)
		if x != nil {
			return rooms.Room{}, rooms.ErrForbidden
		}
	}
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
	v, e := scanRoom(tx.QueryRowContext(ctx, `INSERT INTO chat_rooms(id,is_public,encryption_mode,owned_owner_id,owned_name,owned_description,owned_is_community,owned_slug,owned_slug_realm_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING `+roomColumns, rid, public, r.policy.NoneEncryptionMode, actor, *p.Name, description, community, normalizedSlug(p.Slug), p.RealmID))
	if e != nil {
		return v, mapDetailError(e)
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO chat_members(id,chat_room_id,account_id,notify,joined_at,last_read_at) VALUES($1,$2,$3,$4,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, mid, rid, actor, r.policy.NotifyAll)
	if e != nil {
		return v, e
	}
	if p.RealmID != nil {
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_chat_realm_links(room_id,realm_id)VALUES($1,$2)`, rid, *p.RealmID); e != nil {
			return v, e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE chat_rooms SET owned_relation_guard=true WHERE id=$1`, rid); e != nil {
			return v, e
		}
		v.RelationGuard = true
		if e = r.decorateRoom(ctx, tx, &v, actor); e != nil {
			return v, e
		}
	}
	if e = r.applyRoomMedia(ctx, tx, actor, &v, p, false); e != nil {
		return v, e
	}
	if e = r.decorateRoom(ctx, tx, &v, actor); e != nil {
		return v, e
	}
	e = r.commit(ctx, tx, actor, "chat.create")
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
		e = r.readable(ctx, tx, v, actor)
	}
	if e != nil {
		return rooms.Room{}, e
	}
	if e = r.decorateRoom(ctx, tx, &v, actor); e != nil {
		return v, e
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
	rows, e := tx.QueryContext(ctx, `SELECT `+roomColumns+` FROM chat_rooms WHERE owned_owner_id IS NOT NULL AND deleted_at IS NULL AND (encryption_mode=$2 OR ($5 AND encryption_mode=3)) AND EXISTS(SELECT 1 FROM chat_members m WHERE m.chat_room_id=chat_rooms.id AND m.account_id=$1 AND m.joined_at IS NOT NULL AND m.leave_at IS NULL AND m.deleted_at IS NULL AND NOT m.owned_banned) AND `+r.ReadSQL("chat_rooms.id", "$1")+` ORDER BY owned_updated_at DESC,id LIMIT $3 OFFSET $4`, actor, r.policy.NoneEncryptionMode, take, offset, r.mls)
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
	for i := range out {
		if e = r.decorateRoom(ctx, tx, &out[i], actor); e != nil {
			return nil, e
		}
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
	newRealm := ""
	if p.RealmID != nil {
		newRealm = *p.RealmID
	}
	tx, e := r.beginRoomWrite(ctx, actor, rid, "chat.update", newRealm)
	if e != nil {
		return rooms.Room{}, e
	}
	defer tx.Rollback()
	v, e := r.room(ctx, tx, rid, true)
	if e != nil {
		return v, e
	}
	if e = r.manage(ctx, tx, v, actor); e != nil {
		return rooms.Room{}, e
	}
	if v.Type == 1 {
		if e = r.readable(ctx, tx, v, actor); e != nil {
			return v, e
		}
	}
	if v.Type == 1 && p.Slug != nil {
		return v, rooms.ErrInvalid
	}
	if v.Type == 1 && (p.RealmID != nil || p.IsPublic != nil && *p.IsPublic || p.IsCommunity != nil && *p.IsCommunity) {
		return v, rooms.ErrForbidden
	}
	if p.RealmID != nil {
		x := r.realmPermission(ctx, tx, actor, *p.RealmID, social.CanModerateChat)
		if x != nil {
			return v, rooms.ErrForbidden
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_chat_realm_links(room_id,realm_id)VALUES($1,$2)ON CONFLICT(room_id)DO UPDATE SET realm_id=EXCLUDED.realm_id`, rid, *p.RealmID); e != nil {
			return v, e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE chat_rooms SET owned_relation_guard=true WHERE id=$1`, rid); e != nil {
			return v, e
		}
	}
	if p.Slug != nil {
		v.Slug = normalizedSlug(p.Slug)
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
	v, e = scanRoom(tx.QueryRowContext(ctx, `UPDATE chat_rooms SET owned_name=$2,owned_description=$3,is_public=$4,owned_is_community=$5,owned_updated_at=clock_timestamp(),owned_slug=$6,owned_slug_realm_id=coalesce($7::uuid,owned_slug_realm_id) WHERE id=$1 RETURNING `+roomColumns, rid, v.Name, v.Description, v.IsPublic, v.IsCommunity, v.Slug, slugRealm(p, v)))
	if e != nil {
		return v, mapDetailError(e)
	}
	if e = r.applyRoomMedia(ctx, tx, actor, &v, p, true); e != nil {
		return v, e
	}
	if e = r.decorateRoom(ctx, tx, &v, actor); e != nil {
		return v, e
	}
	return v, r.commit(ctx, tx, actor, "chat.update")
}
func (r *Repository) Delete(ctx context.Context, actor, rid string) error {
	if e := valid(actor, rid); e != nil {
		return e
	}
	tx, e := r.beginRoomWrite(ctx, actor, rid, "chat.delete", "")
	if e != nil {
		return e
	}
	defer tx.Rollback()
	v, e := r.room(ctx, tx, rid, true)
	if e != nil {
		return e
	}
	if e = r.manage(ctx, tx, v, actor); e != nil {
		return e
	}
	for _, q := range []string{`UPDATE chat_messages SET deleted_at=clock_timestamp(),updated_at=clock_timestamp() WHERE chat_room_id=$1 AND deleted_at IS NULL`, `UPDATE chat_members SET deleted_at=clock_timestamp(),owned_updated_at=clock_timestamp() WHERE chat_room_id=$1 AND deleted_at IS NULL`, `UPDATE chat_rooms SET deleted_at=clock_timestamp(),owned_updated_at=clock_timestamp() WHERE id=$1`} {
		if _, e = tx.ExecContext(ctx, q, rid); e != nil {
			return e
		}
	}
	return r.commit(ctx, tx, actor, "chat.delete")
}

// timestamp uses the DB clock after acquiring the room lock, so a blocked send
// cannot be stamped before a read marker that committed while it was waiting.
func timestamp(ctx context.Context, tx *sql.Tx) (time.Time, error) {
	var now time.Time
	e := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return now.UTC(), e
}
