// SPDX-License-Identifier: AGPL-3.0-only
package accountpg

import (
	"context"
	"database/sql"
	_ "embed"
	"regexp"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"time"
)

// PublisherNamesSchema is an explicit migration after the base account schema.
//
//go:embed name_schema.sql
var PublisherNamesSchema string
var selectorAlias = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
var selectorParameter = regexp.MustCompile(`^\$[1-9][0-9]*$`)

// PublisherNamePredicate resolves only exact handles. Arguments are compiled
// SQL aliases/placeholders, never values supplied by an HTTP request.
func PublisherNamePredicate(alias, parameter string) string {
	if !selectorAlias.MatchString(alias) || !selectorParameter.MatchString(parameter) {
		return "FALSE"
	}
	return `EXISTS(SELECT 1 FROM owned_publisher_name_reservations reserved_name WHERE reserved_name.publisher_id=` + alias + `.id AND reserved_name.folded_name=lower(` + parameter + `::text))`
}

// RenamePublisherIdentity composes with a caller-owned transaction. The caller
// holds canonical account/realm/publisher locks and retains its own permission
// policy; this helper grants none. Paid and privileged rename share this policy.
func RenamePublisherIdentity(ctx context.Context, tx *sql.Tx, publisher, expected, name string, now time.Time) error {
	if tx == nil || !authority.ValidID(publisher) {
		return accounts.ErrUnavailable
	}
	nick := "name validation"
	if e := accounts.ValidatePublisher(accounts.PublisherPatch{Name: &name, Nick: &nick}, true); e != nil {
		return e
	}
	var current string
	if e := tx.QueryRowContext(ctx, `SELECT name FROM owned_publishers WHERE id=$1::uuid AND deleted_at IS NULL FOR UPDATE`, publisher).Scan(&current); e != nil {
		return mapped(e)
	}
	if current != expected {
		return accounts.ErrConflict
	}
	if current == name {
		return nil
	}
	var schema sql.NullString
	if e := tx.QueryRowContext(ctx, `SELECT to_regclass('owned_publisher_name_reservations')::text`).Scan(&schema); e != nil {
		return e
	}
	if !schema.Valid {
		return accounts.ErrUnavailable
	}
	var federation sql.NullString
	if e := tx.QueryRowContext(ctx, `SELECT to_regclass('owned_federation_publishers')::text`).Scan(&federation); e != nil {
		return e
	}
	if federation.Valid {
		var anchored bool
		if e := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT (to_jsonb(f)->>'actor_uri')IS NOT NULL AND(to_jsonb(f)->>'key_id')IS NOT NULL FROM owned_federation_publishers f WHERE publisher_id=$1::uuid),true)`, publisher).Scan(&anchored); e != nil {
			return e
		}
		if !anchored {
			return accounts.ErrUnsupported
		}
	}
	var count int
	var mine bool
	if e := tx.QueryRowContext(ctx, `SELECT count(*),EXISTS(SELECT 1 FROM owned_publisher_name_reservations WHERE publisher_id=$1::uuid AND folded_name=lower($2::text))FROM owned_publisher_name_reservations WHERE publisher_id=$1::uuid`, publisher, name).Scan(&count, &mine); e != nil {
		return e
	}
	if count >= 256 && !mine {
		return accounts.ErrConflict
	}
	_, e := tx.ExecContext(ctx, `UPDATE owned_publishers SET name=$2::text,updated_at=$3::timestamptz WHERE id=$1::uuid`, publisher, name, now.UTC().Truncate(time.Microsecond))
	return mapped(e)
}
