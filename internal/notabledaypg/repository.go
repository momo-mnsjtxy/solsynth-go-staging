// SPDX-License-Identifier: AGPL-3.0-only
package notabledaypg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/notabledays"
)

//go:embed schema.sql
var Schema string

// ActivityOverlay reads only existing canonical presence/progression rows in the
// caller's transaction, applying their current audience policy. It must not
// start another transaction, acquire locks, or perform network/provider work.
type ActivityOverlay interface {
	CalendarActivity(context.Context, *sql.Tx, string, string, time.Time, time.Time, string) ([]notabledays.Overlay, error)
	Ready(context.Context, *sql.DB) error
}
type Config struct {
	Now      func() time.Time
	Activity ActivityOverlay
}
type Repository struct {
	db    *sql.DB
	guard *authoritypg.TransactionGuard
	cfg   Config
	media EventMediaStore
}

func New(db *sql.DB, guard *authoritypg.TransactionGuard, cfg Config) (*Repository, error) {
	if db == nil || guard == nil {
		return nil, notabledays.ErrUnavailable
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Repository{db: db, guard: guard, cfg: cfg}, nil
}
func (r *Repository) BindAuthority(ctx context.Context, p authority.Principal) context.Context {
	if r == nil || r.guard == nil {
		return ctx
	}
	return r.guard.Bind(ctx, p)
}
func (r *Repository) Ready(ctx context.Context) error {
	if r == nil || r.db == nil {
		return notabledays.ErrUnavailable
	}
	_, e := r.db.ExecContext(ctx, `SELECT id,definition,region,deleted_at FROM owned_notable_days LIMIT 0; SELECT id,definition,visibility,request_hash,version FROM owned_calendar_events LIMIT 0; SELECT id,subscriber_id,target_account_id FROM owned_calendar_subscriptions LIMIT 0; SELECT actor_id,operation FROM owned_calendar_audit LIMIT 0; SELECT account_id,related_id,status FROM owned_social_effective_relationships LIMIT 0`)
	if e == nil && r.media != nil {
		e = r.media.Ready(ctx)
	}
	if e == nil && r.cfg.Activity != nil {
		e = r.cfg.Activity.Ready(ctx, r.db)
	}
	return e
}
func id() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func mapped(e error) error {
	if errors.Is(e, sql.ErrNoRows) {
		return notabledays.ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(e, &pg) {
		switch pg.Code {
		case "23505", "40P01", "55P03":
			return notabledays.ErrConflict
		case "23514", "22001":
			return notabledays.ErrInvalid
		case "23503":
			return notabledays.ErrNotFound
		}
	}
	return e
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func fingerprint(v any) (string, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
func permission(op string) (string, bool) {
	switch op {
	case "day.create":
		return "notable.days.create", true
	case "day.update":
		return "notable.days.update", true
	case "day.delete":
		return "notable.days.delete", true
	case "event.create":
		return "calendar.events.create", true
	case "event.update":
		return "calendar.events.update", true
	case "event.delete":
		return "calendar.events.delete", true
	case "subscription.add", "subscription.remove":
		return "calendar.subscriptions.manage", true
	case "day.list", "day.get", "day.generated", "event.list", "event.get", "event.tags", "subscription.list", "subscription.subscribers", "calendar.month", "calendar.merged", "calendar.search", "calendar.countdown":
		return "", false
	default:
		return "!", false
	}
}
func (r *Repository) Execute(ctx context.Context, actor string, c notabledays.Command) (out notabledays.Result, e error) {
	if r == nil || r.db == nil || r.guard == nil {
		return out, notabledays.ErrUnavailable
	}
	perm, write := permission(c.Operation)
	if perm == "!" {
		return out, notabledays.ErrUnsupported
	}
	if !notabledays.ValidKey(c.IdempotencyKey) {
		return out, notabledays.ErrInvalid
	}
	if c.IdempotencyKey != "" && c.Operation != "event.create" {
		return out, notabledays.ErrUnsupported
	}
	public := actor == "" && (c.Operation == "day.list" || c.Operation == "day.get" || ((c.Operation == "event.list" || c.Operation == "event.get" || strings.HasPrefix(c.Operation, "calendar.")) && c.Query.Owner != "" && c.Query.Owner != "me" && c.Operation != "calendar.search"))
	if !public && !authority.ValidID(actor) {
		return out, authority.ErrInvalid
	}
	opts := &sql.TxOptions{Isolation: sql.LevelReadCommitted, ReadOnly: public}
	if public && (c.Operation == "day.list" || c.Operation == "day.get") {
		opts = &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	}
	tx, e := r.db.BeginTx(ctx, opts)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	// Subscription FKs touch both accounts. Take the same canonical account
	// order as social writers before the guard or any subscription row lock.
	if c.Operation == "subscription.add" || c.Operation == "subscription.remove" {
		if !authority.ValidID(c.TargetID) || actor == c.TargetID {
			return out, notabledays.ErrInvalid
		}
		ids := []string{actor, c.TargetID}
		sort.Strings(ids)
		for _, accountID := range ids {
			var locked string
			if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE`, accountID).Scan(&locked); e != nil {
				return out, mapped(e)
			}
		}
	}
	if !public {
		if perm == "" {
			_, e = r.guard.Authenticate(ctx, r.db, tx, actor)
		} else {
			_, e = r.guard.Authorize(ctx, r.db, tx, actor, perm)
		}
		if e != nil {
			return out, e
		}
	}
	reads := &calendarReadSet{events: map[string]string{}, versions: map[string]int64{}, snapshots: map[string]notabledays.Event{}}
	ctx = context.WithValue(ctx, calendarReadSetKey{}, reads)
	now := r.cfg.Now().UTC()
	switch {
	case strings.HasPrefix(c.Operation, "day."):
		out, e = r.days(ctx, tx, actor, c, now)
	case strings.HasPrefix(c.Operation, "event."):
		out, e = r.events(ctx, tx, actor, c, now)
	case strings.HasPrefix(c.Operation, "subscription."):
		out, e = r.subscriptions(ctx, tx, actor, c, now)
	case strings.HasPrefix(c.Operation, "calendar."):
		out, e = r.calendar(ctx, tx, actor, c, now)
	}
	if e != nil {
		return notabledays.Result{}, mapped(e)
	}
	if write && out.Changed {
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_calendar_audit(actor_id,operation,resource_id,occurred_at)VALUES($1::uuid,$2::text,$3::uuid,$4::timestamptz)`, actor, c.Operation, out.ResourceID, now); e != nil {
			return notabledays.Result{}, mapped(e)
		}
	}
	if e = r.validateCalendarReads(ctx, tx, actor, c, reads); e != nil {
		return notabledays.Result{}, mapped(e)
	}
	// Media row waits may outlive the original proof. Revalidate that same proof
	// after all associations and before committing, never mint a replacement.
	if !public {
		if perm == "" {
			_, e = r.guard.Authenticate(ctx, r.db, tx, actor)
		} else {
			_, e = r.guard.Authorize(ctx, r.db, tx, actor, perm)
		}
		if e != nil {
			return notabledays.Result{}, e
		}
	}
	if e = tx.Commit(); e != nil {
		return notabledays.Result{}, mapped(e)
	}
	return out, nil
}
func resolveAccount(ctx context.Context, tx *sql.Tx, actor, ref string) (notabledays.Account, string, error) {
	if ref == "" || ref == "me" {
		ref = actor
	}
	if ref == "" || len(ref) > 256 {
		return notabledays.Account{}, "", notabledays.ErrNotFound
	}
	var a notabledays.Account
	var region string
	column := "name"
	if authority.ValidID(ref) {
		column = "id::text"
	}
	e := tx.QueryRowContext(ctx, `SELECT id::text,name,nick,region FROM owned_authority_accounts WHERE `+column+`=$1::text AND NOT disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=owned_authority_accounts.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=statement_timestamp())AND(p.expires_at IS NULL OR p.expires_at>statement_timestamp()))`, ref).Scan(&a.ID, &a.Name, &a.Nick, &region)
	if e != nil {
		return a, region, mapped(e)
	}
	if region == "" {
		region = "CN"
	}
	if actor != "" && actor != a.ID {
		blocked, _, e := relationship(ctx, tx, actor, a.ID)
		if e != nil {
			return a, region, e
		}
		if blocked {
			return a, region, notabledays.ErrNotFound
		}
	}
	return a, region, nil
}
func relationship(ctx context.Context, tx *sql.Tx, a, b string) (blocked, friend bool, e error) {
	if a == "" || b == "" || a == b {
		return false, false, nil
	}
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_social_effective_relationships WHERE ((account_id=$1::uuid AND related_id=$2::uuid)OR(account_id=$2::uuid AND related_id=$1::uuid))AND status=-100),(EXISTS(SELECT 1 FROM owned_social_effective_relationships WHERE account_id=$1::uuid AND related_id=$2::uuid AND status>=100)AND EXISTS(SELECT 1 FROM owned_social_effective_relationships WHERE account_id=$2::uuid AND related_id=$1::uuid AND status>=100))`, a, b).Scan(&blocked, &friend)
	return
}
func page[T any](v []T, q notabledays.Query) ([]T, *int64) {
	total := int64(len(v))
	start := min(q.Offset, len(v))
	end := min(len(v), start+q.Take)
	return v[start:end], &total
}
func sortEvents(v []notabledays.Event) {
	sort.Slice(v, func(i, j int) bool {
		if !v[i].StartTime.Equal(v[j].StartTime) {
			return v[i].StartTime.Before(v[j].StartTime)
		}
		if !v[i].EndTime.Equal(v[j].EndTime) {
			return v[i].EndTime.Before(v[j].EndTime)
		}
		return v[i].ID < v[j].ID
	})
}
