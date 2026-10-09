// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"errors"
	"net/http"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
)

// Ready verifies required relation presence without creating or mutating data.
// It is a dependency readiness probe, not certification of schema/parity/migrations.
func (b *Backend) Ready(ctx context.Context) error {
	if e := b.passkit.ready(ctx); e != nil {
		return e
	}
	if b == nil || b.db == nil || b.authorityDB == nil {
		return errors.New("backend unavailable")
	}
	if b.activities != nil {
		if e := b.activities.Ready(ctx); e != nil {
			return e
		}
	}
	if b.postComposition != nil {
		if e := b.postComposition.Ready(ctx); e != nil {
			return e
		}
	}
	if b.blogs != nil {
		if e := b.blogs.Ready(ctx); e != nil {
			return e
		}
	}
	if e := b.realmMembers.ready(ctx); e != nil {
		return e
	}
	if e := b.realmBoosts.ready(ctx); e != nil {
		return e
	}
	if b.scheduledPosts != nil {
		if e := b.scheduledPosts.SchedulingReady(ctx); e != nil {
			return e
		}
	}
	if b.social != nil {
		if e := b.social.ExpiryReady(ctx); e != nil {
			return e
		}
	}
	if b.commerce != nil {
		if e := b.commerce.ManagedOrdersReady(ctx); e != nil {
			return e
		}
		if e := b.commerce.CouponsReady(ctx); e != nil {
			return e
		}
	}
	if _, e := b.db.ExecContext(ctx, `SELECT notify_subscribers FROM owned_surveys LIMIT 0; SELECT revision FROM owned_survey_answers LIMIT 0`); e != nil {
		return e
	}
	var ready bool
	err := b.authorityDB.QueryRowContext(ctx, `SELECT bool_and(to_regclass(name) IS NOT NULL) FROM (VALUES ('owned_authority_accounts'), ('owned_authority_sessions'), ('owned_authority_grants'), ('owned_authority_groups'), ('owned_authority_group_members'), ('owned_authority_group_grants'), ('owned_authority_punishments')) AS required(name)`).Scan(&ready)
	if err != nil || !ready {
		return errors.New("authority schema unavailable")
	}
	if err := authoritypg.New(b.authorityDB).ContinuityReady(ctx); err != nil {
		return errors.New("authority continuity schema unavailable")
	}
	err = b.db.QueryRowContext(ctx, `SELECT bool_and(to_regclass(name) IS NOT NULL) FROM (VALUES ('owned_authority_accounts'), ('owned_accounts_profiles'), ('owned_publishers'), ('owned_publisher_members'), ('owned_publishing_settings'), ('owned_publisher_quota'), ('owned_publisher_entitlements'), ('owned_accounts_audit'), ('account_board_items'), ('owned_taxonomy.posts'), ('owned_taxonomy.post_tags'), ('owned_taxonomy.post_categories'), ('owned_taxonomy.post_tag_links'), ('owned_taxonomy.post_category_links'), ('owned_taxonomy.post_category_subscriptions'), ('owned_taxonomy.actions'), ('owned_text_posts'), ('owned_text_post_audit'), ('owned_post_media'), ('owned_merchant_management_audit'), ('owned_presence_activities'), ('owned_activity_catalog'), ('owned_activity_events'), ('owned_presence_preferences'), ('owned_presence_last_seen'), ('owned_presence_sessions'), ('owned_presence_statuses'), ('owned_presence_activity_days'), ('owned_progression_definitions'), ('owned_progression_events'), ('owned_progression_progress'), ('owned_progression_grants'), ('owned_progression_experience'), ('owned_progression_badges'), ('owned_progression_checkins'), ('owned_progression_level_policy'), ('owned_progression_audit'), ('owned_progression_outbox'), ('owned_progression_projection_receipts'), ('owned_meets'), ('owned_meet_participants'), ('owned_location_pins'), ('owned_nfc_tags'), ('owned_location_audit'), ('owned_surveys'), ('owned_survey_answers'), ('owned_survey_audit'), ('owned_survey_subscriptions'), ('owned_survey_notifications'), ('owned_sticker_packs'), ('owned_stickers'), ('owned_sticker_ownerships'), ('owned_sticker_audit'), ('owned_post_relations'), ('owned_publisher_follow_policy'), ('owned_publisher_follows'), ('owned_timeline_interests'), ('owned_timeline_preferences'), ('owned_timeline_audit'), ('owned_post_watch_preferences'), ('commerce_v1.orders'), ('commerce_v1.subscriptions'), ('commerce_v1.gifts'), ('commerce_v1.definitions'), ('commerce_v1.outbox'), ('commerce_v1.subscription_maintenance_quarantine'), ('owned_developers'), ('owned_dev_projects'), ('owned_custom_apps'), ('owned_app_products'), ('owned_app_widgets'), ('owned_app_secrets'), ('owned_app_consents'), ('owned_apps_audit'), ('notification_v1.streams'), ('notification_v1.inbox'), ('notification_v1.outbox'), ('notification_v1.events'), ('notification_v1.receipts'), ('notification_v1.deliveries'), ('owned_social_effective_relationships'), ('owned_realms'), ('owned_realm_members'), ('owned_post_reactions'), ('owned_post_bookmarks'), ('owned_post_collections'), ('owned_post_collection_items'), ('chat_rooms'), ('chat_members'), ('chat_messages'), ('chat_reactions'), ('wallet_v1.wallets'), ('wallet_v1.pockets'), ('wallet_v1.transactions'), ('wallet_v1.outbox'), ('wallet_v1.payment_policies'), ('wallet_v1.realm_grants')) AS required(name)`).Scan(&ready)
	if err != nil || !ready {
		return errors.New("domain schema unavailable")
	}
	err = b.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='owned_text_posts'::regclass AND conname='owned_text_posts_realm_fk' AND convalidated)`).Scan(&ready)
	if err != nil || !ready {
		return errors.New("post realm schema unavailable")
	}
	if _, err := b.db.ExecContext(ctx, `SELECT last_read_at,ended_by_account_id FROM owned_publisher_follows LIMIT 0;SELECT id,publisher_id,started_at,operation_state FROM owned_livestreams LIMIT 0`); err != nil {
		return errors.New("publisher subscription state schema unavailable")
	}
	if err := b.publisherCommerce.ready(ctx); err != nil {
		return err
	}
	if _, err := b.db.ExecContext(ctx, `SELECT awarded_score,blog_metadata FROM owned_text_posts LIMIT 0`); err != nil {
		return err
	}
	if b.wallet != nil {
		if err := b.wallet.ExchangeReady(ctx); err != nil {
			return err
		}
		if err := b.wallet.FundsReady(ctx); err != nil {
			return err
		}
		if err := b.wallet.TransferRequestsReady(ctx); err != nil {
			return err
		}
	}
	if err := b.domainCollections.ready(ctx); err != nil {
		return err
	}
	if err := b.accountExtras.ready(ctx); err != nil {
		return err
	}
	if b.moderation != nil {
		if e := b.moderation.Ready(ctx); e != nil {
			return e
		}
	}
	if b.admin.repo != nil {
		if e := b.admin.repo.Ready(ctx); e != nil {
			return e
		}
	}
	if b.accountSettings != nil {
		if err := b.accountSettings.Ready(ctx); err != nil {
			return err
		}
	}
	if _, err := b.db.ExecContext(ctx, `SELECT credential_account_id,verified_at,is_public,revision FROM owned_authority_contacts LIMIT 0; SELECT order_id,address_snapshot,captured_at FROM commerce_v1.order_destinations LIMIT 0`); err != nil {
		return err
	}
	if b.translation != nil {
		if err := b.translation.Ready(ctx); err != nil {
			return err
		}
	}
	if b.autocomplete != nil {
		if err := b.autocomplete.Ready(ctx); err != nil {
			return err
		}
	}
	if _, e := b.db.ExecContext(ctx, `SELECT role_level,generation,permission_mask FROM owned_realm_role_permissions LIMIT 0;SELECT allow_mask,deny_mask,membership_since FROM owned_realm_user_permissions LIMIT 0;SELECT owned_realm_permission_mask(NULL::uuid,NULL::uuid)`); e != nil {
		return errors.New("realm permission schema unavailable")
	}
	if b.chatOrganization != nil {
		if err := b.chatOrganization.Ready(ctx); err != nil {
			return err
		}
	}
	if b.chat != nil {
		if err := b.chat.RelationsReady(ctx); err != nil {
			return err
		}
	}
	if b.reader != nil {
		if err := b.reader.Ready(ctx); err != nil {
			return err
		}
	}
	if b.oauth != nil {
		if err := b.oauth.Ready(ctx); err != nil {
			return err
		}
	}
	if b.fortunes != nil {
		if err := b.fortunes.Ready(ctx); err != nil {
			return err
		}
	}
	if b.calendar != nil {
		if err := b.calendar.Ready(ctx); err != nil {
			return err
		}
	}
	if b.bots != nil {
		if b.bots.ExecutionEnabled() {
			if e := b.bots.ExecutionReady(ctx); e != nil {
				return e
			}
		}
		if err := b.bots.Ready(ctx); err != nil {
			return err
		}
	}
	if b.affiliations != nil {
		if e := b.affiliations.Ready(ctx); e != nil {
			return e
		}
	}
	if b.tickets != nil {
		if err := b.tickets.Ready(ctx); err != nil {
			return err
		}
	}
	if _, e := b.db.ExecContext(ctx, `SELECT code_hash,auth_time,methods FROM owned_authority_code_evidence LIMIT 0;SELECT session_id,session_epoch FROM owned_authority_credential_assurances LIMIT 0`); e != nil {
		return errors.New("credential assurance schema unavailable")
	}
	if _, e := b.db.ExecContext(ctx, `SELECT version,factor_id FROM wallet_v1.payment_policies LIMIT 0;SELECT hash,policy_version FROM wallet_v1.payment_authorizations LIMIT 0`); e != nil {
		return errors.New("payment authorization schema unavailable")
	}
	if b.signinEnabled {
		if _, e := b.db.ExecContext(ctx, `SELECT auth_methods FROM owned_authority_challenges LIMIT 0;SELECT id FROM owned_authority_factor_audit LIMIT 0`); e != nil {
			return errors.New("payment PIN factor schema unavailable")
		}
		var yes bool
		err = b.db.QueryRowContext(ctx, `SELECT bool_and(to_regclass(name) IS NOT NULL) FROM (VALUES ('owned_authority_credentials'),('owned_authority_factors'),('owned_authority_challenges'),('owned_authority_attempts')) AS required(name)`).Scan(&yes)
		if err != nil || !yes {
			return errors.New("sign-in schema unavailable")
		}
	}
	var columns int
	err = b.db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND ((table_name='chat_rooms' AND column_name IN ('owned_owner_id','owned_next_sequence')) OR (table_name='chat_members' AND column_name='owned_banned') OR (table_name='chat_messages' AND column_name='owned_request_hash'))`).Scan(&columns)
	if err != nil || columns != 4 {
		return errors.New("chat write schema unavailable")
	}
	if b.media != nil {
		if err := b.media.ArtworkReady(ctx); err != nil {
			return err
		}
		if err := b.media.Ready(ctx); err != nil {
			return errors.New("media storage unavailable")
		}
	}
	return b.external.ready(ctx, b.db)
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
