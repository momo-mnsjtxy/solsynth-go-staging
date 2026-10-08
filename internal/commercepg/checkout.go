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
	return e
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
