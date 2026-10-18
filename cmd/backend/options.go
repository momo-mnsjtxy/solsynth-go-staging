// SPDX-License-Identifier: AGPL-3.0-only
// 启动参数：所有命令行选项的默认值和解析只在这里登记，按领域保留显式开关。
// 查看可复制调用示例：go run ./cmd/backend --help
// 默认仅校验；只有加 --serve 才允许后续阶段连接数据库并监听。
package main

import (
	"flag"
	"io"
	"time"
)

// backendOptions 保存一次解析的结果；领域选项继续使用各自的边界校验。
type backendOptions struct {
	listen                      string
	serve                       bool
	dsnEnv                      string
	issuer                      string
	audience                    string
	signingKeyID                string
	trustedPublicKeyFile        string
	accessLifetime              time.Duration
	publicVisibility            string
	enableAutomodReview         bool
	enableMLS                   bool
	roomEncryption              string
	notifyAll                   string
	notifyMentions              string
	notifyNone                  string
	chatOrganizationSettings    *string
	chatProfileSettings         *string
	verificationSettings        verificationOptions
	externalSettings            externalFlagSet
	awardSettings               awardSettings
	publisherSettings           publisherCommerceSettings
	postAwardSettings           postAwardSettings
	walletExchangeSettings      walletExchangeSettings
	realmBoostSettings          realmBoostSettings
	realmMemberSettings         realmMemberSettings
	botExecutionSettings        botExecutionSettings
	botRenewalInterval          time.Duration
	ticketSettings              ticketOptions
	fortuneSettings             fortuneSettings
	readerSettings              readerSettings
	blogSettings                blogSettings
	translationSettings         translationSettings
	ipCheckSettings             ipCheckSettings
	accountExtrasSettings       accountExtraOptions
	affiliationPurchaseSettings affiliationPurchaseSettings
	operationsRetentionSettings operationsRetentionSettings
	accountConnectionSettings   connectionSettings
	pushDeviceSettings          pushDeviceSettings
	progressionSettings         progressionExtensionSettings
	passkitSettings             passkitOptions
	couponSettings              couponSettings
	mediaLocationSettings       mediaLocationSettings
	factorIssuerLabel           string
	enableOAuth                 bool
	oauthConsentURL             string
	enableSignin                bool
	issuerPrivateKeyFile        string
	refreshHashEnv              string
	replaySealEnv               string
	signinSealEnv               string
	initialMemberPermissions    string
	notificationApp             string
	mediaRoot                   string
	voiceRetention              time.Duration
	miniAppUploadTimeout        time.Duration
	maxMediaBytes               int64
	catalogFile                 string
	walletProductTreasury       string
	badgeIcons                  string
	pinPepperEnv                string
	pinPepperID                 string
	enablePINEnrollment         bool
	settlementInterval          time.Duration
	positionalArgs              int
}

// 自动续约默认周期同时用于解析与显式启用检查，变更时只修改这一处。
const defaultBotRenewalInterval = 15 * time.Second

// parseBackendOptions 登记全部原有选项，不读取密钥、策略文件或数据库。
func parseBackendOptions(args []string, out io.Writer) (*backendOptions, error) {
	options := &backendOptions{}
	fs := flag.NewFlagSet("backend", flag.ContinueOnError)
	fs.SetOutput(out)
	// --- 进程地址与身份：缺少显式身份时，配置校验会在读取文件之前拒绝启动 ---
	fs.StringVar(&options.listen, "listen", "127.0.0.1:8090", "loopback HTTP address behind trusted TLS ingress")
	fs.BoolVar(&options.serve, "serve", false, "explicitly connect and serve; otherwise validate configuration only")
	fs.StringVar(&options.dsnEnv, "dsn-env", "SOLSYNTH_BACKEND_DSN", "injected environment variable with explicit PostgreSQL URL")
	fs.StringVar(&options.issuer, "issuer", "", "exact issuer of owned authority tokens")
	fs.StringVar(&options.audience, "audience", "", "exact owned token audience")
	fs.StringVar(&options.signingKeyID, "key-id", "", "explicit public signing-key identifier")
	fs.StringVar(&options.trustedPublicKeyFile, "trusted-public-key-file", "", "explicit RSA public PEM, no certificates or private keys")
	fs.DurationVar(&options.accessLifetime, "max-access-lifetime", 10*time.Minute, "maximum access-token lifetime, matching issuer policy")
	fs.StringVar(&options.publicVisibility, "taxonomy-public-visibility", "", "explicit public visibility enum stored in database")
	fs.BoolVar(&options.enableAutomodReview, "enable-automod-review", false, "explicitly enable owned version-checked post rule review; source ranking remains default")
	fs.BoolVar(&options.enableMLS, "enable-mls-delivery", false, "opt in to owned opaque MLS delivery; requires explicit MLS schema and client validation")
	fs.StringVar(&options.roomEncryption, "messager-none-encryption", "", "explicit unencrypted room mode stored in database")
	fs.StringVar(&options.notifyAll, "messager-notify-all", "", "explicit all-notifications mode stored in database")
	fs.StringVar(&options.notifyMentions, "messager-notify-mentions", "", "explicit mentions-only notification mode")
	fs.StringVar(&options.notifyNone, "messager-notify-none", "", "explicit no-notifications mode")

	// --- 领域策略：各主体负责自己的输入格式和开关依赖，入口只负责统一登记 ---
	options.chatOrganizationSettings = chatOrganizationFlags(fs)
	options.chatProfileSettings = chatProfileFlags(fs)
	options.verificationSettings = verificationFlags(fs)
	options.externalSettings = externalFlags(fs)
	options.awardSettings = livestreamAwardFlags(fs)
	options.publisherSettings = publisherCommerceFlags(fs)
	options.postAwardSettings = postAwardFlags(fs)
	options.walletExchangeSettings = walletExchangeFlags(fs)
	options.realmBoostSettings = realmBoostFlags(fs)
	options.realmMemberSettings = realmMemberFlags(fs)
	options.botExecutionSettings = botExecutionFlags(fs)
	fs.DurationVar(&options.botRenewalInterval, "bot-renewal-interval", defaultBotRenewalInterval, "dedicated automatic bot enrollment renewal cadence, 1s through 30s")
	options.ticketSettings = ticketFlags(fs)
	options.fortuneSettings = fortuneFlags(fs)
	options.readerSettings = readerFlags(fs)
	options.blogSettings = blogFlags(fs)
	options.translationSettings = translationFlags(fs)
	options.ipCheckSettings = ipCheckFlags(fs)
	options.accountExtrasSettings = accountExtraFlags(fs)
	options.affiliationPurchaseSettings = affiliationPurchaseFlags(fs)
	options.operationsRetentionSettings = operationsRetentionFlags(fs)
	options.accountConnectionSettings = accountConnectionFlags(fs)
	options.pushDeviceSettings = pushDeviceFlags(fs)
	options.progressionSettings = progressionExtensionFlags(fs)
	options.passkitSettings = passkitFlags(fs)
	options.couponSettings = couponFlags(fs)
	options.mediaLocationSettings = mediaLocationFlags(fs)

	// --- 可选签发和账户权限：默认不开启，不隐式授予注册权限 ---
	fs.StringVar(&options.factorIssuerLabel, "factor-issuer-label", "SoSYS", "human-readable TOTP issuer label")
	fs.BoolVar(&options.enableOAuth, "enable-oauth", false, "enable owned OAuth authorization-code and OIDC userinfo flows")
	fs.StringVar(&options.oauthConsentURL, "oauth-consent-url", "", "deployed HTTPS consent UI calling the owned authorization API")
	fs.BoolVar(&options.enableSignin, "enable-signin", false, "explicitly enable owned first-party issuer and factor protocol")
	fs.StringVar(&options.issuerPrivateKeyFile, "issuer-private-key-file", "", "explicit RSA issuer key PEM")
	fs.StringVar(&options.refreshHashEnv, "issuer-refresh-hash-env", "", "explicit base64 refresh-hash key variable")
	fs.StringVar(&options.replaySealEnv, "issuer-replay-seal-env", "", "explicit independent base64 refresh-seal key variable")
	fs.StringVar(&options.signinSealEnv, "signin-seal-env", "", "explicit independent base64 factor-seal key variable")
	fs.StringVar(&options.initialMemberPermissions, "initial-member-permissions", "", "explicit comma-separated concrete initial registration permissions")
	fs.StringVar(&options.notificationApp, "notification-app", "dev.solsynth.solian", "explicit first-party inbox and stream namespace")

	// --- 私有媒体、商品和 PIN：运营方显式提供路径和密钥来源 ---
	fs.StringVar(&options.mediaRoot, "media-root", "", "explicit preprovisioned private filesystem media directory")
	fs.DurationVar(&options.voiceRetention, "chat-voice-retention", 0, "explicit owned voice retention (1h to 8760h); zero disables voice uploads")
	fs.DurationVar(&options.miniAppUploadTimeout, "miniapp-upload-timeout", 2*time.Minute, "miniapp package upload deadline (5s to 15m)")
	fs.Int64Var(&options.maxMediaBytes, "media-max-bytes", 20<<20, "maximum streamed media object bytes")
	fs.StringVar(&options.catalogFile, "catalog-config", "", "optional explicit catalog JSON snapshot")
	fs.StringVar(&options.walletProductTreasury, "wallet-product-treasury-wallet", "", "explicit trusted treasury wallet for paid wallet-product application")
	fs.StringVar(&options.badgeIcons, "badge-icons", "", "optional directory of licensed badge icons")
	fs.StringVar(&options.pinPepperEnv, "pin-pepper-env", "", "optional explicit environment variable with base64 PIN pepper")
	fs.StringVar(&options.pinPepperID, "pin-pepper-id", "", "key ID for that externally provisioned PIN policy")
	fs.BoolVar(&options.enablePINEnrollment, "enable-pin-enrollment", false, "explicitly enable six-digit factor enrollment using the configured PIN pepper")
	fs.DurationVar(&options.settlementInterval, "settlement-interval", 15*time.Minute, "bounded durable wallet lifecycle interval")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	options.positionalArgs = fs.NArg()
	return options, nil
}
