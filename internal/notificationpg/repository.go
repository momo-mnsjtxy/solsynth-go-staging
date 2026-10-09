// SPDX-License-Identifier: AGPL-3.0-only
package notificationpg

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/notifications"
)

//go:embed schema.sql
var baseSchema string

//go:embed friend_schema.sql
var FriendSchema string

var Schema = baseSchema + "\n" + FriendSchema

type Repository struct {
	collections CollectionPublicationPolicy
	friends     notifications.AudienceEventProjector
	preferences PreferencePolicy
	roomAccess  rooms.AccessPolicy
	db          *sql.DB
	guard       *authoritypg.TransactionGuard
	app         string
}

func (r *Repository) WithRoomAccess(p rooms.AccessPolicy) *Repository { r.roomAccess = p; return r }
func (r *Repository) chatFilter(room, actor string) string {
	if r.roomAccess != nil {
		return r.roomAccess.ReadSQL(room, actor)
	}
	return `NOT r.owned_relation_guard`
}

// New never permits request reads or writes without the same-pool authority
// guard. Enqueue and worker methods are trusted in-process infrastructure APIs.
func New(db *sql.DB, guard *authoritypg.TransactionGuard, app string) *Repository {
	return &Repository{db: db, guard: guard, app: app}
}

var _ notifications.Repository = (*Repository)(nil)
var _ notifications.EventStore = (*Repository)(nil)

func uuid() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
func (r *Repository) begin(ctx context.Context) (*sql.Tx, error) {
	if r == nil || r.db == nil || !notifications.ValidApp(r.app) {
		return nil, notifications.ErrUnavailable
	}
	return r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}
func (r *Repository) authorized(ctx context.Context, actor, app string) (*sql.Tx, error) {
	if r == nil {
		return nil, notifications.ErrUnavailable
	}
	if !authority.ValidID(actor) || app != r.app {
		return nil, notifications.ErrInvalid
	}
	tx, e := r.begin(ctx)
	if e != nil {
		return nil, e
	}
	if _, e = r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
		_ = tx.Rollback()
		return nil, e
	}
	return tx, nil
}

type scanner interface{ Scan(...any) error }

const fields = `id::text,account_id::text,app_id,topic,title,subtitle,content,meta,priority,viewed_at,dismissed_at,created_at,updated_at`

func scan(s scanner) (notifications.Notification, error) {
	var n notifications.Notification
	var b []byte
	e := s.Scan(&n.ID, &n.AccountID, &n.AppID, &n.Topic, &n.Title, &n.Subtitle, &n.Content, &b, &n.Priority, &n.ViewedAt, &n.DismissedAt, &n.CreatedAt, &n.UpdatedAt)
	n.Meta = b
	if errors.Is(e, sql.ErrNoRows) {
		e = notifications.ErrNotFound
	}
	return n, e
}
func (r *Repository) List(ctx context.Context, actor, app string, take, offset int) ([]notifications.Notification, int64, error) {
	if take < 1 || take > 100 || offset < 0 || offset > 100000 {
		return nil, 0, notifications.ErrInvalid
	}
	tx, e := r.authorized(ctx, actor, app)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	var total int64
	e = tx.QueryRowContext(ctx, `SELECT count(*) FROM notification_v1.inbox WHERE account_id=$1::text::uuid AND app_id=$2 AND dismissed_at IS NULL AND `+r.inboxVisibilitySQL(), actor, app).Scan(&total)
	if e != nil {
		return nil, 0, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+fields+` FROM notification_v1.inbox WHERE account_id=$1::text::uuid AND app_id=$2 AND dismissed_at IS NULL AND `+r.inboxVisibilitySQL()+` ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`, actor, app, take, offset)
	if e != nil {
		return nil, 0, e
	}
	list := make([]notifications.Notification, 0)
	ids := make([]string, 0)
	for rows.Next() {
		n, e := scan(rows)
		if e != nil {
			rows.Close()
			return nil, 0, e
		}
		list = append(list, n)
		if n.ViewedAt == nil {
			ids = append(ids, n.ID)
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, 0, e
	}
	if len(ids) > 0 {
		_, e = tx.ExecContext(ctx, `UPDATE notification_v1.inbox SET viewed_at=clock_timestamp(),updated_at=clock_timestamp() WHERE account_id=$1 AND app_id=$2 AND id=ANY($3::uuid[]) AND viewed_at IS NULL`, actor, app, ids)
		if e != nil {
			return nil, 0, e
		}
	}

	// Marking viewed can wait. Recheck current domain visibility after that wait
	// and keep only the same rows, preserving the source client's unread shape.
	visibleIDs := make([]string, 0, len(list))
	for _, n := range list {
		visibleIDs = append(visibleIDs, n.ID)
	}
	allowed, e := r.visibleInboxIDs(ctx, tx, actor, app, visibleIDs)
	if e != nil {
		return nil, 0, e
	}
	kept := make([]notifications.Notification, 0, len(list))
	for _, n := range list {
		if allowed[n.ID] {
			kept = append(kept, n)
		}
	}
	list = kept
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM notification_v1.inbox WHERE account_id=$1::text::uuid AND app_id=$2 AND dismissed_at IS NULL AND `+r.inboxVisibilitySQL(), actor, app).Scan(&total); e != nil {
		return nil, 0, e
	}
	if _, e = r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
		return nil, 0, e
	}
	// Keep the pre-update snapshot: the authored client decrements its unread
	// counter for exactly those returned rows whose viewed_at was null.
	if e = tx.Commit(); e != nil {
		return nil, 0, e
	}
	return list, total, nil
}
func (r *Repository) Unread(ctx context.Context, actor, app string) (int64, error) {
	tx, e := r.authorized(ctx, actor, app)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	var n int64
	e = tx.QueryRowContext(ctx, `SELECT count(*) FROM notification_v1.inbox WHERE account_id=$1::text::uuid AND app_id=$2 AND viewed_at IS NULL AND dismissed_at IS NULL AND `+r.inboxVisibilitySQL(), actor, app).Scan(&n)
	if e != nil {
		return 0, e
	}
	return n, tx.Commit()
}
func (r *Repository) MarkRead(ctx context.Context, actor, app, id string) error {
	return r.change(ctx, actor, app, id, false)
}
func (r *Repository) Dismiss(ctx context.Context, actor, app, id string) error {
	if !authority.ValidID(id) {
		return notifications.ErrInvalid
	}
	return r.change(ctx, actor, app, id, true)
}
func (r *Repository) change(ctx context.Context, actor, app, id string, dismiss bool) error {
	if id != "" && !authority.ValidID(id) {
		return notifications.ErrInvalid
	}
	tx, e := r.authorized(ctx, actor, app)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	set := `viewed_at=coalesce(viewed_at,clock_timestamp())`
	if dismiss {
		set += `,dismissed_at=coalesce(dismissed_at,clock_timestamp())`
	}
	result, e := tx.ExecContext(ctx, `UPDATE notification_v1.inbox SET `+set+`,updated_at=clock_timestamp() WHERE account_id=$1 AND app_id=$2 AND ($3='' OR id=NULLIF($3,'')::uuid)`, actor, app, id)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if id != "" && n == 0 {
		return notifications.ErrNotFound
	}
	return tx.Commit()
}

// Replay uses an account-scoped cursor and rechecks the original proof under
// the authority lock on every call. Filtered events still advance Cursor.
func (r *Repository) Replay(ctx context.Context, actor, app string, after int64, take int) (notifications.Page, error) {
	p := notifications.Page{Events: []notifications.Event{}}
	if r == nil || r.guard == nil {
		return p, notifications.ErrUnavailable
	}
	if !authority.ValidID(actor) || app != r.app || after < 0 || take < 1 || take > 100 {
		return p, notifications.ErrInvalid
	}
	tx, e := r.begin(ctx)
	if e != nil {
		return p, e
	}
	defer tx.Rollback()
	if _, e = r.guard.Preflight(ctx, r.db, tx, actor); e != nil {
		return p, e
	}
	var floor int64
	e = tx.QueryRowContext(ctx, `SELECT sequence,floor FROM notification_v1.streams WHERE account_id=$1 AND app_id=$2`, actor, app).Scan(&p.HighWater, &floor)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return p, e
	}
	if after < floor || after > p.HighWater {
		return p, notifications.ErrCursor
	}
	p.Cursor = after
	// Freeze the finite committed page before discovering its complete account
	// lock set. New commits above HighWater are observed on the next replay call.
	rows, e := tx.QueryContext(ctx, `SELECT sequence,type,data,created_at,audience,audience_id::text FROM notification_v1.events WHERE account_id=$1 AND app_id=$2 AND sequence>$3 AND sequence<=$5 ORDER BY sequence LIMIT $4`, actor, app, after, take, p.HighWater)
	if e != nil {
		return p, e
	}
	type item struct {
		event    notifications.Event
		audience string
		id       *string
	}
	items := []item{}
	for rows.Next() {
		var v item
		var b []byte
		if e = rows.Scan(&v.event.Sequence, &v.event.Type, &b, &v.event.CreatedAt, &v.audience, &v.id); e != nil {
			rows.Close()
			return p, e
		}
		v.event.Data = b
		items = append(items, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return p, e
	}
	ids := map[string]bool{actor: true}
	if r.friends != nil {
		for _, v := range items {
			if v.audience == "friend" && v.id != nil {
				owner, x := r.friends.AudienceEventOwner(ctx, tx, actor, *v.id)
				if x != nil {
					return p, x
				}
				if owner != "" {
					ids[owner] = true
				}
			}
		}
	}
	locks := []string{}
	for id := range ids {
		locks = append(locks, id)
	}
	sort.Strings(locks)
	for _, id := range locks {
		if _, e = lockAccount(ctx, tx, id); e != nil {
			return p, e
		}
	}
	if _, e = r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
		return p, e
	}
	for _, v := range items {
		p.Cursor = v.event.Sequence
		visible := v.audience == "account"
		if v.audience == "notification" {
			e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM notification_v1.inbox WHERE account_id=$1::text::uuid AND app_id=$2 AND id=$3 AND dismissed_at IS NULL AND `+r.inboxVisibilitySQL()+`)`, actor, app, v.id).Scan(&visible)
		}
		if v.audience == "chat" {
			e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_members m JOIN chat_rooms r ON r.id=m.chat_room_id WHERE m.account_id=$1 AND r.id=$2 AND m.joined_at IS NOT NULL AND m.joined_at<=$3 AND m.leave_at IS NULL AND m.deleted_at IS NULL AND NOT m.owned_banned AND r.deleted_at IS NULL AND `+r.chatFilter("r.id", "$1")+`)`, actor, v.id, v.event.CreatedAt).Scan(&visible)
		}
		if v.audience == "friend" && r.friends != nil && v.id != nil {
			event, allow, x := r.friends.ProjectAudienceEvent(ctx, tx, actor, *v.id)
			e = x
			visible = allow
			event.Sequence = v.event.Sequence
			v.event = event
		}
		if e != nil {
			return p, e
		}
		if visible {
			p.Events = append(p.Events, v.event)
		}
	}
	// A later artwork wait can consume an earlier event's source lifetime. With
	// all locks held, reproject every friend event and check the original reader.
	final := []notifications.Event{}
	for _, event := range p.Events {
		var source *string
		for _, v := range items {
			if v.event.Sequence == event.Sequence && v.audience == "friend" {
				source = v.id
				break
			}
		}

		// A friend/media wait may outlive another notification's source grant.
		// Recheck notification visibility with the current statement snapshot.
		allowed := true
		for _, v := range items {
			if v.event.Sequence == event.Sequence && v.audience == "notification" {
				e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM notification_v1.inbox WHERE account_id=$1::text::uuid AND app_id=$2 AND id=$3 AND dismissed_at IS NULL AND `+r.inboxVisibilitySQL()+`)`, actor, app, v.id).Scan(&allowed)
				if e != nil {
					return p, e
				}
				break
			}
		}
		if !allowed {
			continue
		}
		if source != nil {
			fresh, allow, x := r.friends.ProjectAudienceEvent(ctx, tx, actor, *source)
			if x != nil {
				return p, x
			}
			if !allow {
				continue
			}
			fresh.Sequence = event.Sequence
			event = fresh
		}
		final = append(final, event)
	}

	// All projection waits are now complete. Filter the entire notification
	// subset in one fresh statement so a later friend's wait cannot preserve an
	// earlier source grant. The immutable stream cursor still advances.
	notificationIDs := []string{}
	eventIDs := map[int64]string{}
	for _, v := range items {
		if v.audience == "notification" && v.id != nil {
			notificationIDs = append(notificationIDs, *v.id)
			eventIDs[v.event.Sequence] = *v.id
		}
	}
	allowed, e := r.visibleInboxIDs(ctx, tx, actor, app, notificationIDs)
	if e != nil {
		return p, e
	}
	p.Events = []notifications.Event{}
	for _, event := range final {
		id, scoped := eventIDs[event.Sequence]
		if !scoped || allowed[id] {
			p.Events = append(p.Events, event)
		}
	}
	if len(items) == 0 {
		p.Cursor = p.HighWater
	}
	if _, e = r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
		return notifications.Page{}, e
	}
	return p, tx.Commit()
}

// appendEvent requires the recipient's canonical authority account lock. That
// lock serializes commit order, so an identity sequence cannot skip late commits.
func appendEvent(ctx context.Context, tx *sql.Tx, account, app, key, kind, audience string, aid *string, data []byte, at time.Time) error {
	if len(data) > notifications.MaxPayload || !json.Valid(data) {
		return notifications.ErrInvalid
	}
	var exists bool
	if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM notification_v1.events WHERE account_id=$1 AND app_id=$2 AND source_key=$3)`, account, app, key).Scan(&exists); e != nil {
		return e
	}
	if exists {
		return nil
	}
	var seq int64
	e := tx.QueryRowContext(ctx, `INSERT INTO notification_v1.streams(account_id,app_id,sequence) VALUES($1,$2,1) ON CONFLICT(account_id,app_id) DO UPDATE SET sequence=notification_v1.streams.sequence+1 RETURNING sequence`, account, app).Scan(&seq)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO notification_v1.events(account_id,app_id,sequence,source_key,type,data,audience,audience_id,created_at) VALUES($1,$2,$3,$4,$5,$6::jsonb,$7,$8,$9)`, account, app, seq, key, kind, string(data), audience, aid, at)
	return e
}
func lockAccount(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	var active bool
	e := tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, id).Scan(&active)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	return active, e
}
