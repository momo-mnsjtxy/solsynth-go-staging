// SPDX-License-Identifier: AGPL-3.0-only
// 就绪检查：按业务检查已安装的数据库与已启用依赖；不建表、不迁移、不写数据。
// 排查启动失败时，先看 Ready 返回的业务阶段，再看该阶段保留的数据库错误。
// 已构建 backend 后可直接调用（需 context、time）：
//
//	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
//	defer cancel()
//	if err := backend.Ready(ctx); err != nil { return err }
//
// HTTP 的 GET /readyz 也调用同一条检查流程；它只表示依赖可用，不证明迁移或原版功能对齐。
package application

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
)

// Ready 按既有顺序检查依赖，首个失败即返回，避免故障后继续查询其它服务。
func (b *Backend) Ready(ctx context.Context) error {
	if b == nil || b.db == nil || b.authorityDB == nil { // 先确认主体存在，再读取任何模块。
		return errors.New("backend unavailable")
	}
	if err := b.readyAccountNotifications(ctx); err != nil { // 账号与通知依赖。
		return fmt.Errorf("readiness AccountNotifications: %w", err)
	}
	if err := b.readyPublishingServices(ctx); err != nil { // 发布、成长与社交依赖。
		return fmt.Errorf("readiness PublishingServices: %w", err)
	}
	if err := b.readyCoreSchemas(ctx); err != nil { // 权威账号与领域基础表。
		return fmt.Errorf("readiness CoreSchemas: %w", err)
	}
	if err := b.readyPublishingSchema(ctx); err != nil { // 发布设置、订阅与正文投影。
		return fmt.Errorf("readiness PublishingSchema: %w", err)
	}
	if err := b.readyProgressionSources(ctx); err != nil { // 成长来源的字段、约束与不可变性。
		return fmt.Errorf("readiness ProgressionSources: %w", err)
	}
	if err := b.readyWalletAndGovernance(ctx); err != nil { // 钱包、域名与账号治理。
		return fmt.Errorf("readiness WalletAndGovernance: %w", err)
	}
	if err := b.readyContactsAndMessaging(ctx); err != nil { // 联系方式、空间权限与聊天依赖。
		return fmt.Errorf("readiness ContactsAndMessaging: %w", err)
	}
	if err := b.readyConnectedServices(ctx); err != nil { // 授权、日历与扩展服务。
		return fmt.Errorf("readiness ConnectedServices: %w", err)
	}
	if err := b.readyCredentialSchemas(ctx); err != nil { // 登录凭证与支付授权。
		return fmt.Errorf("readiness CredentialSchemas: %w", err)
	}
	if err := b.readyChatMediaAndExternal(ctx); err != nil { // 聊天写入、媒体与外部依赖。
		return fmt.Errorf("readiness ChatMediaAndExternal: %w", err)
	}
	return nil
}

// --- 检查账号与通知依赖 ---
func (b *Backend) readyAccountNotifications(ctx context.Context) error {
	if e := b.passkit.ready(ctx); e != nil { // 会员卡仅检查已配置的签发存储。
		return e
	}
	if b.publisherNotifications != nil { // 发布者通知需要自己的订阅和通知投影。
		if e := b.publisherNotifications.Ready(ctx); e != nil {
			return e
		}
	}
	if e := b.notificationDevices.ready(ctx); e != nil { // 推送设备登记与认证依赖。
		return e
	}
	if b.notificationWorker != nil && b.notificationWorker.Repository != nil { // 启用投递工作器时检查 NightOwl 队列。
		if e := b.notificationWorker.Repository.NightOwlReady(ctx); e != nil {
			return e
		}
	}
	if e := b.accountConnections.ready(ctx); e != nil { // 账号连接关系与凭证存储。
		return e
	}
	if b.accountHistory != nil { // 账号活动历史投影。
		if e := b.accountHistory.Ready(ctx); e != nil {
			return e
		}
	}
	return nil
}

// --- 检查发布、成长与社交依赖 ---
func (b *Backend) readyPublishingServices(ctx context.Context) error {
	if b.progression != nil { // 成长规则、经验和授予存储。
		if e := b.progression.Ready(ctx); e != nil {
			return e
		}
	}
	if b.activities != nil { // 用户活动及在线状态存储。
		if e := b.activities.Ready(ctx); e != nil {
			return e
		}
	}
	if b.postComposition != nil { // 正文组合及嵌入引用存储。
		if e := b.postComposition.Ready(ctx); e != nil {
			return e
		}
	}
	if b.blogs != nil { // 博客扩展存储。
		if e := b.blogs.Ready(ctx); e != nil {
			return e
		}
	}
	if e := b.realmMembers.ready(ctx); e != nil { // 空间成员关系。
		return e
	}
	if e := b.realmBoosts.ready(ctx); e != nil { // 空间助力与权益。
		return e
	}
	if b.scheduledPosts != nil { // 定时发布状态和到期队列。
		if e := b.scheduledPosts.SchedulingReady(ctx); e != nil {
			return e
		}
	}
	if b.social != nil { // 社交关系到期处理依赖。
		if e := b.social.ExpiryReady(ctx); e != nil {
			return e
		}
	}
	if b.commerce != nil { // 订单与优惠券沿用原先检查顺序。
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
	return nil
}

// --- 检查权威账号与领域基础表 ---
func (b *Backend) readyCoreSchemas(ctx context.Context) error {
	var ready bool
	err := b.authorityDB.QueryRowContext(ctx, `SELECT bool_and(to_regclass(name) IS NOT NULL) FROM (VALUES ('owned_authority_accounts'), ('owned_authority_sessions'), ('owned_authority_grants'), ('owned_authority_groups'), ('owned_authority_group_members'), ('owned_authority_group_grants'), ('owned_authority_punishments'), ('owned_authority_stream_tickets')) AS required(name)`).Scan(&ready)
	if err != nil || !ready {
		return errors.New("authority schema unavailable")
	}
	if err := authoritypg.New(b.authorityDB).ContinuityReady(ctx); err != nil {
		return errors.New("authority continuity schema unavailable")
	}
	err = b.db.QueryRowContext(ctx, `SELECT bool_and(to_regclass(name) IS NOT NULL) FROM (VALUES ('owned_authority_accounts'), ('owned_accounts_profiles'), ('owned_publishers'), ('owned_publisher_members'), ('owned_publishing_settings'), ('owned_publisher_quota'), ('owned_publisher_entitlements'), ('owned_publisher_features'), ('owned_publisher_relationships'), ('owned_accounts_audit'), ('account_board_items'), ('owned_taxonomy.posts'), ('owned_taxonomy.post_tags'), ('owned_taxonomy.post_categories'), ('owned_taxonomy.post_tag_links'), ('owned_taxonomy.post_category_links'), ('owned_taxonomy.post_category_subscriptions'), ('owned_taxonomy.actions'), ('owned_text_posts'), ('owned_text_post_audit'), ('owned_post_boosts'), ('owned_post_progression_sources'), ('owned_post_featured_days'), ('owned_post_media'), ('owned_merchant_management_audit'), ('owned_presence_activities'), ('owned_activity_catalog'), ('owned_activity_events'), ('owned_presence_preferences'), ('owned_presence_last_seen'), ('owned_presence_sessions'), ('owned_presence_statuses'), ('owned_presence_activity_days'), ('owned_progression_definitions'), ('owned_progression_events'), ('owned_progression_progress'), ('owned_progression_grants'), ('owned_progression_experience'), ('owned_progression_badges'), ('owned_progression_checkins'), ('owned_progression_level_policy'), ('owned_progression_audit'), ('owned_progression_outbox'), ('owned_progression_projection_receipts'), ('owned_meets'), ('owned_meet_participants'), ('owned_location_pins'), ('owned_nfc_tags'), ('owned_location_audit'), ('owned_surveys'), ('owned_survey_answers'), ('owned_survey_audit'), ('owned_survey_subscriptions'), ('owned_survey_notifications'), ('owned_sticker_packs'), ('owned_stickers'), ('owned_sticker_ownerships'), ('owned_sticker_audit'), ('owned_post_relations'), ('owned_publisher_follow_policy'), ('owned_publisher_follows'), ('owned_timeline_interests'), ('owned_timeline_preferences'), ('owned_timeline_audit'), ('owned_post_watch_preferences'), ('commerce_v1.orders'), ('commerce_v1.subscriptions'), ('commerce_v1.gifts'), ('commerce_v1.definitions'), ('commerce_v1.outbox'), ('commerce_v1.subscription_maintenance_quarantine'), ('commerce_v1.subscription_trials'), ('commerce_v1.stellar_support_months'), ('commerce_v1.merchant_settlements'), ('commerce_v1.order_reversals'), ('owned_developers'), ('owned_dev_projects'), ('owned_custom_apps'), ('owned_app_products'), ('owned_app_widgets'), ('owned_app_secrets'), ('owned_app_consents'), ('owned_apps_audit'), ('notification_v1.streams'), ('notification_v1.inbox'), ('notification_v1.outbox'), ('notification_v1.events'), ('notification_v1.receipts'), ('notification_v1.deliveries'), ('owned_social_effective_relationships'), ('owned_realms'), ('owned_realm_members'), ('owned_post_reactions'), ('owned_post_bookmarks'), ('owned_post_collections'), ('owned_post_collection_items'), ('owned_taxonomy.owned_collection_subscription_live'), ('owned_collection_publications'), ('owned_collection_publication_notifications'), ('owned_collection_publication_subscriptions'), ('chat_rooms'), ('chat_members'), ('chat_messages'), ('chat_reactions'), ('wallet_v1.wallets'), ('wallet_v1.pockets'), ('wallet_v1.transactions'), ('wallet_v1.outbox'), ('wallet_v1.payment_policies'), ('wallet_v1.realm_grants')) AS required(name)`).Scan(&ready)
	if err != nil || !ready {
		return errors.New("domain schema unavailable")
	}
	return nil
}

// --- 检查发布设置、订阅与正文投影 ---
func (b *Backend) readyPublishingSchema(ctx context.Context) error {
	if _, err := b.db.ExecContext(ctx, `SELECT id,account_id,default_publisher_id,default_reply_publisher_id,default_fediverse_publisher_id,created_at,updated_at FROM owned_publishing_settings LIMIT 0`); err != nil {
		return errors.New("publishing settings schema unavailable")
	}
	var ready bool
	err := b.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='owned_text_posts'::regclass AND conname='owned_text_posts_realm_fk' AND convalidated)`).Scan(&ready)
	if err != nil || !ready {
		return errors.New("post realm schema unavailable")
	}
	if _, err := b.db.ExecContext(ctx, `SELECT last_read_at,ended_by_account_id FROM owned_publisher_follows LIMIT 0;SELECT id,publisher_id,started_at,operation_state FROM owned_livestreams LIMIT 0`); err != nil {
		return errors.New("publisher subscription state schema unavailable")
	}
	if err := b.publisherCommerce.ready(ctx); err != nil { // 发布奖励、赞助和评分依赖。
		return err
	}
	if _, err := b.db.ExecContext(ctx, `SELECT awarded_score,blog_metadata,pin_mode FROM owned_text_posts LIMIT 0; SELECT background_file_id,icon_file_id FROM owned_post_collections LIMIT 0`); err != nil {
		return err
	}
	return nil
}

// --- 检查成长来源的字段、约束与不可变性 ---
func (b *Backend) readyProgressionSources(ctx context.Context) error {
	var ready bool
	if _, err := b.db.ExecContext(ctx, `SELECT target_publisher_id,action_publisher_id,account_id,published_at,tag_id,category_id,score,featured_day,featured_rank FROM owned_post_progression_sources LIMIT 0; SELECT day,completed_at,candidate_count FROM owned_post_featured_days LIMIT 0; SELECT transaction_id,payer_wallet_id,payee_wallet_id,currency,amount,confirmed_at,cycle_days FROM commerce_v1.stellar_support_months LIMIT 0`); err != nil {
		return errors.New("progression source schema unavailable")
	}
	err := b.db.QueryRowContext(ctx, `SELECT
	 bool_and(EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=required.relation::regclass AND c.conname=required.name AND c.convalidated))
	 FROM(VALUES
	  ('owned_post_progression_sources','owned_post_progression_source_kind'),
	  ('owned_post_progression_sources','owned_post_progression_source_shape'),
	  ('owned_post_progression_sources','owned_post_progression_source_post'),
	  ('owned_post_progression_sources','owned_post_progression_source_target_publisher'),
	  ('owned_post_progression_sources','owned_post_progression_source_action_publisher'),
	  ('owned_post_progression_sources','owned_post_progression_source_account'),
	  ('owned_post_progression_sources','owned_post_progression_source_tag'),
	  ('owned_post_progression_sources','owned_post_progression_source_category'),
	  ('owned_post_featured_days','owned_post_featured_day_count'),
	  ('commerce_v1.stellar_support_months','stellar_support_month_shape'),
	  ('commerce_v1.stellar_support_months','stellar_support_month_order'),
	  ('commerce_v1.stellar_support_months','stellar_support_month_subscription'),
	  ('commerce_v1.stellar_support_months','stellar_support_month_account'),
	  ('commerce_v1.stellar_support_months','stellar_support_month_transaction'),
	  ('commerce_v1.stellar_support_months','stellar_support_month_payer'),
	  ('commerce_v1.stellar_support_months','stellar_support_month_payee'),
	  ('commerce_v1.stellar_support_months','stellar_support_month_order_index')
	 )required(relation,name)`).Scan(&ready)
	if err != nil || !ready {
		return errors.New("progression source constraints unavailable")
	}
	err = b.db.QueryRowContext(ctx, `SELECT
	 to_regclass('owned_post_progression_topical_once') IS NOT NULL
	 AND to_regclass('owned_post_progression_featured_day_rank') IS NOT NULL
	 AND to_regclass('owned_post_progression_featured_day_post') IS NOT NULL
	 AND to_regclass('owned_post_progression_due') IS NOT NULL
	 AND EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='public.owned_post_featured_days'::regclass AND conname='owned_post_featured_days_pkey' AND contype='p' AND conkey=ARRAY[(SELECT attnum FROM pg_attribute WHERE attrelid='public.owned_post_featured_days'::regclass AND attname='day' AND NOT attisdropped)]::smallint[])
	 AND to_regclass('commerce_v1.stellar_support_months_due') IS NOT NULL
	 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='owned_post_progression_sources'::regclass AND tgname='owned_post_progression_source_immutable' AND NOT tgisinternal AND tgenabled<>'D')
	 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='commerce_v1.stellar_support_months'::regclass AND tgname='stellar_support_month_immutable' AND NOT tgisinternal AND tgenabled<>'D')`).Scan(&ready)
	if err != nil || !ready {
		return errors.New("progression source integrity unavailable")
	}
	return nil
}

// --- 检查钱包、域名与账号治理 ---
func (b *Backend) readyWalletAndGovernance(ctx context.Context) error {
	if b.wallet != nil { // 换汇、资金与转账请求缺一不可。
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
	if err := b.domainCollections.ready(ctx); err != nil { // 域名信任和合集管理依赖。
		return err
	}
	if b.locations != nil { // 会面、位置和 NFC 扩展。
		if err := b.locations.Ready(ctx); err != nil {
			return errors.New("location extension schema unavailable")
		}
	}
	if err := b.accountExtras.ready(ctx); err != nil { // 改名卡与年度报告依赖。
		return err
	}
	if b.moderation != nil { // 审核记录和通知队列。
		if e := b.moderation.Ready(ctx); e != nil {
			return e
		}
	}
	if b.admin.repo != nil { // 后台管理投影。
		if e := b.admin.repo.Ready(ctx); e != nil {
			return e
		}
	}
	if b.accountSettings != nil { // 账号偏好和联系设置。
		if err := b.accountSettings.Ready(ctx); err != nil {
			return err
		}
	}
	return nil
}

// --- 检查联系方式、空间权限与聊天依赖 ---
func (b *Backend) readyContactsAndMessaging(ctx context.Context) error {
	if _, err := b.db.ExecContext(ctx, `SELECT credential_account_id,verified_at,is_public,revision FROM owned_authority_contacts LIMIT 0; SELECT order_id,address_snapshot,captured_at FROM commerce_v1.order_destinations LIMIT 0`); err != nil {
		return err
	}
	if b.translation != nil { // 翻译存储。
		if err := b.translation.Ready(ctx); err != nil {
			return err
		}
	}
	if b.autocomplete != nil { // 自动补全索引。
		if err := b.autocomplete.Ready(ctx); err != nil {
			return err
		}
	}
	if _, e := b.db.ExecContext(ctx, `SELECT role_level,generation,permission_mask FROM owned_realm_role_permissions LIMIT 0;SELECT allow_mask,deny_mask,membership_since FROM owned_realm_user_permissions LIMIT 0;SELECT owned_realm_permission_mask(NULL::uuid,NULL::uuid)`); e != nil {
		return errors.New("realm permission schema unavailable")
	}
	if b.chatOrganization != nil { // 聊天分组与组织关系。
		if err := b.chatOrganization.Ready(ctx); err != nil {
			return err
		}
	}
	if b.chat != nil { // 临时消息与聊天关系存储。
		if e := b.chat.TransientReady(ctx); e != nil {
			return e
		}
		if err := b.chat.RelationsReady(ctx); err != nil {
			return err
		}
	}
	if b.reader != nil { // 阅读记录和内容存储。
		if err := b.reader.Ready(ctx); err != nil {
			return err
		}
	}
	return nil
}

// --- 检查授权、日历与扩展服务 ---
func (b *Backend) readyConnectedServices(ctx context.Context) error {
	if b.oauth != nil { // 第三方授权和资源关系。
		if err := b.oauth.Ready(ctx); err != nil {
			return err
		}
	}
	if b.fortunes != nil { // 每日运势存储。
		if err := b.fortunes.Ready(ctx); err != nil {
			return err
		}
	}
	if b.calendar != nil { // 日历与纪念日存储。
		if err := b.calendar.Ready(ctx); err != nil {
			return err
		}
	}
	if b.bots != nil { // 执行功能另有开关，基础机器人存储仍必查。
		if b.bots.ExecutionEnabled() {
			if e := b.bots.ExecutionReady(ctx); e != nil {
				return e
			}
		}
		if err := b.bots.Ready(ctx); err != nil {
			return err
		}
	}
	if b.affiliations != nil { // 组织归属关系。
		if e := b.affiliations.Ready(ctx); e != nil {
			return e
		}
	}
	if b.tickets != nil { // 工单与回复存储。
		if err := b.tickets.Ready(ctx); err != nil {
			return err
		}
	}
	return nil
}

// --- 检查登录凭证与支付授权 ---
func (b *Backend) readyCredentialSchemas(ctx context.Context) error {
	if _, e := b.db.ExecContext(ctx, `SELECT code_hash,auth_time,methods FROM owned_authority_code_evidence LIMIT 0;SELECT session_id,session_epoch FROM owned_authority_credential_assurances LIMIT 0`); e != nil {
		return errors.New("credential assurance schema unavailable")
	}
	if _, e := b.db.ExecContext(ctx, `SELECT version,factor_id FROM wallet_v1.payment_policies LIMIT 0;SELECT hash,policy_version FROM wallet_v1.payment_authorizations LIMIT 0`); e != nil {
		return errors.New("payment authorization schema unavailable")
	}
	if b.signinEnabled { // 只有启用本地登录时才要求登录因子表。
		if _, e := b.db.ExecContext(ctx, `SELECT auth_methods FROM owned_authority_challenges LIMIT 0;SELECT id FROM owned_authority_factor_audit LIMIT 0`); e != nil {
			return errors.New("payment PIN factor schema unavailable")
		}
		var yes bool
		err := b.db.QueryRowContext(ctx, `SELECT bool_and(to_regclass(name) IS NOT NULL) FROM (VALUES ('owned_authority_credentials'),('owned_authority_factors'),('owned_authority_challenges'),('owned_authority_attempts')) AS required(name)`).Scan(&yes)
		if err != nil || !yes {
			return errors.New("sign-in schema unavailable")
		}
	}
	return nil
}

// --- 检查聊天写入、媒体与外部依赖 ---
func (b *Backend) readyChatMediaAndExternal(ctx context.Context) error {
	var columns int
	err := b.db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND ((table_name='chat_rooms' AND column_name IN ('owned_owner_id','owned_next_sequence')) OR (table_name='chat_members' AND column_name='owned_banned') OR (table_name='chat_messages' AND column_name='owned_request_hash'))`).Scan(&columns)
	if err != nil || columns != 4 {
		return errors.New("chat write schema unavailable")
	}
	if b.media != nil { // 仅检查已配置媒体；语音依赖受独立开关控制。
		if b.voiceEnabled {
			if err := b.media.VoiceReady(ctx); err != nil {
				return err
			}
		}
		if err := b.media.ArtworkReady(ctx); err != nil {
			return err
		}
		if err := b.media.Ready(ctx); err != nil {
			return errors.New("media storage unavailable")
		}
	}
	return b.external.ready(ctx, b.db)
}

// --- 将就绪结果作为健康探针返回 ---
func (b *Backend) readinessHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { // 探针只接受 GET，HEAD 错误响应也保持空正文。
		w.Header().Set("Allow", "GET")
		if r.Method == http.MethodHead {
			w = headWriter{w}
		}
		problem(w, 405, "METHOD_NOT_ALLOWED")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second) // 限制整条依赖检查的总时长。
	defer cancel()
	if err := b.Ready(ctx); err != nil {
		problem(w, 503, "BACKEND_NOT_READY")
		return
	}
	problem(w, 200, "BACKEND_READY")
}
