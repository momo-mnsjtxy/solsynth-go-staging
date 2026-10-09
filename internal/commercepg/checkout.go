// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"context"
	"database/sql"
	"errors"
	c "solsynth.local/go-migration/ports/wallet/commerce"
)

// lockOrderApp validates the immutable canonical order/app relation and holds
// publisher, project and app state through payment commit. Its lock order is the
// same as app credential checks and app management, and precedes the order lock.
func lockOrderApp(ctx context.Context, tx *sql.Tx, o c.Order) error {
	var app sql.NullString
	if e := tx.QueryRowContext(ctx, `SELECT app_id::text FROM commerce_v1.orders WHERE id=$1`, o.ID).Scan(&app); e != nil {
		return notFound(e)
	}
	if !app.Valid {
		if o.AppIdentifier != "internal" && o.AppIdentifier != "gift" {
			return c.ErrForbidden
		}
		return nil
	}
	if o.AppIdentifier != "developer.app:"+app.String {
		return c.ErrForbidden
	}
	var project, developer, publisher string
	e := tx.QueryRowContext(ctx, `SELECT a.project_id::text,p.developer_id::text,d.publisher_id::text FROM owned_custom_apps a JOIN owned_dev_projects p ON p.id=a.project_id JOIN owned_developers d ON d.id=p.developer_id WHERE a.id=$1`, app.String).Scan(&project, &developer, &publisher)
	if errors.Is(e, sql.ErrNoRows) {
		return c.ErrForbidden
	}
	if e != nil {
		return e
	}
	var yes bool
	e = tx.QueryRowContext(ctx, `SELECT true FROM owned_publishers WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, publisher).Scan(&yes)
	if errors.Is(e, sql.ErrNoRows) {
		return c.ErrForbidden
	}
	if e != nil {
		return e
	}
	e = tx.QueryRowContext(ctx, `SELECT true FROM owned_dev_projects WHERE id=$1 AND developer_id=$2 AND deleted_at IS NULL FOR SHARE`, project, developer).Scan(&yes)
	if errors.Is(e, sql.ErrNoRows) {
		return c.ErrForbidden
	}
	if e != nil {
		return e
	}
	e = tx.QueryRowContext(ctx, `SELECT true FROM owned_custom_apps WHERE id=$1 AND project_id=$2 AND status=1 AND deleted_at IS NULL FOR SHARE`, app.String, project).Scan(&yes)
	if errors.Is(e, sql.ErrNoRows) {
		return c.ErrForbidden
	}
	if e != nil {
		return e
	}
	return checkOrderAppOwner(ctx, tx, o)
}

// checkOrderAppOwner observes current owner eligibility without adding another
// account lock after the buyer's lock. App ancestry is already retained by
// lockOrderApp. Repeat this after wallet/payment waits before a new charge
// commits. A concurrent disable after the final observation may serialize after
// the payment; a disable committed before the observation must be respected.
func checkOrderAppOwner(ctx context.Context, tx *sql.Tx, o c.Order) error {
	if o.AppIdentifier == "internal" || o.AppIdentifier == "gift" {
		return nil
	}
	var active bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM commerce_v1.orders o JOIN owned_custom_apps a ON a.id=o.app_id JOIN owned_dev_projects project ON project.id=a.project_id JOIN owned_developers dev ON dev.id=project.developer_id JOIN owned_publishers pub ON pub.id=dev.publisher_id LEFT JOIN owned_realms realm ON pub.type=1 AND realm.id=pub.realm_id JOIN owned_authority_accounts owner ON owner.id=CASE WHEN pub.type=0 THEN pub.account_id WHEN pub.type=1 THEN realm.account_id END WHERE o.id=$1::uuid AND ('developer.app:'||a.id::text)=$2::text AND a.status=1 AND a.deleted_at IS NULL AND project.deleted_at IS NULL AND pub.deleted_at IS NULL AND ((pub.type=0 AND pub.account_id IS NOT NULL AND pub.realm_id IS NULL) OR (pub.type=1 AND pub.account_id IS NULL AND realm.id IS NOT NULL AND realm.deleted_at IS NULL)) AND NOT owner.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments punishment WHERE punishment.account_id=owner.id AND punishment.kind='disable_account' AND(punishment.starts_at IS NULL OR punishment.starts_at<=clock_timestamp())AND(punishment.expires_at IS NULL OR punishment.expires_at>clock_timestamp())))`, o.ID, o.AppIdentifier).Scan(&active)
	if e != nil {
		return e
	}
	if !active {
		return c.ErrForbidden
	}
	return nil
}

// enrichOrder projects only public display fields. It never exposes OAuth
// configuration, app credentials, publisher membership or private metadata.
func enrichOrder(ctx context.Context, tx *sql.Tx, o *c.Order) error {
	o.App = nil
	o.Developer = nil
	o.IsSystemApp = false
	if o.AppIdentifier == "internal" {
		description := "Built-in platform services"
		o.App = &c.OrderApp{ID: "00000000-0000-0000-0000-000000000000", Slug: "internal", Name: "Solar Network", Description: &description, Status: 1, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt}
		o.IsSystemApp = true
		return nil
	}
	var app c.OrderApp
	var developer c.OrderDeveloper
	e := tx.QueryRowContext(ctx, `SELECT a.id::text,a.slug,a.name,a.description,a.status,a.created_at,a.updated_at,d.id::text,pub.id::text,pub.name FROM commerce_v1.orders o JOIN owned_custom_apps a ON a.id=o.app_id JOIN owned_dev_projects p ON p.id=a.project_id JOIN owned_developers d ON d.id=p.developer_id JOIN owned_publishers pub ON pub.id=d.publisher_id WHERE o.id=$1 AND a.deleted_at IS NULL AND a.status=1 AND p.deleted_at IS NULL AND pub.deleted_at IS NULL`, o.ID).Scan(&app.ID, &app.Slug, &app.Name, &app.Description, &app.Status, &app.CreatedAt, &app.UpdatedAt, &developer.ID, &developer.PublisherID, &developer.PublisherName)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	o.App = &app
	o.Developer = &developer
	return nil
}
