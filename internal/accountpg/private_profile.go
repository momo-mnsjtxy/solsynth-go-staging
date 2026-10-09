// SPDX-License-Identifier: AGPL-3.0-only
package accountpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"time"
)

func (r *Repository) BindAuthority(ctx context.Context, p authority.Principal) context.Context {
	if b, ok := r.guard.(interface {
		Bind(context.Context, authority.Principal) context.Context
	}); ok {
		return b.Bind(ctx, p)
	}
	return ctx
}
func (r *Repository) LookupSelf(ctx context.Context, actor string) (out accounts.Account, e error) {
	e = r.write(ctx, actor, "", func(tx *sql.Tx, _ time.Time) error {
		var e error
		out, e = lookupAccount(ctx, tx, actor)
		if e != nil {
			return e
		}
		if out.Profile != nil {
			e = tx.QueryRowContext(ctx, `SELECT birthday FROM owned_accounts_profiles WHERE account_id=$1::uuid`, actor).Scan(&out.Profile.Birthday)
		}
		if e == nil && r.profileMedia != nil {
			e = r.profileMedia.Load(ctx, tx, actor, &out)
		}
		return e
	})
	return
}
