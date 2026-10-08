// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Ready verifies required relation presence without creating or mutating data.
// It is a dependency readiness probe, not certification of schema/parity/migrations.
func (b *Backend) Ready(ctx context.Context) error {
	if b == nil || b.db == nil || b.authorityDB == nil {
		return errors.New("backend unavailable")
	}
	var ready bool
	err := b.authorityDB.QueryRowContext(ctx, `SELECT bool_and(to_regclass(name) IS NOT NULL) FROM (VALUES ('owned_authority_accounts'), ('owned_authority_sessions'), ('owned_authority_grants'), ('owned_authority_groups'), ('owned_authority_group_members'), ('owned_authority_group_grants'), ('owned_authority_punishments')) AS required(name)`).Scan(&ready)
	if err != nil || !ready {
		return errors.New("authority schema unavailable")
	}
	err = b.db.QueryRowContext(ctx, `SELECT bool_and(to_regclass(name) IS NOT NULL) FROM (VALUES ('owned_authority_accounts'), ('owned_accounts_profiles'), ('owned_publishers'), ('owned_publisher_members'), ('owned_publishing_settings'), ('owned_publisher_quota'), ('owned_publisher_entitlements'), ('owned_accounts_audit'), ('account_board_items'), ('owned_taxonomy.posts'), ('owned_taxonomy.post_tags'), ('owned_taxonomy.post_categories'), ('owned_taxonomy.post_tag_links'), ('owned_taxonomy.post_category_links'), ('owned_taxonomy.post_category_subscriptions'), ('owned_taxonomy.actions'), ('owned_text_posts'), ('owned_text_post_audit'), ('chat_rooms'), ('chat_members'), ('chat_messages'), ('chat_reactions'), ('wallet_v1.wallets'), ('wallet_v1.pockets'), ('wallet_v1.transactions'), ('wallet_v1.outbox'), ('wallet_v1.payment_policies'), ('wallet_v1.realm_grants')) AS required(name)`).Scan(&ready)
	if err != nil || !ready {
		return errors.New("domain schema unavailable")
	}
	return nil
}
func (b *Backend) readinessHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		if r.Method == http.MethodHead {
			w = headWriter{w}
		}
		problem(w, 405, "METHOD_NOT_ALLOWED")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := b.Ready(ctx); err != nil {
		problem(w, 503, "BACKEND_NOT_READY")
		return
	}
	problem(w, 200, "BACKEND_READY")
}
