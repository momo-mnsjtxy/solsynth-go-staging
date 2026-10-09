// SPDX-License-Identifier: AGPL-3.0-only
package realmmemberpg

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	m "solsynth.local/go-migration/ports/passport/realmmembers"
	"solsynth.local/go-migration/ports/passport/social"
	"sort"
	"time"
)

//go:embed schema.sql
var Schema string

type BoostPolicy interface {
	LabelPolicyInTransaction(context.Context, *sql.Tx, string) (int, int, error)
	MemberPerkInTransaction(context.Context, *sql.Tx, string, string, string, time.Time) (int, error)
}
type Config struct {
	// Empty explicitly disables level calculation. Values are cumulative floors,
	// chosen by the owner, not an inferred legacy formula.
	LevelThresholds   []int64
	EnableDailyTenure bool
	Now               func() time.Time
}
type Repository struct {
	db          *sql.DB
	guard       *authoritypg.TransactionGuard
	boosts      BoostPolicy
	permissions social.PermissionProvider
	cfg         Config
}

func ValidateConfig(c Config) error {
	if c.LevelThresholds == nil || len(c.LevelThresholds) > 10000 {
		return m.ErrUnavailable
	}
	for i, n := range c.LevelThresholds {
		if n < 0 || n > 1000000000000000 || i == 0 && n != 0 || i > 0 && n <= c.LevelThresholds[i-1] {
			return m.ErrInvalid
		}
	}
	return nil
}
func New(db *sql.DB, g *authoritypg.TransactionGuard, b BoostPolicy, p social.PermissionProvider, c Config) (*Repository, error) {
	if db == nil || g == nil || b == nil || p == nil {
		return nil, m.ErrUnavailable
	}
	if e := ValidateConfig(c); e != nil {
		return nil, e
	}
	c.LevelThresholds = append([]int64{}, c.LevelThresholds...)
	if c.Now == nil {
		c.Now = time.Now
	}
	return &Repository{db, g, b, p, c}, nil
}
func (r *Repository) Ready(ctx context.Context) error {
	if r == nil {
		return m.ErrUnavailable
	}
	_, e := r.db.ExecContext(ctx, `SELECT id FROM owned_realm_labels LIMIT 0;SELECT member_id,joined_at FROM owned_realm_label_assignments LIMIT 0;SELECT fingerprint FROM owned_realm_label_requests LIMIT 0;SELECT day,delta FROM owned_realm_tenure_experience LIMIT 0;SELECT id FROM owned_realm_member_audit LIMIT 0;SELECT member_id,nick,bio FROM owned_realm_member_profiles LIMIT 0;SELECT id FROM owned_realm_profile_audit LIMIT 0;SELECT id FROM owned_realm_post_moderation LIMIT 0;SELECT thresholds FROM owned_progression_level_policy LIMIT 0;SELECT delta FROM owned_progression_experience LIMIT 0`)
	return e
}
func (r *Repository) BindAuthority(ctx context.Context, p authority.Principal) context.Context {
	return r.guard.Bind(ctx, p)
}
func uid() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func mapped(e error) error {
	if errors.Is(e, sql.ErrNoRows) {
		return m.ErrNotFound
	}
	var p *pgconn.PgError
	if errors.As(e, &p) {
		switch p.Code {
		case "55P03", "40P01", "40001":
			return m.ErrRetry
		case "23505":
			return m.ErrConflict
		}
	}
	return e
}

type scope struct {
	ID, Owner string
	Public    bool
}

func observe(ctx context.Context, tx *sql.Tx, ref string) (v scope, e error) {
	if ref == "" || len(ref) > 256 {
		return v, m.ErrInvalid
	}
	e = tx.QueryRowContext(ctx, `SELECT id::text,account_id::text,is_public FROM owned_realms WHERE deleted_at IS NULL AND(id::text=$1 OR lower(slug)=lower($1))`, ref).Scan(&v.ID, &v.Owner, &v.Public)
	return v, mapped(e)
}
func currentAccount(ctx context.Context, tx *sql.Tx, id string) error {
	var active bool
	e := tx.QueryRowContext(ctx, `SELECT NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp())) FROM owned_authority_accounts a WHERE a.id=$1::uuid`, id).Scan(&active)
	if e != nil {
		return mapped(e)
	}
	if !active {
		return m.ErrForbidden
	}
	return nil
}
func lockAccounts(ctx context.Context, tx *sql.Tx, ids []string) error {
	sort.Strings(ids)
	prior := ""
	for _, id := range ids {
		if id == "" || id == prior {
			continue
		}
		prior = id
		if !authority.ValidID(id) {
			return m.ErrInvalid
		}
		var active bool
		if e := tx.QueryRowContext(ctx, `SELECT NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp())) FROM owned_authority_accounts a WHERE a.id=$1::uuid FOR UPDATE OF a`, id).Scan(&active); e != nil {
			return mapped(e)
		}
		if !active {
			return m.ErrForbidden
		}
	}
	return nil
}
func (r *Repository) begin(ctx context.Context, actor, ref string, write bool, permission string, targets []string) (tx *sql.Tx, v scope, e error) {
	if r == nil {
		return nil, v, m.ErrUnavailable
	}
	if actor != "" && !authority.ValidID(actor) {
		return nil, v, authority.ErrInvalid
	}
	if write && actor == "" {
		return nil, v, authority.ErrInvalid
	}
	tx, e = r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return
	}
	defer func() {
		if e != nil {
			tx.Rollback()
		}
	}()
	if actor != "" {
		perms := []string{}
		if write {
			perms = append(perms, permission)
		}
		if _, e = r.guard.Preflight(ctx, r.db, tx, actor, perms...); e != nil {
			return
		}
	}
	before, e := observe(ctx, tx, ref)
	if e != nil {
		return
	}
	ids := append([]string{actor, before.Owner}, targets...)
	if e = lockAccounts(ctx, tx, ids); e != nil {
		return
	}
	if actor != "" {
		if write {
			_, e = r.guard.Authorize(ctx, r.db, tx, actor, permission)
		} else {
			_, e = r.guard.Authenticate(ctx, r.db, tx, actor)
		}
		if e != nil {
			return
		}
	}
	suffix := " FOR SHARE"
	if write {
		suffix = " FOR UPDATE"
	}
	var held string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_realms WHERE id=$1::uuid AND deleted_at IS NULL`+suffix, before.ID).Scan(&held); e != nil {
		e = mapped(e)
		return
	}
	v, e = observe(ctx, tx, ref)
	if e != nil {
		return
	}
	if v.ID != before.ID || v.Owner != before.Owner {
		e = m.ErrConflict
		return
	}
	for _, id := range ids {
		if id != "" {
			if e = currentAccount(ctx, tx, id); e != nil {
				return
			}
		}
	}
	if write {
		e = r.authorize(ctx, tx, actor, v, permission, false)
	} else {
		if actor != "" {
			if _, e = r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
				return
			}
		}
		if !v.Public {
			var member bool
			e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_realm_members WHERE realm_id=$1::uuid AND account_id=NULLIF($2::text,'')::uuid AND joined_at IS NOT NULL AND leave_at IS NULL AND role>=0)`, v.ID, actor).Scan(&member)
			if e == nil && !member {
				e = m.ErrNotFound
			}
		}
	}
	return
}
func (r *Repository) authorize(ctx context.Context, tx *sql.Tx, actor string, v scope, permission string, boost bool) error {
	if e := currentAccount(ctx, tx, v.Owner); e != nil {
		return e
	}
	if _, e := r.guard.Authorize(ctx, r.db, tx, actor, permission); e != nil {
		return e
	}
	var role int
	if e := tx.QueryRowContext(ctx, `SELECT m.role FROM owned_realm_members m JOIN owned_realms r ON r.id=m.realm_id LEFT JOIN owned_realm_role_permissions p ON p.realm_id=m.realm_id AND p.role_level=m.role WHERE m.realm_id=$1::uuid AND m.account_id=$2::uuid AND m.joined_at IS NOT NULL AND m.leave_at IS NULL AND((r.account_id=m.account_id AND m.role=100)OR(r.account_id<>m.account_id AND m.role<100 AND p.deleted_at IS NULL AND p.generation=m.permission_generation))`, v.ID, actor).Scan(&role); e != nil {
		if errors.Is(e, sql.ErrNoRows) {
			return m.ErrForbidden
		}
		return e
	}
	if role < int(social.Moderator) {
		return m.ErrForbidden
	}
	if permission == "realms.members.manage" {
		if e := r.permissions.RealmPermissionForWriteInTransaction(ctx, tx, actor, v.ID, social.CanManageMembers); e != nil {
			return e
		}
	}
	if boost {
		level, _, e := r.boosts.LabelPolicyInTransaction(ctx, tx, v.ID)
		if e != nil {
			return e
		}
		if level < 1 {
			return m.ErrForbidden
		}
	}
	return nil
}
func audit(ctx context.Context, tx *sql.Tx, actor, realm, target, action string, now time.Time) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_realm_member_audit(actor_id,realm_id,target_id,action,occurred_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::text,$5::timestamptz)`, actor, realm, target, action, now)
	return e
}

var _ m.Repository = (*Repository)(nil)
