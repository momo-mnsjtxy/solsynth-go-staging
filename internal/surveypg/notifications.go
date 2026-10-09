// SPDX-License-Identifier: AGPL-3.0-only
package surveypg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/notifications"
	"solsynth.local/go-migration/ports/sphere/surveys"
	"strings"
	"time"
	"unicode/utf8"
)

// LocalNotificationSink joins the domain transaction. Implementations must
// project only local inbox/replay state; no provider I/O or external delivery.
type LocalNotificationSink interface {
	ProjectLocalInTransaction(context.Context, *sql.DB, *sql.Tx, notifications.Command) error
}

// enqueueAnswer snapshots subscription incarnations, not private answers or
// respondent identities. Each changed response revision has one durable item
// per recipient. An identical retry creates no new response revision.
func enqueueAnswer(ctx context.Context, tx *sql.Tx, actor string, s surveys.Survey, answer, app string, now time.Time) error {
	if !notifications.ValidApp(app) {
		return surveys.ErrUnavailable
	}
	rows, e := tx.QueryContext(ctx, `SELECT id::text,account_id::text FROM owned_survey_subscriptions WHERE survey_id=$1 AND revoked_at IS NULL AND account_id<>$2 ORDER BY id LIMIT $3`, s.ID, actor, MaxSubscribers+1)
	if e != nil {
		return e
	}
	type target struct{ id, account string }
	var targets []target
	for rows.Next() {
		var t target
		if e = rows.Scan(&t.id, &t.account); e != nil {
			rows.Close()
			return e
		}
		targets = append(targets, t)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if len(targets) > MaxSubscribers {
		return surveys.ErrLimit
	}
	expires := now.Add(24 * time.Hour)
	if s.EndedAt != nil && s.EndedAt.Before(expires) {
		expires = *s.EndedAt
	}
	for _, t := range targets {
		id, e := uid()
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_survey_notifications(id,survey_id,subscription_id,account_id,answer_id,answer_revision,created_at,expires_at,app_id) SELECT $1,$2,$3,$4,id,revision,$6,$7,$8 FROM owned_survey_answers WHERE id=$5 ON CONFLICT(answer_id,answer_revision,subscription_id) DO NOTHING`, id, s.ID, t.id, t.account, answer, now, expires, app)
		if e != nil {
			return e
		}
	}
	return nil
}

// ProcessNotifications is a finite trusted runtime worker. It rechecks current
// account, survey, post, publisher/realm, block/mute and exact subscription
// incarnation before atomically projecting a minimal local notification.
// Failures roll back both inbox/event and queue receipt. No external sender is
// accepted here; historical inbox items are not retroactively erased.
func (r *Repository) ProcessNotifications(ctx context.Context, sink LocalNotificationSink, app string, limit int) (int, error) {
	if r == nil || r.db == nil || sink == nil || !notifications.ValidApp(app) || app != r.notificationApp {
		return 0, surveys.ErrUnavailable
	}
	if limit < 1 || limit > 100 {
		return 0, surveys.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rows, e := r.db.QueryContext(ctx, `SELECT id::text,survey_id::text,account_id::text FROM owned_survey_notifications WHERE processed_at IS NULL AND app_id=$1 ORDER BY created_at,id LIMIT $2`, app, limit)
	if e != nil {
		return 0, e
	}
	type item struct{ id, survey, account string }
	var items []item
	for rows.Next() {
		var v item
		if e = rows.Scan(&v.id, &v.survey, &v.account); e != nil {
			rows.Close()
			return 0, e
		}
		items = append(items, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	n := 0
	for _, v := range items {
		done, e := r.processNotification(ctx, sink, app, v.id, v.survey, v.account)
		if e != nil {
			return n, e
		}
		if done {
			n++
		}
	}
	return n, nil
}
func (r *Repository) processNotification(ctx context.Context, sink LocalNotificationSink, app, id, survey, account string) (bool, error) {
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	// Always account -> realm/publisher/post -> survey -> queue/inbox. No lease
	// or outbox lock is held while waiting for canonical authority/domain locks.
	var active bool
	if e = tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, account).Scan(&active); e != nil {
		return false, e
	}
	x, e := r.load(ctx, tx, account, survey, false)
	if errors.Is(e, surveys.ErrNotFound) {
		active = false
	} else if e != nil {
		return false, e
	}
	var subscriptionID string
	var expires time.Time
	var processed *time.Time
	var pinnedApp *string
	e = tx.QueryRowContext(ctx, `SELECT subscription_id::text,expires_at,processed_at,app_id FROM owned_survey_notifications WHERE id=$1 AND survey_id=$2 AND account_id=$3 FOR UPDATE SKIP LOCKED`, id, survey, account).Scan(&subscriptionID, &expires, &processed, &pinnedApp)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if processed != nil {
		return false, nil
	}
	if pinnedApp == nil || *pinnedApp != app {
		return false, surveys.ErrConflict
	}
	if active {
		active, e = notificationAccountActive(ctx, tx, account)
		if e != nil {
			return false, e
		}
		if active && x.s.Publisher.AccountID != nil {
			active, e = notificationAccountActive(ctx, tx, *x.s.Publisher.AccountID)
			if e != nil {
				return false, e
			}
		}
		if active && x.s.Publisher.RealmID != nil {
			var owner string
			if e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_realms WHERE id=$1`, *x.s.Publisher.RealmID).Scan(&owner); e != nil {
				return false, e
			}
			active, e = notificationAccountActive(ctx, tx, owner)
			if e != nil {
				return false, e
			}
		}
		if active && x.s.PostID != nil {
			// An individual publisher's post may have its own realm. That
			// realm's owner is a separate source account from the publisher
			// owner and must pass the same current punishment check. The
			// canonical load already holds the post and realm locks; do not
			// acquire a source-account lock after those domain locks.
			var owner *string
			if e = tx.QueryRowContext(ctx, `SELECT r.account_id::text FROM owned_text_posts p LEFT JOIN owned_realms r ON r.id=p.realm_id WHERE p.id=$1`, *x.s.PostID).Scan(&owner); e != nil {
				return false, e
			}
			if owner != nil {
				active, e = notificationAccountActive(ctx, tx, *owner)
				if e != nil {
					return false, e
				}
			}
		}
	}
	now := r.now().UTC()
	active = active && expires.After(now) && x.s.Status == surveys.Published && x.s.NotifySubscribers && (x.s.EndedAt == nil || x.s.EndedAt.After(now))
	if active {
		var subscribed bool
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_survey_subscriptions WHERE id=$1 AND survey_id=$2 AND account_id=$3 AND revoked_at IS NULL)`, subscriptionID, survey, account).Scan(&subscribed)
		if e != nil {
			return false, e
		}
		active = subscribed
	}
	if active && x.s.Publisher.AccountID != nil && r.visibility != nil {
		policy, ok := r.visibility.(interface {
			IsMutedInTransaction(context.Context, *sql.Tx, string, string) (bool, error)
		})
		if !ok {
			return false, surveys.ErrUnavailable
		}
		muted, e := policy.IsMutedInTransaction(ctx, tx, *x.s.Publisher.AccountID, account)
		if e != nil {
			return false, e
		}
		active = !muted
	}
	if active {
		title := notificationTitle(x.s.Title)
		meta, _ := json.Marshal(map[string]string{"survey_id": survey, "publisher_id": x.s.PublisherID, "action_uri": "/surveys/" + survey})
		// The source's answerer_id is deliberately omitted for ALL surveys. Results,
		// answer identifiers, text and statistics never enter inbox or replay data.
		e = sink.ProjectLocalInTransaction(ctx, r.db, tx, notifications.Command{Key: "survey-answer:" + id, AccountID: account, AppID: app, Topic: "surveys.answer", Title: title, Content: "Someone just answered your survey.", Meta: meta})
		if e != nil {
			return false, e
		}
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_survey_notifications SET app_id=$2::text,processed_at=$3::timestamptz,suppressed_at=CASE WHEN $4::boolean THEN NULL::timestamptz ELSE $3::timestamptz END WHERE id=$1::uuid`, id, app, now, active)
	if e != nil {
		return false, e
	}
	return true, tx.Commit()
}

func notificationAccountActive(ctx context.Context, tx *sql.Tx, account string) (bool, error) {
	var active bool
	e := tx.QueryRowContext(ctx, `SELECT NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp())) FROM owned_authority_accounts a WHERE a.id=$1`, account).Scan(&active)
	return active, e
}

// WithNotificationApp binds enqueue and projection to the host's explicit local
// inbox namespace. Configure before sharing the repository across goroutines.
func (r *Repository) WithNotificationApp(app string) *Repository { r.notificationApp = app; return r }

func notificationTitle(value *string) string {
	if value == nil {
		return "a survey"
	}
	title := strings.TrimSpace(*value)
	if title == "" {
		return "a survey"
	}
	if len(title) > 1024 {
		title = title[:1024]
		for !utf8.ValidString(title) {
			title = title[:len(title)-1]
		}
	}
	return title
}
