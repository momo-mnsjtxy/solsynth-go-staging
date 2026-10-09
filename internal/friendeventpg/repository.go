// SPDX-License-Identifier: AGPL-3.0-only
package friendeventpg

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/notifications"
	"time"
)

//go:embed schema.sql
var Schema string

type Source struct {
	ID, Owner, Session, Activity, Kind        string
	Version, Epoch, Revision, PresenceVersion int64
	At                                        time.Time
}
type Guard interface {
	StoredAuthenticationEligible(context.Context, *sql.DB, *sql.Tx, string, string, int64, int64) (bool, error)
}
type Projector interface {
	FriendActivitiesInTransaction(context.Context, *sql.Tx, string, Source) (json.RawMessage, bool, error)
}
type Repository struct {
	db        *sql.DB
	guard     Guard
	projector Projector
	status    StatusProjector
}

func New(db *sql.DB, g Guard, p Projector) *Repository {
	return &Repository{db: db, guard: g, projector: p}
}

// CaptureActivity is called only by a canonical activity mutation while holding
// its source account lock. Recipients are snapshots of the accepted friendship
// incarnation, not an authorization for eventual delivery. No payload is stored.
func CaptureActivity(ctx context.Context, tx *sql.Tx, s Source, visibility int) error {
	s.Kind = "activity"
	return capture(ctx, tx, s, visibility)
}

func capture(ctx context.Context, tx *sql.Tx, s Source, visibility int) error {
	if tx == nil || !authority.ValidID(s.Owner) || !authority.ValidID(s.Session) || s.Kind == "activity" && !authority.ValidID(s.Activity) {
		return notifications.ErrInvalid
	}
	if visibility != 1 && visibility != 2 {
		return nil
	}
	var visible bool
	if e := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT status_visibility<>'private' FROM owned_presence_preferences WHERE account_id=$1::uuid),true) AND NOT COALESCE((SELECT type=3 FROM owned_presence_statuses WHERE account_id=$1::uuid AND(cleared_at IS NULL OR cleared_at>clock_timestamp()) ORDER BY created_at DESC,id LIMIT 1),false)`, s.Owner).Scan(&visible); e != nil {
		return e
	}
	if !visible {
		return nil
	}
	// Source account locking also serializes canonical friendship mutations.
	rows, e := tx.QueryContext(ctx, `SELECT f.related_id::text,f.policy_revision,r.policy_revision FROM owned_social_effective_relationships f JOIN owned_social_effective_relationships r ON r.account_id=f.related_id AND r.related_id=f.account_id JOIN owned_authority_accounts a ON a.id=f.related_id WHERE f.account_id=$1::uuid AND f.status>=100 AND r.status>=100 AND NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_social_effective_relationships b WHERE ((b.account_id=f.account_id AND b.related_id=f.related_id)OR(b.account_id=f.related_id AND b.related_id=f.account_id)) AND b.status=-100) ORDER BY f.related_id LIMIT 1001`, s.Owner)
	if e != nil {
		return e
	}
	type recipient struct {
		id               string
		forward, reverse int64
	}
	audience := []recipient{}
	for rows.Next() {
		var v recipient
		if e = rows.Scan(&v.id, &v.forward, &v.reverse); e != nil {
			rows.Close()
			return e
		}
		audience = append(audience, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if len(audience) > 1000 {
		return notifications.ErrUnavailable
	}
	if len(audience) == 0 {
		return nil
	}
	var event string
	e = tx.QueryRowContext(ctx, `INSERT INTO owned_friend_events(id,account_id,kind,activity_id,revision,session_id,account_version,authorization_epoch,occurred_at,presence_version) VALUES(gen_random_uuid(),$1::uuid,$7::text,NULLIF($2::text,'')::uuid,$3::bigint,$4::uuid,$5::bigint,$6::bigint,clock_timestamp(),COALESCE((SELECT revision FROM owned_friend_presence_versions WHERE account_id=$1::uuid),0)) ON CONFLICT(activity_id,revision) DO UPDATE SET activity_id=EXCLUDED.activity_id RETURNING id::text`, s.Owner, s.Activity, s.Revision, s.Session, s.Version, s.Epoch, s.Kind).Scan(&event)
	if e != nil {
		return e
	}
	for _, v := range audience {
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_friend_audiences(event_id,recipient_id,forward_revision,reverse_revision)VALUES($1::uuid,$2::uuid,$3::bigint,$4::bigint)ON CONFLICT(event_id,recipient_id)DO NOTHING`, event, v.id, v.forward, v.reverse); e != nil {
			return e
		}
	}
	return nil
}
func (r *Repository) PendingAudienceEvents(ctx context.Context, take int) ([]notifications.AudienceEventPending, error) {
	if r == nil || r.db == nil || take < 1 || take > 100 {
		return nil, notifications.ErrInvalid
	}
	rows, e := r.db.QueryContext(ctx, `SELECT a.event_id::text,a.recipient_id::text,e.account_id::text FROM owned_friend_audiences a JOIN owned_friend_events e ON e.id=a.event_id WHERE a.processed_at IS NULL ORDER BY e.occurred_at,e.id,a.recipient_id LIMIT $1::integer`, take)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []notifications.AudienceEventPending{}
	for rows.Next() {
		var v notifications.AudienceEventPending
		if e = rows.Scan(&v.ID, &v.Recipient, &v.Owner); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (r *Repository) AudienceEventOwner(ctx context.Context, tx *sql.Tx, recipient, id string) (string, error) {
	var owner string
	e := tx.QueryRowContext(ctx, `SELECT e.account_id::text FROM owned_friend_events e JOIN owned_friend_audiences a ON a.event_id=e.id WHERE e.id=$1::uuid AND a.recipient_id=$2::uuid`, id, recipient).Scan(&owner)
	if errors.Is(e, sql.ErrNoRows) {
		return "", nil
	}
	return owner, e
}
func (r *Repository) LockAudienceEvent(ctx context.Context, tx *sql.Tx, recipient, id string) (bool, error) {
	var pending bool
	e := tx.QueryRowContext(ctx, `SELECT processed_at IS NULL FROM owned_friend_audiences WHERE event_id=$1::uuid AND recipient_id=$2::uuid FOR UPDATE`, id, recipient).Scan(&pending)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	return pending, e
}
func (r *Repository) CompleteAudienceEvent(ctx context.Context, tx *sql.Tx, recipient, id string) error {
	_, e := tx.ExecContext(ctx, `UPDATE owned_friend_audiences SET processed_at=clock_timestamp() WHERE event_id=$1::uuid AND recipient_id=$2::uuid`, id, recipient)
	return e
}

// ProjectAudienceEvent requires source and recipient canonical account locks.
// It rehydrates from canonical current rows; enqueue-time data is never a grant.
func (r *Repository) ProjectAudienceEvent(ctx context.Context, tx *sql.Tx, recipient, id string) (event notifications.Event, allow bool, e error) {
	if r == nil || r.db == nil || r.guard == nil || r.projector == nil || tx == nil {
		return event, false, notifications.ErrUnavailable
	}
	var s Source
	var forward, reverse int64
	e = tx.QueryRowContext(ctx, `SELECT e.id::text,e.account_id::text,COALESCE(e.activity_id::text,''),e.revision,e.session_id::text,e.account_version,e.authorization_epoch,e.occurred_at,e.kind,e.presence_version,a.forward_revision,a.reverse_revision FROM owned_friend_events e JOIN owned_friend_audiences a ON a.event_id=e.id WHERE e.id=$1::uuid AND a.recipient_id=$2::uuid`, id, recipient).Scan(&s.ID, &s.Owner, &s.Activity, &s.Revision, &s.Session, &s.Version, &s.Epoch, &s.At, &s.Kind, &s.PresenceVersion, &forward, &reverse)
	if errors.Is(e, sql.ErrNoRows) {
		return event, false, nil
	}
	if e != nil {
		return
	}
	var current bool
	e = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT revision FROM owned_friend_presence_versions WHERE account_id=$1::uuid),0)=$2::bigint`, s.Owner, s.PresenceVersion).Scan(&current)
	if e != nil || !current {
		return event, false, e
	}
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_social_effective_relationships f JOIN owned_social_effective_relationships r ON r.account_id=f.related_id AND r.related_id=f.account_id WHERE f.account_id=$1::uuid AND f.related_id=$2::uuid AND f.status>=100 AND r.status>=100 AND f.policy_revision=$3::bigint AND r.policy_revision=$4::bigint) AND NOT EXISTS(SELECT 1 FROM owned_social_effective_relationships WHERE ((account_id=$1::uuid AND related_id=$2::uuid)OR(account_id=$2::uuid AND related_id=$1::uuid))AND status=-100) AND NOT EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id IN($1::uuid,$2::uuid) AND(a.disabled OR EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp()))))`, s.Owner, recipient, forward, reverse).Scan(&current)
	if e != nil || !current {
		return event, false, e
	}
	current, e = r.guard.StoredAuthenticationEligible(ctx, r.db, tx, s.Owner, s.Session, s.Version, s.Epoch)
	if e != nil || !current {
		return event, false, e
	}
	var data json.RawMessage
	var visible bool
	if s.Kind == "activity" {
		data, visible, e = r.projector.FriendActivitiesInTransaction(ctx, tx, recipient, s)
	} else if s.Kind == "status" && r.status != nil {
		data, visible, e = r.status.FriendStatusEventInTransaction(ctx, tx, recipient, s)
	}
	if e != nil || !visible {
		return event, false, e
	}
	// A marker stores no payload, so appendEvent's limit cannot protect a later
	// projection. Bound the encoded dictionary for both event kinds: valid status
	// metadata can expand beyond the cap through JSON escaping. Suppression lets
	// the worker complete this receipt and progress to a later bounded update.
	if len(data) > notifications.MaxPayload {
		return event, false, nil
	}
	// The projector may wait on artwork. Source authentication is checked again
	// after that wait; the owning recipient rechecks its original request proof.
	current, e = r.guard.StoredAuthenticationEligible(ctx, r.db, tx, s.Owner, s.Session, s.Version, s.Epoch)
	if e != nil || !current {
		return event, false, e
	}
	kind := "account.presence.activities.updated"
	if s.Kind == "status" {
		kind = "account.status.updated"
	}
	return notifications.Event{Type: kind, Data: data, CreatedAt: s.At}, true, nil
}
