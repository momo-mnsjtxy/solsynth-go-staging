// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"strings"
	"unicode/utf8"
)

//go:embed shipping_schema.sql
var ShippingSchema string

func shippingSelection(meta map[string]json.RawMessage) (string, error) {
	var id, text string
	if json.Unmarshal(meta["address_contact_id"], &id) != nil || !authority.ValidID(id) || json.Unmarshal(meta["address_snapshot"], &text) != nil || strings.TrimSpace(text) == "" || len(text) > 2048 || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return "", c.ErrInvalid
	}
	return id, nil
}
func pendingDestination(ctx context.Context, tx *sql.Tx, o *c.Order, contact string) error {
	if !authority.ValidID(contact) {
		return c.ErrInvalid
	}
	_, e := tx.ExecContext(ctx, `INSERT INTO commerce_v1.order_destinations(order_id,address_contact_id)VALUES($1,$2)`, o.ID, contact)
	return e
}

// captureDestination runs after canonical payer account and order locks. Contact
// writers take that same account lock; FOR SHARE also covers a blocked SQL row.
// The address is read at payment, matching the authored wallet controller.
func (r *Repository) captureDestination(ctx context.Context, tx *sql.Tx, actor string, o *c.Order) error {
	var contact string
	var captured sql.NullTime
	e := tx.QueryRowContext(ctx, `SELECT address_contact_id::text,captured_at FROM commerce_v1.order_destinations WHERE order_id=$1 FOR UPDATE`, o.ID).Scan(&contact, &captured)
	_, required := o.Meta["fulfillment"]
	if errors.Is(e, sql.ErrNoRows) {
		if required {
			return c.ErrConflict
		}
		return nil
	}
	if e != nil {
		return e
	}
	if captured.Valid || !required {
		return c.ErrConflict
	}
	var text string
	var revision int64
	e = tx.QueryRowContext(ctx, `SELECT content,revision FROM owned_authority_contacts WHERE id=$1 AND account_id=$2 AND type=2 AND credential_account_id IS NULL AND deleted_at IS NULL FOR SHARE`, contact, actor).Scan(&text, &revision)
	if e != nil {
		return notFound(e)
	}
	if strings.TrimSpace(text) == "" || len(text) > 2048 || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return c.ErrInvalid
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, "orders.pay"); e != nil {
		return e
	}
	result, e := tx.ExecContext(ctx, `UPDATE commerce_v1.order_destinations SET account_id=$2,address_snapshot=$3,contact_revision=$4,captured_at=$5 WHERE order_id=$1 AND captured_at IS NULL`, o.ID, actor, text, revision, r.now().UTC())
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil || n != 1 {
		return c.ErrConflict
	}
	o.Meta["fulfillment"], e = jsonBytes(c.Fulfillment{AddressContactID: contact, AddressSnapshot: text, RequiresAddress: true})
	return e
}
func (r *Repository) OrderFulfillment(ctx context.Context, orderID string, credentials c.Credentials) (out c.MerchantFulfillment, e error) {
	if !authority.ValidID(orderID) {
		return out, c.ErrNotFound
	}
	tx, e := r.begin(ctx, "", "")
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	app, e := r.client(ctx, tx, credentials, c.FulfillmentReadScope)
	if e != nil {
		return out, e
	}
	o, e := order(ctx, tx, orderID, true)
	if e != nil {
		return out, e
	}
	if o.AppIdentifier != "developer.app:"+app.AppID {
		return out, c.ErrNotFound
	}
	if o.Status != c.Paid && o.Status != c.Finished {
		return out, c.ErrConflict
	}
	e = tx.QueryRowContext(ctx, `SELECT address_snapshot,captured_at FROM commerce_v1.order_destinations WHERE order_id=$1 AND captured_at IS NOT NULL`, o.ID).Scan(&out.AddressSnapshot, &out.CapturedAt)
	if e != nil {
		return out, notFound(e)
	}
	out.OrderID = o.ID
	out.Status = o.Status
	out.Items = append([]c.OrderItem{}, o.Items...)
	if _, e = r.client(ctx, tx, credentials, c.FulfillmentReadScope); e != nil {
		return c.MerchantFulfillment{}, e
	}
	if e = tx.Commit(); e != nil {
		return c.MerchantFulfillment{}, e
	}
	return
}

func cloneShippingMeta(v map[string]json.RawMessage) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for k, b := range v {
		out[k] = append(json.RawMessage(nil), b...)
	}
	return out
}
