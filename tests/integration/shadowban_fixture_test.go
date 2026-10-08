// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"database/sql"
	"testing"
	"time"

	admin "solsynth.local/go-migration/ports/admin/content"
)

// fixturePublisherShadowban writes only valid canonical states. Invalid-state
// constraint tests should issue their deliberately malformed SQL separately.
func fixturePublisherShadowban(t *testing.T, db *sql.DB, id, reason string, at time.Time) {
	t.Helper()
	var timestamp, value any
	if reason == "" {
		if !at.IsZero() {
			t.Fatal("cleared shadowban requires a zero timestamp")
		}
	} else {
		if !admin.ValidReason(reason, false) || at.IsZero() {
			t.Fatal("shadowban fixture requires a canonical reason and timestamp")
		}
		timestamp, value = at.UTC(), reason
	}
	r, err := db.Exec(`UPDATE owned_publishers SET shadowbanned_at=$2::timestamptz,shadowban_reason=$3::text WHERE id=$1::uuid`, id, timestamp, value)
	if err != nil {
		t.Fatal(err)
	}
	n, err := r.RowsAffected()
	if err != nil || n != 1 {
		t.Fatal("shadowban fixture must update exactly one publisher", n, err)
	}
}

func fixturePublisherShadowbanName(t *testing.T, db *sql.DB, name, reason string, at time.Time) {
	t.Helper()
	var id string
	if err := db.QueryRow(`SELECT id::text FROM owned_publishers WHERE name=$1::text`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	fixturePublisherShadowban(t, db, id, reason, at)
}
