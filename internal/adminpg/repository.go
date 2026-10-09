// SPDX-License-Identifier: AGPL-3.0-only
package adminpg

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/moderationpg"
	c "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	"time"
)

//go:embed schema.sql
var Schema string

type Repository struct {
	db     *sql.DB
	global *moderationpg.Repository
	now    func() time.Time
}

func New(db *sql.DB, g *authoritypg.TransactionGuard, now func() time.Time) (*Repository, error) {
	if now == nil {
		now = time.Now
	}
	global, e := moderationpg.New(db, g, moderationpg.Config{Now: now})
	if e != nil {
		return nil, e
	}
	return &Repository{db, global, now}, nil
}
func (r *Repository) BindAuthority(ctx context.Context, p authority.Principal) context.Context {
	return r.global.BindAuthority(ctx, p)
}
func newID() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
func mapped(e error) error {
	if errors.Is(e, sql.ErrNoRows) {
		return c.ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(e, &pg) && pg.Code == "23505" {
		return c.ErrConflict
	}
	return e
}
func (r *Repository) audit(ctx context.Context, tx *sql.Tx, actor, kind, target, action string, detail any) error {
	b, e := json.Marshal(detail)
	if e != nil || len(b) > 65536 {
		return c.ErrInvalid
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_admin_extension_audit(actor_id,kind,target_id,action,detail,occurred_at)VALUES($1,$2,$3,$4,$5::jsonb,$6)`, actor, kind, target, action, b, r.now().UTC())
	return e
}
func (r *Repository) Ready(ctx context.Context) error {
	var ready bool
	e := r.db.QueryRowContext(ctx, `SELECT bool_and(to_regclass(name)IS NOT NULL)FROM(VALUES('owned_admin_extension_audit'),('owned_automod_rules'),('owned_automod_state'),('owned_post_automod_events'))required(name)`).Scan(&ready)
	if e != nil {
		return e
	}
	if !ready {
		return c.ErrUnavailable
	}
	_, e = r.db.ExecContext(ctx, `SELECT a.verification,p.verification,r.verification FROM owned_accounts_profiles a CROSS JOIN owned_publishers p CROSS JOIN owned_realms r LIMIT 0;SELECT automod_revision,automod_decision FROM owned_text_posts LIMIT 0`)
	return e
}
