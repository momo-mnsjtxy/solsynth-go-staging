// SPDX-License-Identifier: AGPL-3.0-only
package merchantpg

import (
	"context"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/wallet/merchants"
)

const definitionFields = `identifier,document,payee_wallet_id::text,enabled`

func (r *Repository) Catalog(ctx context.Context, actor string, q merchants.Query) ([]merchants.Definition, int64, error) {
	if q.Take == 0 {
		q.Take = 20
	}
	if q.Take < 1 || q.Take > 100 || q.Offset < 0 || q.Offset > 100000 {
		return nil, 0, merchants.ErrInvalid
	}
	tx, e := r.begin(ctx, actor, "subscriptions.groups.manage")
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `WITH all_rows AS(SELECT `+definitionFields+` FROM commerce_v1.definitions) SELECT (SELECT count(*) FROM all_rows),p.identifier,p.document,p.payee_wallet_id,p.enabled FROM (SELECT 1) seed LEFT JOIN LATERAL(SELECT * FROM all_rows ORDER BY identifier LIMIT $1 OFFSET $2) p ON true`, q.Take, q.Offset)
	if e != nil {
		return nil, 0, e
	}
	var total int64
	out := []merchants.Definition{}
	for rows.Next() {
		var id, wallet *string
		var enabled *bool
		var raw []byte
		if e = rows.Scan(&total, &id, &raw, &wallet, &enabled); e != nil {
			rows.Close()
			return nil, 0, e
		}
		if id == nil {
			continue
		}
		var d merchants.Definition
		if e = json.Unmarshal(raw, &d.Definition); e != nil {
			rows.Close()
			return nil, 0, e
		}
		if d.Identifier != *id {
			rows.Close()
			return nil, 0, merchants.ErrUnavailable
		}
		d.PaymentWalletID = *wallet
		d.PayeeWalletID = *wallet
		d.Enabled = *enabled
		out = append(out, d)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, 0, e
	}
	return out, total, tx.Commit()
}
func (r *Repository) Definition(ctx context.Context, actor, key string) (merchants.Definition, error) {
	if !merchants.ValidKey(key) {
		return merchants.Definition{}, merchants.ErrInvalid
	}
	tx, e := r.begin(ctx, actor, "subscriptions.groups.manage")
	if e != nil {
		return merchants.Definition{}, e
	}
	defer tx.Rollback()
	d, e := loadDefinition(tx.QueryRowContext(ctx, `SELECT `+definitionFields+` FROM commerce_v1.definitions WHERE identifier=$1 FOR SHARE`, key))
	if e != nil {
		return d, e
	}
	return d, tx.Commit()
}
func (r *Repository) UpsertDefinition(ctx context.Context, actor string, v merchants.DefinitionInput) (merchants.Definition, bool, error) {
	d, e := merchants.ValidateDefinition(v)
	if e != nil {
		return merchants.Definition{}, false, e
	}
	tx, e := r.begin(ctx, actor, "subscriptions.groups.manage")
	if e != nil {
		return merchants.Definition{}, false, e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "merchant:definition:"+v.Identifier); e != nil {
		return merchants.Definition{}, false, e
	}
	var pub *accounts.Publisher
	if v.AppIdentifier != nil {
		b, p, e := r.app(ctx, tx, actor, "", *v.AppIdentifier)
		if e != nil {
			return merchants.Definition{}, false, e
		}
		canonical := "developer.app:" + b.AppID
		d.AppIdentifier = &canonical
		pub = &p
	}
	old, e := loadDefinition(tx.QueryRowContext(ctx, `SELECT `+definitionFields+` FROM commerce_v1.definitions WHERE identifier=$1 FOR UPDATE`, v.Identifier))
	created := errors.Is(e, merchants.ErrNotFound)
	if e != nil && !created {
		return merchants.Definition{}, false, e
	}
	if !created {
		if (old.AppIdentifier == nil) != (d.AppIdentifier == nil) || old.AppIdentifier != nil && *old.AppIdentifier != *d.AppIdentifier {
			return merchants.Definition{}, false, merchants.ErrConflict
		}
		if e = r.wallet(ctx, tx, actor, old.PaymentWalletID, pub); e != nil {
			return merchants.Definition{}, false, e
		}
	}
	if e = r.wallet(ctx, tx, actor, v.PayeeWalletID, pub); e != nil {
		return merchants.Definition{}, false, e
	}
	enabled := true
	if v.Enabled != nil {
		enabled = *v.Enabled
	}
	raw, e := json.Marshal(d)
	if e != nil {
		return merchants.Definition{}, false, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO commerce_v1.definitions(identifier,document,payee_wallet_id,enabled) VALUES($1,$2,$3,$4) ON CONFLICT(identifier) DO UPDATE SET document=EXCLUDED.document,payee_wallet_id=EXCLUDED.payee_wallet_id,enabled=EXCLUDED.enabled`, d.Identifier, raw, d.PayeeWalletID, enabled)
	if e != nil {
		return merchants.Definition{}, false, mapped(e)
	}
	if e = audit(ctx, tx, actor, d.Identifier, "catalog.upsert", r.now().UTC()); e != nil {
		return merchants.Definition{}, false, e
	}
	out := merchants.Definition{Definition: d, PaymentWalletID: d.PayeeWalletID, Enabled: enabled}
	return out, created, tx.Commit()
}
func (r *Repository) DeleteDefinition(ctx context.Context, actor, key string) error {
	if !merchants.ValidKey(key) {
		return merchants.ErrInvalid
	}
	tx, e := r.begin(ctx, actor, "subscriptions.groups.manage")
	if e != nil {
		return e
	}
	defer tx.Rollback()
	d, e := loadDefinition(tx.QueryRowContext(ctx, `SELECT `+definitionFields+` FROM commerce_v1.definitions WHERE identifier=$1 FOR UPDATE`, key))
	if e != nil {
		return e
	}
	if e = r.wallet(ctx, tx, actor, d.PaymentWalletID, nil); e != nil {
		return e
	}
	if !d.Enabled {
		return tx.Commit()
	}
	if _, e = tx.ExecContext(ctx, `UPDATE commerce_v1.definitions SET enabled=false WHERE identifier=$1`, key); e != nil {
		return e
	}
	if e = audit(ctx, tx, actor, key, "catalog.disable", r.now().UTC()); e != nil {
		return e
	}
	return tx.Commit()
}
