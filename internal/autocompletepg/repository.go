// SPDX-License-Identifier: AGPL-3.0-only
package autocompletepg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/internal/stickerpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/autocomplete"
	"strings"
)

type Config struct {
	Stickers *stickerpg.Repository
	Rooms    autocomplete.RoomPolicy
}
type Repository struct {
	db       *sql.DB
	guard    *authoritypg.TransactionGuard
	accounts *accountpg.Repository
	social   *socialpg.Repository
	config   Config
}

func New(db *sql.DB, guard *authoritypg.TransactionGuard, c Config) (*Repository, error) {
	if db == nil || guard == nil {
		return nil, autocomplete.ErrInvalid
	}
	v := socialpg.New(db)
	return &Repository{db, guard, accountpg.New(db).WithRealms(v), v, c}, nil
}
func (r *Repository) BindAuthority(ctx context.Context, p authority.Principal) context.Context {
	return r.guard.Bind(ctx, p)
}
func (r *Repository) Ready(ctx context.Context) error {
	if r == nil {
		return autocomplete.ErrUnavailable
	}
	var ready bool
	e := r.db.QueryRowContext(ctx, `SELECT bool_and(to_regclass(name)IS NOT NULL)FROM(VALUES('owned_authority_accounts'),('owned_accounts_profiles'),('owned_publishers'),('owned_realms'),('owned_realm_members'),('owned_social_relationships'),('owned_timeline_preferences'))required(name)`).Scan(&ready)
	if e != nil {
		return e
	}
	if !ready {
		return autocomplete.ErrUnavailable
	}
	return nil
}
func (r *Repository) room(ctx context.Context, tx *sql.Tx, actor, id string) error {
	if id == "" {
		return nil
	}
	if r.config.Rooms == nil {
		return autocomplete.ErrUnavailable
	}
	var joined bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_rooms r JOIN chat_members m ON m.chat_room_id=r.id JOIN owned_authority_accounts owner ON owner.id=r.owned_owner_id WHERE r.id=$1 AND r.deleted_at IS NULL AND NOT owner.disabled AND m.account_id=$2 AND m.joined_at IS NOT NULL AND m.leave_at IS NULL AND m.deleted_at IS NULL AND NOT m.owned_banned)`, id, actor).Scan(&joined)
	if e != nil {
		return e
	}
	if !joined {
		return autocomplete.ErrForbidden
	}
	yes, e := r.config.Rooms.CanReadInTransaction(ctx, tx, actor, id)
	if e != nil {
		return e
	}
	if !yes {
		return autocomplete.ErrForbidden
	}
	return nil
}

// Expressions passed here are compile-time canonical aliases, never request input.
func allowedAccount(account string) string {
	return `NOT EXISTS(SELECT 1 FROM owned_social_relationships b WHERE b.status=-100 AND((b.account_id=$1::uuid AND b.related_id=` + account + `)OR(b.related_id=$1::uuid AND b.account_id=` + account + `))) AND NOT EXISTS(SELECT 1 FROM owned_timeline_preferences x WHERE x.account_id=$1::uuid AND x.kind='account' AND x.reference_id=` + account + `) AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments x WHERE x.account_id=` + account + ` AND x.kind='disable_account' AND(x.starts_at IS NULL OR x.starts_at<=statement_timestamp())AND(x.expires_at IS NULL OR x.expires_at>statement_timestamp()))`
}
func (r *Repository) candidates(ctx context.Context, tx *sql.Tx, actor, room string, q autocomplete.Query) ([]string, error) {
	var query string
	args := []any{actor, strings.ToLower(q.Text)}
	switch q.Kind {
	case "user":
		query = `SELECT a.id::text FROM owned_authority_accounts a WHERE NOT a.disabled AND(left(lower(a.name),length($2))=$2 OR left(lower(a.nick),length($2))=$2) AND ` + allowedAccount("a.id") + ` AND($3::text='' OR EXISTS(SELECT 1 FROM chat_members m WHERE m.account_id=a.id AND m.chat_room_id=NULLIF($3::text,'')::uuid AND m.joined_at IS NOT NULL AND m.leave_at IS NULL AND m.deleted_at IS NULL AND NOT m.owned_banned)) ORDER BY lower(a.name),a.id LIMIT 10`
		args = append(args, room)
	case "publisher":
		query = `SELECT p.id::text FROM owned_publishers p WHERE p.deleted_at IS NULL AND(p.account_id IS NULL OR EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id=p.account_id AND NOT a.disabled)) AND(left(lower(p.name),length($2))=$2 OR left(lower(p.nick),length($2))=$2) AND ` + r.social.PublisherVisibilitySQL("p", "$1") + ` AND ` + allowedAccount("p.account_id") + ` AND NOT EXISTS(SELECT 1 FROM owned_timeline_preferences x WHERE x.account_id=$1::uuid AND((x.kind='publisher' AND x.reference_id=p.id)OR(x.kind='realm' AND x.reference_id=p.realm_id))) ORDER BY lower(p.name),p.id LIMIT 10`
	case "realm":
		query = `SELECT r.id::text FROM owned_realms r JOIN owned_authority_accounts a ON a.id=r.account_id WHERE r.deleted_at IS NULL AND NOT a.disabled AND(left(lower(r.slug),length($2))=$2 OR left(lower(r.name),length($2))=$2) AND(r.is_public OR EXISTS(SELECT 1 FROM owned_realm_members m WHERE m.realm_id=r.id AND m.account_id=$1::uuid AND m.joined_at IS NOT NULL AND m.leave_at IS NULL)) AND ` + allowedAccount("r.account_id") + ` AND NOT EXISTS(SELECT 1 FROM owned_timeline_preferences x WHERE x.account_id=$1::uuid AND x.kind='realm' AND x.reference_id=r.id) ORDER BY lower(r.slug),r.id LIMIT 10`
	default:
		return []string{}, nil
	}
	rows, e := tx.QueryContext(ctx, query, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (r *Repository) Suggest(ctx context.Context, actor, room, content string) ([]autocomplete.Suggestion, error) {
	if r == nil || r.db == nil {
		return nil, autocomplete.ErrUnavailable
	}
	if !authority.ValidID(actor) {
		return nil, autocomplete.ErrUnauthenticated
	}
	if room != "" && !authority.ValidID(room) {
		return nil, autocomplete.ErrInvalid
	}
	q, e := autocomplete.Parse(content)
	if e != nil {
		return nil, e
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if _, e = r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
		return nil, e
	}
	if e = r.room(ctx, tx, actor, room); e != nil {
		return nil, e
	}
	out := []autocomplete.Suggestion{}
	if q.Kind == "sticker" {
		if r.config.Stickers == nil {
			return nil, autocomplete.ErrUnavailable
		}
		items, e := r.config.Stickers.SuggestionsInTransaction(ctx, tx, actor, q.Text, 10)
		if e != nil {
			return nil, e
		}
		for _, item := range items {
			out = append(out, autocomplete.Suggestion{Type: "sticker", Keyword: ":" + item.Identifier + ":", Data: item})
		}
	} else {
		ids, e := r.candidates(ctx, tx, actor, room, q)
		if e != nil {
			return nil, e
		}
		for _, id := range ids {
			var data any
			var name string
			switch q.Kind {
			case "user":
				v, err := r.accounts.AccountInTransaction(ctx, tx, actor, id)
				e = err
				data, name = v, v.Name
			case "publisher":
				v, err := r.accounts.PublisherForViewerInTransaction(ctx, tx, actor, id)
				e = err
				data, name = v, v.Name
			case "realm":
				v, err := r.social.RealmForViewerInTransaction(ctx, tx, actor, id)
				e = err
				data, name = v, v.Slug
			}
			if errors.Is(e, accounts.ErrNotFound) || errors.Is(e, social.ErrNotFound) {
				continue
			}
			if e != nil {
				return nil, e
			}
			out = append(out, autocomplete.Suggestion{Type: q.Kind, Keyword: q.Prefix + name, Data: data})
		}
	}
	if e = r.room(ctx, tx, actor, room); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return out, nil
}
