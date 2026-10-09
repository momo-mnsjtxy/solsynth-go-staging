// SPDX-License-Identifier: AGPL-3.0-only
package merchantpg

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/wallet/merchants"
	"strings"
	"time"
)

//go:embed schema.sql
var Schema string

type Repository struct {
	db         *sql.DB
	guard      *authoritypg.TransactionGuard
	publishers *accountpg.Repository
	social     *socialpg.Repository
	now        func() time.Time
}

func New(db *sql.DB, g *authoritypg.TransactionGuard) *Repository {
	if g == nil {
		g = authoritypg.NewTransactionGuard(nil, nil)
	}
	return &Repository{db: db, guard: g, publishers: accountpg.New(db), social: socialpg.New(db), now: time.Now}
}
func (r *Repository) WithClock(now func() time.Time) *Repository {
	if now != nil {
		r.now = now
	}
	return r
}
func (r *Repository) BindAuthority(ctx context.Context, p authority.Principal) context.Context {
	return r.guard.Bind(ctx, p)
}

var _ merchants.Repository = (*Repository)(nil)

func mapped(e error) error {
	if errors.Is(e, sql.ErrNoRows) {
		return merchants.ErrNotFound
	}
	var p *pgconn.PgError
	if errors.As(e, &p) && p.Code == "23505" {
		return merchants.ErrConflict
	}
	return e
}
func (r *Repository) begin(ctx context.Context, actor, permission string) (*sql.Tx, error) {
	if r == nil || r.db == nil || r.guard == nil {
		return nil, merchants.ErrUnavailable
	}
	if !authority.ValidID(actor) {
		return nil, merchants.ErrUnauthenticated
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return nil, e
	}
	if permission == "" {
		_, e = r.guard.Authenticate(ctx, r.db, tx, actor)
	} else {
		_, e = r.guard.Authorize(ctx, r.db, tx, actor, permission)
	}
	if e != nil {
		tx.Rollback()
		return nil, e
	}
	return tx, nil
}
func audit(ctx context.Context, tx *sql.Tx, actor, key, action string, now time.Time) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_merchant_management_audit(actor_id,resource_key,action,occurred_at) VALUES($1,$2,$3,$4)`, actor, key, action, now)
	return e
}
func (r *Repository) publisher(ctx context.Context, tx *sql.Tx, actor, selector string) (accounts.Publisher, error) {
	p, e := r.publishers.RequirePostEditor(ctx, tx, actor, selector)
	if e != nil {
		return p, e
	}
	var manager bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publisher_members WHERE publisher_id=$1 AND account_id=$2 AND role>=$3 AND joined_at IS NOT NULL AND revoked_at IS NULL)`, p.ID, actor, accounts.Manager).Scan(&manager)
	if e != nil {
		return p, e
	}
	if !manager {
		return accounts.Publisher{}, merchants.ErrForbidden
	}
	return p, nil
}
func (r *Repository) app(ctx context.Context, tx *sql.Tx, actor, publisher, selector string) (merchants.Binding, accounts.Publisher, error) {
	var out merchants.Binding
	var pub accounts.Publisher
	selector = strings.TrimPrefix(selector, "developer.app:")
	var project, developer, pubID string
	e := tx.QueryRowContext(ctx, `SELECT a.id::text,a.slug,a.project_id::text,d.id::text,d.publisher_id::text FROM owned_custom_apps a JOIN owned_dev_projects p ON p.id=a.project_id JOIN owned_developers d ON d.id=p.developer_id WHERE (a.id::text=$1 OR lower(a.slug)=lower($1)) AND a.deleted_at IS NULL AND p.deleted_at IS NULL`, selector).Scan(&out.AppID, &out.AppSlug, &project, &developer, &pubID)
	if e != nil {
		return out, pub, mapped(e)
	}
	if publisher == "" {
		publisher = pubID
	}
	pub, e = r.publisher(ctx, tx, actor, publisher)
	if e != nil {
		return out, pub, e
	}
	if pub.ID != pubID {
		return out, pub, merchants.ErrNotFound
	}
	out.PublisherID = pub.ID
	var valid bool
	if e = tx.QueryRowContext(ctx, `SELECT true FROM owned_dev_projects WHERE id=$1 AND developer_id=$2 AND deleted_at IS NULL FOR SHARE`, project, developer).Scan(&valid); e != nil {
		return out, pub, mapped(e)
	}
	if e = tx.QueryRowContext(ctx, `SELECT true FROM owned_custom_apps WHERE id=$1 AND project_id=$2 AND deleted_at IS NULL FOR SHARE`, out.AppID, project).Scan(&valid); e != nil {
		return out, pub, mapped(e)
	}
	return out, pub, nil
}
func (r *Repository) wallet(ctx context.Context, tx *sql.Tx, actor, id string, pub *accounts.Publisher) error {
	if !authority.ValidID(id) {
		return merchants.ErrInvalid
	}
	var owner, realm *string
	if e := tx.QueryRowContext(ctx, `SELECT account_id::text,realm_id::text FROM wallet_v1.wallets WHERE id=$1 AND deleted_at IS NULL`, id).Scan(&owner, &realm); e != nil {
		return mapped(e)
	}
	if owner != nil {
		if *owner != actor {
			return merchants.ErrForbidden
		}
	} else {
		if realm == nil {
			return merchants.ErrForbidden
		}
		role, e := r.social.RealmRoleForWriteInTransaction(ctx, tx, actor, *realm)
		if e != nil {
			if errors.Is(e, social.ErrForbidden) || errors.Is(e, social.ErrNotFound) {
				return merchants.ErrForbidden
			}
			return e
		}
		if role != social.Owner {
			return merchants.ErrForbidden
		}
	}
	var currentOwner, currentRealm *string
	if e := tx.QueryRowContext(ctx, `SELECT account_id::text,realm_id::text FROM wallet_v1.wallets WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, id).Scan(&currentOwner, &currentRealm); e != nil {
		return mapped(e)
	}
	if (owner == nil) != (currentOwner == nil) || (realm == nil) != (currentRealm == nil) || owner != nil && *owner != *currentOwner || realm != nil && *realm != *currentRealm {
		return merchants.ErrConflict
	}
	if pub != nil {
		same := pub.AccountID != nil && owner != nil && *pub.AccountID == *owner || pub.RealmID != nil && realm != nil && *pub.RealmID == *realm
		if !same {
			return merchants.ErrForbidden
		}
	}
	return nil
}

func (r *Repository) Profile(ctx context.Context, actor, selector string) (merchants.Profile, error) {
	tx, e := r.begin(ctx, actor, "")
	if e != nil {
		return merchants.Profile{}, e
	}
	defer tx.Rollback()
	p, e := r.publisher(ctx, tx, actor, selector)
	if e != nil {
		return merchants.Profile{}, e
	}
	out := merchants.Profile{PublisherID: p.ID, Name: p.Name, Bindings: []merchants.Binding{}}
	rows, e := tx.QueryContext(ctx, `SELECT a.id::text,a.slug,m.wallet_id::text FROM owned_custom_apps a JOIN owned_dev_projects p ON p.id=a.project_id JOIN owned_developers d ON d.id=p.developer_id LEFT JOIN commerce_v1.merchants m ON m.app_id=a.id WHERE d.publisher_id=$1 AND a.deleted_at IS NULL AND p.deleted_at IS NULL ORDER BY a.slug,a.id`, p.ID)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var b merchants.Binding
		b.PublisherID = p.ID
		if e = rows.Scan(&b.AppID, &b.AppSlug, &b.PaymentWalletID); e != nil {
			rows.Close()
			return merchants.Profile{}, e
		}
		out.Bindings = append(out.Bindings, b)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return merchants.Profile{}, e
	}
	return out, tx.Commit()
}
func (r *Repository) Binding(ctx context.Context, actor, publisher, app string) (merchants.Binding, error) {
	tx, e := r.begin(ctx, actor, "")
	if e != nil {
		return merchants.Binding{}, e
	}
	defer tx.Rollback()
	b, _, e := r.app(ctx, tx, actor, publisher, app)
	if e != nil {
		return b, e
	}
	e = tx.QueryRowContext(ctx, `SELECT wallet_id::text FROM commerce_v1.merchants WHERE app_id=$1 FOR SHARE`, b.AppID).Scan(&b.PaymentWalletID)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return b, e
	}
	return b, tx.Commit()
}
func (r *Repository) BindWallet(ctx context.Context, actor, publisher, app string, v merchants.WalletPatch) (merchants.Binding, error) {
	tx, e := r.begin(ctx, actor, "merchants.manage")
	if e != nil {
		return merchants.Binding{}, e
	}
	defer tx.Rollback()
	b, p, e := r.app(ctx, tx, actor, publisher, app)
	if e != nil {
		return b, e
	}
	var prior string
	e = tx.QueryRowContext(ctx, `SELECT wallet_id::text FROM commerce_v1.merchants WHERE app_id=$1 FOR UPDATE`, b.AppID).Scan(&prior)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return b, e
	}
	if v.WalletID == nil {
		_, e = tx.ExecContext(ctx, `DELETE FROM commerce_v1.merchants WHERE app_id=$1`, b.AppID)
	} else {
		if e = r.wallet(ctx, tx, actor, *v.WalletID, &p); e != nil {
			return merchants.Binding{}, e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO commerce_v1.merchants(app_id,wallet_id) VALUES($1,$2) ON CONFLICT(app_id) DO UPDATE SET wallet_id=EXCLUDED.wallet_id`, b.AppID, *v.WalletID)
	}
	if e != nil {
		return merchants.Binding{}, e
	}
	if e = audit(ctx, tx, actor, b.AppID, "merchant.wallet", r.now().UTC()); e != nil {
		return merchants.Binding{}, e
	}
	b.PaymentWalletID = v.WalletID
	return b, tx.Commit()
}
func loadDefinition(row interface{ Scan(...any) error }) (merchants.Definition, error) {
	var d merchants.Definition
	var raw []byte
	var key string
	e := row.Scan(&key, &raw, &d.PaymentWalletID, &d.Enabled)
	if e != nil {
		return d, mapped(e)
	}
	if e = json.Unmarshal(raw, &d.Definition); e != nil {
		return d, e
	}
	if d.Identifier != key {
		return d, merchants.ErrUnavailable
	}
	d.PayeeWalletID = d.PaymentWalletID
	return d, nil
}
