// SPDX-License-Identifier: AGPL-3.0-only
package botpg

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/develop/bots"
	"solsynth.local/go-migration/ports/passport/accounts"
	"sort"
	"time"
)

//go:embed schema.sql
var Schema string

type TransactionGuard interface {
	Authenticate(context.Context, *sql.DB, *sql.Tx, string) (authority.Principal, error)
	Authorize(context.Context, *sql.DB, *sql.Tx, string, ...string) (authority.Principal, error)
}
type Repository struct {
	db    *sql.DB
	guard TransactionGuard
	media bots.MediaProvider
}

func New(db *sql.DB) *Repository { return &Repository{db: db} }
func NewWithAuthority(db *sql.DB, g TransactionGuard) *Repository {
	return &Repository{db: db, guard: g}
}

func (r *Repository) WithMedia(m bots.MediaProvider) *Repository { r.media = m; return r }

// Ready checks the explicitly installed schema; it never creates tables or policy.
func (r *Repository) Ready(ctx context.Context) error {
	if r == nil || r.db == nil || r.guard == nil {
		return bots.ErrUnavailable
	}
	_, e := r.db.ExecContext(ctx, `SELECT account_id,app_id,project_id,controlled_disabled_version FROM owned_bots LIMIT 0; SELECT commands,subscribed_events FROM owned_bot_chat LIMIT 0; SELECT package_file_id,package_fingerprint,download_count FROM owned_bot_miniapps LIMIT 0; SELECT publisher_id,bots_total,miniapps_total,starts_at,expires_at FROM owned_bot_quotas LIMIT 0; SELECT actor_id,app_id,action,target_id,occurred_at FROM owned_bot_audit LIMIT 0`)
	return e
}

var _ bots.Repository = (*Repository)(nil)

func mapped(e error) error {
	if errors.Is(e, sql.ErrNoRows) {
		return bots.ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(e, &pg) && (pg.Code == "23505" || pg.Code == "23503") {
		return bots.ErrConflict
	}
	return e
}
func newID() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func encoded(v any) []byte { b, _ := json.Marshal(v); return b }
func audit(ctx context.Context, tx *sql.Tx, s apps.Scope, action, target string, at time.Time) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_bot_audit(actor_id,app_id,action,target_id,occurred_at)VALUES($1,$2,$3,$4,$5)`, s.Actor, s.App, action, target, at)
	return e
}
func activeAccount(ctx context.Context, tx *sql.Tx, id string, now time.Time) error {
	var allowed bool
	e := tx.QueryRowContext(ctx, `SELECT NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=$2)AND(p.expires_at IS NULL OR p.expires_at>$2))FROM owned_authority_accounts a WHERE a.id=$1`, id, now).Scan(&allowed)
	if e != nil {
		return mapped(e)
	}
	if !allowed {
		return bots.ErrForbidden
	}
	return nil
}

const ancestry = ` FROM owned_custom_apps a JOIN owned_dev_projects project ON project.id=a.project_id JOIN owned_developers d ON d.id=project.developer_id JOIN owned_publishers pub ON pub.id=d.publisher_id`

// Keep the canonical app adapter's unsupported organization-app boundary for
// imported bot/miniapp rows too; a NULL direct owner grants no authority.
const liveAncestry = `a.deleted_at IS NULL AND project.deleted_at IS NULL AND pub.deleted_at IS NULL AND pub.type=0 AND a.status<>2`

type appScope struct {
	publisher, developer string
	status               int
}

// Scope/identity are always reread after deterministic canonical account locks.
// Bot account IDs are immutable associations, never accepted from a request.
func (r *Repository) within(ctx context.Context, s apps.Scope, target, permission string, role int, fn func(*sql.Tx, appScope, time.Time) error) error {
	if r == nil || r.db == nil || r.guard == nil {
		return bots.ErrUnavailable
	}
	if !authority.ValidID(s.Actor) || !authority.ValidID(s.Project) || !authority.ValidID(s.App) || s.Developer == "" || len(s.Developer) > 256 {
		return bots.ErrInvalid
	}
	if target != "" && !authority.ValidID(target) {
		return bots.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var owner sql.NullString
	e = tx.QueryRowContext(ctx, `SELECT pub.account_id::text`+ancestry+` WHERE a.id=$1 AND project.id=$2 AND `+accountpg.PublisherNamePredicate("pub", "$3")+` AND `+liveAncestry, s.App, s.Project, s.Developer).Scan(&owner)
	if e != nil {
		return mapped(e)
	}
	if !owner.Valid {
		return bots.ErrForbidden
	}
	ids := []string{s.Actor}
	if owner.Valid {
		ids = append(ids, owner.String)
	}
	if target != "" {
		var account string
		e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_bots WHERE id=$1 AND app_id=$2`, target, s.App).Scan(&account)
		if e == nil {
			ids = append(ids, account)
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
	}
	sort.Strings(ids)
	previous := ""
	for _, id := range ids {
		if id == previous {
			continue
		}
		previous = id
		var v string
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, id).Scan(&v); e != nil {
			return mapped(e)
		}
	}
	if permission == "" {
		_, e = r.guard.Authenticate(ctx, r.db, tx, s.Actor)
	} else {
		_, e = r.guard.Authorize(ctx, r.db, tx, s.Actor, permission)
	}
	if e != nil {
		return e
	}
	var now time.Time
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return e
	}
	if owner.Valid {
		if e = activeAccount(ctx, tx, owner.String, now); e != nil {
			return e
		}
	}
	// Lock order matches canonical app mutations: publisher/member, project, app.
	var out appScope
	var currentOwner sql.NullString
	e = tx.QueryRowContext(ctx, `SELECT pub.id::text,d.id::text,pub.account_id::text`+ancestry+` JOIN owned_publisher_members member ON member.publisher_id=pub.id WHERE a.id=$1 AND project.id=$2 AND `+accountpg.PublisherNamePredicate("pub", "$3")+` AND `+liveAncestry+` AND member.account_id=$4 AND member.joined_at IS NOT NULL AND member.revoked_at IS NULL AND member.role>=$5 FOR UPDATE OF pub,member`, s.App, s.Project, s.Developer, s.Actor, role).Scan(&out.publisher, &out.developer, &currentOwner)
	if errors.Is(e, sql.ErrNoRows) {
		return bots.ErrForbidden
	}
	if e != nil {
		return e
	}
	if currentOwner != owner {
		return bots.ErrConflict
	}
	var locked string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_dev_projects WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, s.Project).Scan(&locked); e != nil {
		return mapped(e)
	}
	if e = tx.QueryRowContext(ctx, `SELECT status FROM owned_custom_apps WHERE id=$1 AND project_id=$2 AND deleted_at IS NULL AND status<>2 FOR UPDATE`, s.App, s.Project).Scan(&out.status); e != nil {
		return mapped(e)
	}
	// Lock waits and package reads can cross token/grant time boundaries. Check
	// the original proof again after ancestry locks and immediately before commit.
	revalidate := func() error {
		var e error
		if permission == "" {
			_, e = r.guard.Authenticate(ctx, r.db, tx, s.Actor)
		} else {
			_, e = r.guard.Authorize(ctx, r.db, tx, s.Actor, permission)
		}
		if e != nil {
			return e
		}
		if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
			return e
		}
		if owner.Valid {
			return activeAccount(ctx, tx, owner.String, now)
		}
		return nil
	}
	if e = revalidate(); e != nil {
		return e
	}
	if e = fn(tx, out, now.UTC()); e != nil {
		return mapped(e)
	}
	if e = revalidate(); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *Repository) public(ctx context.Context, fn func(*sql.Tx, time.Time) error) error {
	if r == nil || r.db == nil {
		return bots.ErrUnavailable
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var now time.Time
	if e = tx.QueryRowContext(ctx, `SELECT statement_timestamp()`).Scan(&now); e != nil {
		return e
	}
	if e = fn(tx, now.UTC()); e != nil {
		return mapped(e)
	}
	return tx.Commit()
}
func capacity(ctx context.Context, tx *sql.Tx, publisher string, mini bool, now time.Time) error {
	column := "bots_total"
	table := "owned_bots"
	if mini {
		column = "miniapps_total"
		table = "owned_bot_miniapps"
	}
	var total, used int
	e := tx.QueryRowContext(ctx, `SELECT `+column+` FROM owned_bot_quotas WHERE publisher_id=$1 AND starts_at<=$2 AND(expires_at IS NULL OR expires_at>$2)FOR UPDATE`, publisher, now).Scan(&total)
	if errors.Is(e, sql.ErrNoRows) {
		return bots.ErrQuota
	}
	if e != nil {
		return e
	}
	e = tx.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` b JOIN owned_custom_apps a ON a.id=b.app_id JOIN owned_dev_projects p ON p.id=a.project_id JOIN owned_developers d ON d.id=p.developer_id WHERE d.publisher_id=$1 AND b.deleted_at IS NULL`, publisher).Scan(&used)
	if e != nil {
		return e
	}
	if used >= total {
		return bots.ErrQuota
	}
	return nil
}
func developer(ctx context.Context, tx *sql.Tx, app string) (v bots.Developer, e error) {
	var p accounts.Publisher
	var id *string
	e = tx.QueryRowContext(ctx, `SELECT d.id::text,d.publisher_id::text,pub.name,d.created_at,pub.id::text,pub.name,pub.nick,pub.bio,pub.type,pub.account_id::text,pub.realm_id::text,pub.created_at,pub.updated_at`+ancestry+` WHERE a.id=$1`, app).Scan(&v.ID, &v.PublisherID, &v.Name, &v.CreatedAt, &p.ID, &p.Name, &p.Nick, &p.Bio, &p.Type, &p.AccountID, &id, &p.CreatedAt, &p.UpdatedAt)
	p.RealmID = id
	v.Publisher = &p
	return
}

const publicAncestry = `a.status=1 AND a.deleted_at IS NULL AND project.deleted_at IS NULL AND pub.deleted_at IS NULL AND pub.type=0 AND EXISTS(SELECT 1 FROM owned_authority_accounts owner WHERE owner.id=pub.account_id AND NOT owner.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments pun WHERE pun.account_id=owner.id AND pun.kind='disable_account' AND(pun.starts_at IS NULL OR pun.starts_at<=statement_timestamp())AND(pun.expires_at IS NULL OR pun.expires_at>statement_timestamp())))`
