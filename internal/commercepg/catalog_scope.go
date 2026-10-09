// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"strings"
)

// Optional scoped definitions use canonical ancestry, in the same publisher,
// project, app order as normal app checkout. No owner-account lock is added
// after the buyer's authority lock: two users buying each other's app would
// otherwise invert account locks. Owner enablement is re-read after waits.
func lockDefinitionApp(ctx context.Context, tx *sql.Tx, ref *string) error {
	if ref == nil {
		return nil
	}
	id := strings.TrimPrefix(*ref, "developer.app:")
	if *ref != "developer.app:"+id || !authority.ValidID(id) {
		return c.ErrUnavailable
	}
	var project, developer, publisher string
	e := tx.QueryRowContext(ctx, `SELECT a.project_id::text,p.developer_id::text,d.publisher_id::text FROM owned_custom_apps a JOIN owned_dev_projects p ON p.id=a.project_id JOIN owned_developers d ON d.id=p.developer_id WHERE a.id=$1`, id).Scan(&project, &developer, &publisher)
	if e != nil {
		return notFound(e)
	}
	var yes bool
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`SELECT true FROM owned_publishers WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, []any{publisher}},
		{`SELECT true FROM owned_dev_projects WHERE id=$1 AND developer_id=$2 AND deleted_at IS NULL FOR SHARE`, []any{project, developer}},
		{`SELECT true FROM owned_custom_apps WHERE id=$1 AND project_id=$2 AND status=1 AND deleted_at IS NULL FOR SHARE`, []any{id, project}},
	} {
		if e = tx.QueryRowContext(ctx, q.sql, q.args...).Scan(&yes); e != nil {
			return notFound(e)
		}
	}
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_custom_apps a JOIN owned_dev_projects p ON p.id=a.project_id JOIN owned_developers d ON d.id=p.developer_id JOIN owned_publishers pub ON pub.id=d.publisher_id WHERE a.id=$1 AND a.project_id=$2 AND d.publisher_id=$3 AND a.status=1 AND a.deleted_at IS NULL AND p.deleted_at IS NULL AND pub.deleted_at IS NULL AND (pub.account_id IS NULL OR EXISTS(SELECT 1 FROM owned_authority_accounts owner WHERE owner.id=pub.account_id AND NOT owner.disabled)))`, id, project, publisher).Scan(&yes)
	if e != nil {
		return e
	}
	if !yes {
		return c.ErrNotFound
	}
	return nil
}
func sameDefinitionApp(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// Paying an existing subscription/gift order must take catalog ancestry before
// the order or wallets; later activation rechecks the same locked definition.
func lockOrderDefinition(ctx context.Context, tx *sql.Tx, id string) error {
	var key sql.NullString
	e := tx.QueryRowContext(ctx, `SELECT product_identifier FROM commerce_v1.orders WHERE id=$1 AND (subscription_id IS NOT NULL OR gift_id IS NOT NULL)`, id).Scan(&key)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	if !key.Valid {
		return c.ErrUnavailable
	}
	_, e = definition(ctx, tx, key.String)
	return e
}
