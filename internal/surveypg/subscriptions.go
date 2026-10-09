// SPDX-License-Identifier: AGPL-3.0-only
package surveypg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/surveys"
	"time"
)

const MaxSubscribers = 1000

func (r *Repository) recheck(ctx context.Context, tx *sql.Tx, actor, permission string) error {
	if r.guard == nil {
		return nil
	}
	var e error
	if permission == "" {
		_, e = r.guard.Authenticate(ctx, r.db, tx, actor)
	} else {
		_, e = r.guard.Authorize(ctx, r.db, tx, actor, permission)
	}
	return e
}
func subscription(ctx context.Context, tx *sql.Tx, actor, id string) (v surveys.Subscription, e error) {
	e = tx.QueryRowContext(ctx, `SELECT id::text,survey_id::text,account_id::text,created_at,updated_at FROM owned_survey_subscriptions WHERE survey_id=$1 AND account_id=$2 AND revoked_at IS NULL`, id, actor).Scan(&v.ID, &v.SurveyID, &v.AccountID, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		e = surveys.ErrSubscriptionNotFound
	}
	return
}

// Caller holds the survey exclusively, including for auto-subscription on
// create/update. The cap and unique live pair are therefore replica-safe.
func ensureSubscription(ctx context.Context, tx *sql.Tx, actor, id string, now time.Time) (surveys.Subscription, error) {
	v, e := subscription(ctx, tx, actor, id)
	if e == nil {
		return v, nil
	}
	if !errors.Is(e, surveys.ErrSubscriptionNotFound) {
		return v, e
	}
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_survey_subscriptions WHERE survey_id=$1 AND revoked_at IS NULL`, id).Scan(&count); e != nil {
		return v, e
	}
	if count >= MaxSubscribers {
		return v, surveys.ErrLimit
	}
	v = surveys.Subscription{SurveyID: id, AccountID: actor, CreatedAt: now, UpdatedAt: now}
	if v.ID, e = uid(); e != nil {
		return v, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_survey_subscriptions(id,survey_id,account_id,created_at,updated_at) VALUES($1,$2,$3,$4,$4)`, v.ID, id, actor, now)
	return v, e
}
func (r *Repository) Subscribe(ctx context.Context, actor, id string) (surveys.Subscription, error) {
	tx, e := r.begin(ctx, actor, "surveys.subscribe", true)
	if e != nil {
		return surveys.Subscription{}, e
	}
	defer tx.Rollback()
	if _, e = r.load(ctx, tx, actor, id, false, true); e != nil {
		return surveys.Subscription{}, e
	}
	v, e := ensureSubscription(ctx, tx, actor, id, r.now().UTC())
	if e != nil {
		return v, e
	}
	if e = r.recheck(ctx, tx, actor, "surveys.subscribe"); e != nil {
		return surveys.Subscription{}, e
	}
	if e = audit(ctx, tx, actor, id, "subscribe", r.now().UTC()); e != nil {
		return surveys.Subscription{}, e
	}
	return v, tx.Commit()
}
func (r *Repository) Subscription(ctx context.Context, actor, id string) (surveys.Subscription, error) {
	tx, e := r.begin(ctx, actor, "", true)
	if e != nil {
		return surveys.Subscription{}, e
	}
	defer tx.Rollback()
	if _, e = r.load(ctx, tx, actor, id, false); e != nil {
		return surveys.Subscription{}, e
	}
	v, e := subscription(ctx, tx, actor, id)
	if e != nil {
		return v, e
	}
	if e = r.recheck(ctx, tx, actor, ""); e != nil {
		return surveys.Subscription{}, e
	}
	return v, tx.Commit()
}

// Removing one's own subscription is idempotent even when its survey has
// become private, deleted or unavailable. It reveals no survey state.
func (r *Repository) Unsubscribe(ctx context.Context, actor, id string) error {
	if !authority.ValidID(id) {
		return surveys.ErrNotFound
	}
	tx, e := r.begin(ctx, actor, "surveys.subscribe", true)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	// Take the same survey lock as enqueue so no recipient can be added between
	// revocation and cancellation. No read of survey contents is needed.
	var held string
	e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_surveys WHERE id=$1 FOR UPDATE`, id).Scan(&held)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if e = r.recheck(ctx, tx, actor, "surveys.subscribe"); e != nil {
		return e
	}
	result, e := tx.ExecContext(ctx, `UPDATE owned_survey_subscriptions SET revoked_at=$3,updated_at=$3 WHERE survey_id=$1 AND account_id=$2 AND revoked_at IS NULL`, id, actor, r.now().UTC())
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n > 0 {
		if e = audit(ctx, tx, actor, id, "unsubscribe", r.now().UTC()); e != nil {
			return e
		}
	}
	return tx.Commit()
}
