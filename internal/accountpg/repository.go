// SPDX-License-Identifier: AGPL-3.0-only
package accountpg

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"strings"
	"time"
)

//go:embed schema.sql
var Schema string

// New never migrates or provisions accounts, grants, entitlements or quota.
type Repository struct {
	db  *sql.DB
	now func() time.Time
}

func New(db *sql.DB) *Repository { return &Repository{db: db, now: time.Now} }

var _ accounts.Repository = (*Repository)(nil)

type scanner interface{ Scan(...any) error }
type querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func mapped(e error) error {
	if errors.Is(e, sql.ErrNoRows) {
		return accounts.ErrNotFound
	}
	var p *pgconn.PgError
	if errors.As(e, &p) && p.Code == "23505" {
		return accounts.ErrConflict
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

const accountColumns = `a.id::text,a.name,a.nick,a.language,a.region,a.created_at,p.id::text,p.bio,p.pronouns,p.location,p.time_zone,p.links,p.updated_at`

func scanAccount(s scanner) (accounts.Account, error) {
	var a accounts.Account
	var id, bio, pronouns, location, zone sql.NullString
	var links []byte
	var updated sql.NullTime
	e := s.Scan(&a.ID, &a.Name, &a.Nick, &a.Language, &a.Region, &a.CreatedAt, &id, &bio, &pronouns, &location, &zone, &links, &updated)
	if e != nil {
		return a, mapped(e)
	}
	if id.Valid {
		a.Profile = &accounts.Profile{ID: id.String, Bio: bio.String, Pronouns: pronouns.String, Location: location.String, TimeZone: zone.String}
		if e = json.Unmarshal(links, &a.Profile.Links); e != nil {
			return accounts.Account{}, e
		}
		if a.Profile.Links == nil {
			a.Profile.Links = []accounts.Link{}
		}
	}
	if updated.Valid {
		a.UpdatedAt = &updated.Time
	}
	return a, nil
}
func lookupAccount(ctx context.Context, q querier, ref string) (accounts.Account, error) {
	if ref == "" || len(ref) > 256 {
		return accounts.Account{}, accounts.ErrNotFound
	}
	match := `lower(a.name)=lower($1)`
	if authority.ValidID(ref) {
		// UUID references identify only the canonical account, never another
		// account whose display name happens to resemble that UUID.
		match = `a.id=$1::uuid`
	}
	return scanAccount(q.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM owned_authority_accounts a LEFT JOIN owned_accounts_profiles p ON p.account_id=a.id WHERE NOT a.disabled AND `+match, ref))
}
func (r *Repository) LookupAccount(ctx context.Context, ref string) (accounts.Account, error) {
	if r == nil || r.db == nil {
		return accounts.Account{}, accounts.ErrUnavailable
	}
	return lookupAccount(ctx, r.db, ref)
}
func (r *Repository) write(ctx context.Context, actor string, fn func(*sql.Tx, time.Time) error) error {
	if r == nil || r.db == nil {
		return accounts.ErrUnavailable
	}
	if !authority.ValidID(actor) {
		return accounts.ErrForbidden
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var disabled bool
	if e = tx.QueryRowContext(ctx, `SELECT disabled FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, actor).Scan(&disabled); e != nil {
		return mapped(e)
	}
	if disabled {
		return accounts.ErrForbidden
	}
	if e = fn(tx, r.now().UTC()); e != nil {
		return mapped(e)
	}
	return tx.Commit()
}
func audit(ctx context.Context, tx *sql.Tx, actor, action, target string, now time.Time) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_accounts_audit(actor_id,action,target_id,occurred_at) VALUES($1,$2,$3,$4)`, actor, action, target, now)
	return e
}
func (r *Repository) UpdateProfile(ctx context.Context, actor string, p accounts.ProfilePatch) (accounts.Account, error) {
	var a accounts.Account
	if e := accounts.ValidateProfile(p); e != nil {
		return a, e
	}
	e := r.write(ctx, actor, func(tx *sql.Tx, now time.Time) error {
		var e error
		a, e = lookupAccount(ctx, tx, actor)
		if e != nil {
			return e
		}
		if p.Nick != nil {
			a.Nick = *p.Nick
		}
		if p.Language != nil {
			a.Language = *p.Language
		}
		if p.Region != nil {
			a.Region = *p.Region
		}
		if a.Profile == nil {
			id, e := newID()
			if e != nil {
				return e
			}
			a.Profile = &accounts.Profile{ID: id, Links: []accounts.Link{}}
		}
		v := a.Profile
		if p.Bio != nil {
			v.Bio = *p.Bio
		}
		if p.Pronouns != nil {
			v.Pronouns = *p.Pronouns
		}
		if p.Location != nil {
			v.Location = *p.Location
		}
		if p.TimeZone != nil {
			v.TimeZone = *p.TimeZone
		}
		if p.Links != nil {
			v.Links = *p.Links
			if v.Links == nil {
				v.Links = []accounts.Link{}
			}
		}
		b, e := json.Marshal(v.Links)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_authority_accounts SET nick=$2,language=$3,region=$4 WHERE id=$1`, actor, a.Nick, a.Language, a.Region); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_accounts_profiles(account_id,id,bio,pronouns,location,time_zone,links,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(account_id) DO UPDATE SET bio=EXCLUDED.bio,pronouns=EXCLUDED.pronouns,location=EXCLUDED.location,time_zone=EXCLUDED.time_zone,links=EXCLUDED.links,updated_at=EXCLUDED.updated_at`, actor, v.ID, v.Bio, v.Pronouns, v.Location, v.TimeZone, b, now); e != nil {
			return e
		}
		a.UpdatedAt = &now
		return audit(ctx, tx, actor, "accounts.profile.update", actor, now)
	})
	return a, e
}

const publisherColumns = `p.id::text,p.type,p.name,p.nick,p.bio,p.account_id::text,p.realm_id::text,p.created_at,p.updated_at`

func scanPublisher(s scanner) (accounts.Publisher, error) {
	var p accounts.Publisher
	e := s.Scan(&p.ID, &p.Type, &p.Name, &p.Nick, &p.Bio, &p.AccountID, &p.RealmID, &p.CreatedAt, &p.UpdatedAt)
	return p, mapped(e)
}
func lookupPublisher(ctx context.Context, q querier, ref string, lock bool) (accounts.Publisher, error) {
	if ref == "" || len(ref) > 256 {
		return accounts.Publisher{}, accounts.ErrNotFound
	}
	suffix := ""
	if lock {
		suffix = " FOR UPDATE OF p"
	}
	return scanPublisher(q.QueryRowContext(ctx, `SELECT `+publisherColumns+` FROM owned_publishers p WHERE p.deleted_at IS NULL AND (p.id::text=$1 OR lower(p.name)=lower($1)) AND (p.account_id IS NULL OR EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id=p.account_id AND NOT a.disabled))`+suffix, ref))
}
func hydrate(ctx context.Context, q querier, p *accounts.Publisher) error {
	if p.AccountID != nil {
		a, e := lookupAccount(ctx, q, *p.AccountID)
		if e != nil {
			return e
		}
		p.Account = &a
	}
	return nil
}
func (r *Repository) LookupPublisher(ctx context.Context, ref string) (accounts.Publisher, error) {
	if r == nil || r.db == nil {
		return accounts.Publisher{}, accounts.ErrUnavailable
	}
	p, e := lookupPublisher(ctx, r.db, ref, false)
	if e == nil {
		e = hydrate(ctx, r.db, &p)
	}
	return p, e
}
func (r *Repository) ListPublishers(ctx context.Context, account string) ([]accounts.Publisher, error) {
	if r == nil || r.db == nil {
		return nil, accounts.ErrUnavailable
	}
	if !authority.ValidID(account) {
		return nil, accounts.ErrInvalid
	}
	if _, e := r.LookupAccount(ctx, account); e != nil {
		return nil, e
	}
	rows, e := r.db.QueryContext(ctx, `SELECT `+publisherColumns+` FROM owned_publishers p JOIN owned_publisher_members m ON m.publisher_id=p.id LEFT JOIN owned_publishing_settings s ON s.account_id=m.account_id WHERE m.account_id=$1 AND m.revoked_at IS NULL AND m.joined_at IS NOT NULL AND p.deleted_at IS NULL AND (p.account_id IS NULL OR EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id=p.account_id AND NOT a.disabled)) ORDER BY (s.default_publisher_id=p.id) DESC NULLS LAST,p.created_at,p.id`, account)
	if e != nil {
		return nil, e
	}
	items := []accounts.Publisher{}
	for rows.Next() {
		p, e := scanPublisher(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		items = append(items, p)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	for i := range items {
		if e = hydrate(ctx, r.db, &items[i]); e != nil {
			return nil, e
		}
	}
	return items, nil
}

const memberColumns = `m.id::text,m.publisher_id::text,m.account_id::text,m.role,m.joined_at,m.created_at,m.updated_at`

func scanMember(s scanner) (accounts.Member, error) {
	var m accounts.Member
	e := s.Scan(&m.ID, &m.PublisherID, &m.AccountID, &m.Role, &m.JoinedAt, &m.CreatedAt, &m.UpdatedAt)
	return m, mapped(e)
}
func membership(ctx context.Context, q querier, publisher, account string) (accounts.Member, error) {
	if !authority.ValidID(account) || !authority.ValidID(publisher) {
		return accounts.Member{}, accounts.ErrInvalid
	}
	return scanMember(q.QueryRowContext(ctx, `SELECT `+memberColumns+` FROM owned_publisher_members m JOIN owned_publishers p ON p.id=m.publisher_id JOIN owned_authority_accounts a ON a.id=m.account_id WHERE m.publisher_id=$1 AND m.account_id=$2 AND m.revoked_at IS NULL AND p.deleted_at IS NULL AND NOT a.disabled AND (p.account_id IS NULL OR EXISTS(SELECT 1 FROM owned_authority_accounts owner WHERE owner.id=p.account_id AND NOT owner.disabled))`, publisher, account))
}
func (r *Repository) Membership(ctx context.Context, publisher, account string) (accounts.Member, error) {
	if r == nil || r.db == nil {
		return accounts.Member{}, accounts.ErrUnavailable
	}
	return membership(ctx, r.db, publisher, account)
}
func require(ctx context.Context, q querier, publisher, actor string, role accounts.Role) error {
	m, e := membership(ctx, q, publisher, actor)
	if errors.Is(e, accounts.ErrNotFound) {
		return accounts.ErrForbidden
	}
	if e != nil {
		return e
	}
	if m.JoinedAt == nil || m.Role < role {
		return accounts.ErrForbidden
	}
	return nil
}
func (r *Repository) RequireManager(ctx context.Context, actor, publisher string) error {
	if r == nil || r.db == nil {
		return accounts.ErrUnavailable
	}
	return require(ctx, r.db, publisher, actor, accounts.Manager)
}
func (r *Repository) Resolve(ctx context.Context, actor, selector string) (string, error) {
	if r == nil || r.db == nil {
		return "", accounts.ErrUnavailable
	}
	if !authority.ValidID(actor) {
		return "", accounts.ErrForbidden
	}
	if selector != "" {
		p, e := lookupPublisher(ctx, r.db, selector, false)
		if e != nil {
			return "", e
		}
		if e = require(ctx, r.db, p.ID, actor, accounts.Viewer); e != nil {
			return "", e
		}
		return p.ID, nil
	}
	var id string
	e := r.db.QueryRowContext(ctx, `SELECT p.id::text FROM owned_publishing_settings s JOIN owned_publishers p ON p.id=s.default_publisher_id JOIN owned_publisher_members m ON m.publisher_id=p.id AND m.account_id=s.account_id JOIN owned_authority_accounts a ON a.id=m.account_id WHERE s.account_id=$1 AND NOT a.disabled AND p.deleted_at IS NULL AND m.revoked_at IS NULL AND m.joined_at IS NOT NULL AND (p.account_id IS NULL OR EXISTS(SELECT 1 FROM owned_authority_accounts owner WHERE owner.id=p.account_id AND NOT owner.disabled))`, actor).Scan(&id)
	if e == nil {
		return id, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return "", e
	}
	ps, e := r.ListPublishers(ctx, actor)
	if e != nil {
		return "", e
	}
	if len(ps) == 0 {
		return "", accounts.ErrForbidden
	}
	if len(ps) != 1 {
		return "", accounts.ErrAmbiguous
	}
	return ps[0].ID, nil
}
func (r *Repository) PublisherEntitlement(ctx context.Context, id string) (accounts.Entitlement, error) {
	var out accounts.Entitlement
	if r == nil || r.db == nil {
		return out, accounts.ErrUnavailable
	}
	if !authority.ValidID(id) {
		return out, accounts.ErrInvalid
	}
	e := r.db.QueryRowContext(ctx, `SELECT e.protected_tag_total,e.level,e.perk_level FROM owned_publisher_entitlements e JOIN owned_publishers p ON p.id=e.publisher_id WHERE e.publisher_id=$1 AND p.deleted_at IS NULL AND (p.account_id IS NULL OR EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id=p.account_id AND NOT a.disabled)) AND e.starts_at<=CURRENT_TIMESTAMP AND(e.expires_at IS NULL OR e.expires_at>CURRENT_TIMESTAMP)`, id).Scan(&out.Total, &out.Level, &out.PerkLevel)
	if errors.Is(e, sql.ErrNoRows) {
		e = accounts.ErrPolicy
	}
	return out, e
}
func lower(s string) string { return strings.ToLower(s) }
