// SPDX-License-Identifier: AGPL-3.0-only
package locationpg

import (
	"context"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"math"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/locations"
	"time"
)

//go:embed schema.sql
var Schema string

type Repository struct {
	db              *sql.DB
	guard           *authoritypg.TransactionGuard
	now             func() time.Time
	nfcKeys         cipher.AEAD
	nfcLocker       locations.NFCLocker
	mediaEnabled    bool
	geocoder        locations.Geocoder
	mediaProtection mediapg.FileReadProtection
}

func New(db *sql.DB) *Repository { return &Repository{db: db, now: time.Now} }
func NewWithAuthority(db *sql.DB, g *authoritypg.TransactionGuard) *Repository {
	r := New(db)
	if g == nil {
		g = authoritypg.NewTransactionGuard(nil, nil)
	}
	r.guard = g
	return r
}

// NewWithAuthorityAndClock permits deterministic domain timestamps in tests.
// Security TTL, nearby slots/rates and original proof freshness remain DB timed.
func NewWithAuthorityAndClock(db *sql.DB, g *authoritypg.TransactionGuard, now func() time.Time) *Repository {
	r := NewWithAuthority(db, g)
	if now != nil {
		r.now = now
	}
	return r
}
func (r *Repository) BindAuthority(ctx context.Context, p authority.Principal) context.Context {
	if r.guard != nil {
		return r.guard.Bind(ctx, p)
	}
	return ctx
}

var _ locations.Repository = (*Repository)(nil)

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
		return locations.ErrNotFound
	}
	var p *pgconn.PgError
	if errors.As(e, &p) && p.Code == "23505" {
		return locations.ErrConflict
	}
	return e
}
func (r *Repository) begin(ctx context.Context, actor, permission string, required bool) (*sql.Tx, authority.Principal, error) {
	var p authority.Principal
	if r == nil || r.db == nil {
		return nil, p, locations.ErrUnavailable
	}
	if actor == "" && required || actor != "" && !authority.ValidID(actor) {
		return nil, p, locations.ErrUnauthenticated
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return nil, p, e
	}
	if actor != "" {
		if r.guard != nil {
			if permission == "" {
				p, e = r.guard.Authenticate(ctx, r.db, tx, actor)
			} else {
				p, e = r.guard.Authorize(ctx, r.db, tx, actor, permission)
			}
			if e != nil {
				tx.Rollback()
				return nil, p, e
			}
		}
		var enabled bool
		e = tx.QueryRowContext(ctx, `SELECT id::text,NOT disabled,is_superuser FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, actor).Scan(&p.Account.ID, &enabled, &p.Account.Superuser)
		if e != nil || !enabled {
			tx.Rollback()
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return nil, p, e
			}
			return nil, p, locations.ErrUnauthenticated
		}
	}
	return tx, p, nil
}
func audit(ctx context.Context, tx *sql.Tx, actor, id, action string, now time.Time) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_location_audit(actor_id,resource_id,action,occurred_at) VALUES($1,$2,$3,$4)`, actor, id, action, now)
	return e
}
func account(ctx context.Context, tx *sql.Tx, id string) (accounts.Account, error) {
	var a accounts.Account
	e := tx.QueryRowContext(ctx, `SELECT a.id::text,a.name,a.nick,a.language,a.region,a.created_at FROM owned_authority_accounts a WHERE a.id=$1 AND NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments punishment WHERE punishment.account_id=a.id AND punishment.kind='disable_account' AND(punishment.starts_at IS NULL OR punishment.starts_at<=clock_timestamp()) AND(punishment.expires_at IS NULL OR punishment.expires_at>clock_timestamp()))`, id).Scan(&a.ID, &a.Name, &a.Nick, &a.Language, &a.Region, &a.CreatedAt)
	return a, mapped(e)
}
func socialState(ctx context.Context, tx *sql.Tx, a, b string) (blocked, friend bool, err error) {
	if a == "" || a == b {
		return false, a == b, nil
	}
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_social_effective_relationships WHERE status=-100 AND ((account_id=$1 AND related_id=$2) OR(account_id=$2 AND related_id=$1))),EXISTS(SELECT 1 FROM owned_social_effective_relationships WHERE account_id=$1 AND related_id=$2 AND status>=100) AND EXISTS(SELECT 1 FROM owned_social_effective_relationships WHERE account_id=$2 AND related_id=$1 AND status>=100)`, a, b).Scan(&blocked, &friend)
	return
}
func coordinates(wkt *string) (*float64, *float64, *string, error) {
	if wkt == nil || *wkt == "" {
		return nil, nil, nil, nil
	}
	p, e := locations.ParsePoint(*wkt)
	if e != nil {
		return nil, nil, nil, e
	}
	v := p.WKT()
	return &p.Longitude, &p.Latitude, &v, nil
}
func wkt(lon, lat *float64) *string {
	if lon == nil || lat == nil {
		return nil
	}
	s := (locations.Point{Longitude: *lon, Latitude: *lat}).WKT()
	return &s
}
func meta(v map[string]json.RawMessage) ([]byte, error) {
	if v == nil {
		v = map[string]json.RawMessage{}
	}
	return json.Marshal(v)
}
func pageOK(p locations.Page) bool {
	if math.IsNaN(p.Radius) || math.IsInf(p.Radius, 0) || p.Radius < 0 || p.Radius > 100000 {
		return false
	}
	if p.Take < 1 || p.Take > 100 || p.Offset < 0 || p.Offset > 100000 || p.Status != nil && (*p.Status < 0 || *p.Status > 3) || p.Visibility != nil && (*p.Visibility < 0 || *p.Visibility > 2) {
		return false
	}
	if p.Origin != nil {
		if _, e := locations.ParsePoint(p.Origin.WKT()); e != nil {
			return false
		}
	}
	return !p.Nearby || p.Origin != nil && p.Radius > 0 && p.Radius <= 100000
}
