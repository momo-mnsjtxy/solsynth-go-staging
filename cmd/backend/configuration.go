// SPDX-License-Identifier: AGPL-3.0-only
// 启动配置：按既定顺序验证显式权限/领域策略，并持有本次命令打开的私有资源。
// 运行边界回归：go test ./cmd/backend -run 'TestStartup|TestPublicKeyFileAndOfflineConfiguration'
// 离线校验和正式服务复用同一份 application.Config；数据库连接只由 main.go 在 --serve 后建立。
package main

import (
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/mediastore"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/fortunes"
	walletaccounts "solsynth.local/go-migration/ports/wallet/accounts"
	"solsynth.local/go-migration/ports/wallet/exchangecatalog"
)

// backendStartup 拥有经校验的配置和本次命令的资源，避免把清理责任散落到各个分支。
type backendStartup struct {
	options             *backendOptions
	dsn                 string
	config              application.Config
	fortuneCorpus       *fortunes.Corpus
	pinPepper           []byte
	mediaRoot           *mediastore.Filesystem
	closeMediaLocations func()
}

// validateConfiguration 保留既有检查顺序：启动边界、身份、票据、消息枚举、私有资源，再到各领域策略。
// 这里会读取显式文件并锁定媒体目录，但绝不打开数据库或网络监听。
func (s *backendStartup) validateConfiguration() error {
	options := s.options
	// --- 拒绝含糊启动：地址、身份和任务周期必须先于文件或网络访问通过检查 ---
	if options.positionalArgs != 0 || !loopback(options.listen) ||
		options.issuer == "" || options.audience == "" || options.signingKeyID == "" ||
		options.settlementInterval < time.Second || options.settlementInterval > 24*time.Hour {
		return errors.New("explicit authority settings, loopback listen and valid settlement interval required")
	}
	if options.botRenewalInterval < time.Second || options.botRenewalInterval > 30*time.Second || (!*options.botExecutionSettings.enabled && options.botRenewalInterval != defaultBotRenewalInterval) {
		return errBotExecutionConfig
	}
	// pgx 会自动读取环境中的服务配置；必须先拒绝它，防止显式 DSN 被宿主设置覆盖。
	if err := ambientPostgres(); err != nil {
		return err
	}
	dsn, err := explicitDSN(os.Getenv(options.dsnEnv))
	if err != nil {
		return err
	}
	s.dsn = dsn // 只保存显式 URL，连接留在 main.go 的 --serve 分支。

	if err = s.configureAuthority(); err != nil {
		return err
	}
	ticketConfig, err := options.ticketSettings.config()
	if err != nil {
		return err
	}
	if err = options.verificationSettings.apply(s.config.Signin); err != nil {
		return err
	}
	s.config.Tickets = ticketConfig

	if err = s.configureRoomPolicy(); err != nil {
		return err
	}
	if err = s.prepareLocalResources(); err != nil {
		return err
	}

	s.config.ChatVoiceRetention = options.voiceRetention
	s.config.MiniAppUploadTimeout = options.miniAppUploadTimeout
	s.config.PostAutomodReview = options.enableAutomodReview
	s.config.EnableMLSDelivery = options.enableMLS
	s.config.WalletProductTreasuryWalletID = options.walletProductTreasury
	s.config.BadgeIconsPath = options.badgeIcons
	s.config.MaxMediaBytes = options.maxMediaBytes
	s.config.NotificationApp = options.notificationApp
	closeMediaLocations, err := options.mediaLocationSettings.apply(&s.config)
	if err != nil {
		return err
	}
	s.closeMediaLocations = closeMediaLocations // 媒体池和 NFC 随命令退出释放，早于主目录及 PIN。

	if err = s.configureAccountFeatures(); err != nil {
		return err
	}
	if err = s.configureCommerceAndRealms(); err != nil {
		return err
	}
	return s.configureContentProviders()
}

// configureAuthority 只启用显式选择的签发能力，并让验证器和签发器使用同一组身份配置。
func (s *backendStartup) configureAuthority() error {
	options := s.options
	pubKey, err := publicKey(options.trustedPublicKeyFile)
	if err != nil {
		return err
	}
	// 验证器与可选签发器共用这些身份值，不从宿主环境发现任何密钥。
	config := authority.Config{Issuer: options.issuer, Audience: options.audience, TrustedKeys: map[string]*rsa.PublicKey{options.signingKeyID: pubKey}, AccessTTL: options.accessLifetime}
	if _, err = authority.NewVerifier(authoritypg.New(nil), config); err != nil {
		return errors.New("invalid authority verification configuration")
	}
	var signinConfig *signin.Config
	if options.enableSignin || options.enableOAuth {
		config.SigningKeyID = options.signingKeyID
		config.SigningKey, err = issuerKey(options.issuerPrivateKeyFile)
		if err != nil {
			return err
		}
		config.RefreshHashKey, err = issuerSecret(options.refreshHashEnv)
		if err != nil {
			return err
		}
		config.ReplaySealKey, err = issuerSecret(options.replaySealEnv)
		if err != nil {
			return err
		}
		// 登录因子使用独立封装密钥；OAuth 单独开启时仍禁止隐式配置登录因子。
		if options.enableSignin {
			seal, e := issuerSecret(options.signinSealEnv)
			if e != nil {
				return e
			}
			if base64.StdEncoding.EncodeToString(seal) == base64.StdEncoding.EncodeToString(config.RefreshHashKey) || base64.StdEncoding.EncodeToString(seal) == base64.StdEncoding.EncodeToString(config.ReplaySealKey) {
				return errors.New("factor seal key must be independent")
			}
			if _, err = authority.New(authoritypg.New(nil), config); err != nil {
				return errors.New("invalid explicit issuer configuration")
			}
			signinConfig = &signin.Config{SealKey: seal, Issuer: options.factorIssuerLabel}
			if options.initialMemberPermissions != "" {
				for _, permission := range strings.Split(options.initialMemberPermissions, ",") {
					permission = strings.TrimSpace(permission)
					if !authority.ValidPermissionPattern(permission) || strings.Contains(permission, "*") {
						return errors.New("initial permissions must be concrete keys")
					}
					signinConfig.InitialMemberPermissions = append(signinConfig.InitialMemberPermissions, permission)
				}
			}
		} else if options.signinSealEnv != "" || options.initialMemberPermissions != "" {
			return errors.New("factor settings require enable-signin")
		}
		if _, err = authority.New(authoritypg.New(nil), config); err != nil {
			return errors.New("invalid explicit issuer configuration")
		}
	} else if options.issuerPrivateKeyFile != "" || options.refreshHashEnv != "" || options.replaySealEnv != "" || options.signinSealEnv != "" || options.initialMemberPermissions != "" {
		return errors.New("issuer configuration requires explicit enable-signin or enable-oauth")
	}
	s.config.Authority = config
	s.config.Signin = signinConfig
	return nil
}

// configureRoomPolicy 按数据库现存枚举构造消息策略，不推断任何默认权限或通知含义。
func (s *backendStartup) configureRoomPolicy() error {
	options := s.options
	publicVisibility, err := explicitInt(options.publicVisibility, "taxonomy-public-visibility")
	if err != nil {
		return err
	}
	encryption, err := explicitInt(options.roomEncryption, "messager-none-encryption")
	if err != nil {
		return err
	}
	notifyAll, err := explicitInt(options.notifyAll, "messager-notify-all")
	if err != nil {
		return err
	}
	notifyMentions, err := explicitInt(options.notifyMentions, "messager-notify-mentions")
	if err != nil {
		return err
	}
	notifyNone, err := explicitInt(options.notifyNone, "messager-notify-none")
	if err != nil {
		return err
	}
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: encryption, NotifyAll: notifyAll, NotifyMentions: notifyMentions, NotifyNone: notifyNone}
	if _, err = messagerpg.NewWithIndependentPolicy(nil, policy); err != nil {
		return errors.New("notification enum values must be distinct")
	}
	s.config.PublicVisibility = &publicVisibility
	s.config.MessagerPolicy = &policy
	return nil
}

// prepareLocalResources 打开显式私有目录、解码 PIN pepper 并读取商品目录；失败清理由命令统一承担。
func (s *backendStartup) prepareLocalResources() error {
	options := s.options
	peppers := map[string][]byte{}
	s.config.PINPeppers = peppers
	if options.enablePINEnrollment && (!options.enableSignin || options.pinPepperID == "") {
		return errors.New("PIN enrollment requires sign-in and an explicit PIN pepper")
	}
	if (options.pinPepperEnv == "") != (options.pinPepperID == "") {
		return errors.New("PIN pepper environment and ID must be supplied together")
	}
	if options.pinPepperEnv != "" {
		raw, err := base64.StdEncoding.DecodeString(os.Getenv(options.pinPepperEnv))
		if err != nil || len(raw) < 32 || len(options.pinPepperID) > 64 {
			return errors.New("invalid injected PIN pepper")
		}
		s.pinPepper = raw // 把擦除责任交给本次启动，后面的领域校验失败时同样会执行。
		peppers[options.pinPepperID] = raw
	}
	var objectStore media.ObjectStore
	if options.maxMediaBytes <= 0 || options.maxMediaBytes > 1<<30 {
		return errors.New("media maximum must be positive and at most one GiB")
	}
	if options.mediaRoot != "" {
		store, e := mediastore.NewFilesystem(options.mediaRoot, options.maxMediaBytes)
		if e != nil {
			return errors.New("media root unavailable or not private")
		}
		s.mediaRoot = store // 固定已校验目录，不创建目录，也不让策略失败留下文件句柄。
		objectStore = store
	}
	var catalogProviders []exchangecatalog.Provider
	if options.catalogFile != "" {
		file, e := os.Open(options.catalogFile)
		if e != nil {
			return errors.New("catalog configuration unavailable")
		}
		provider, e := exchangecatalog.LoadJSON(file) // 商品只来自运营方指定的快照。
		closeErr := file.Close()
		if e != nil || closeErr != nil {
			return errors.New("catalog configuration invalid")
		}
		catalogProviders = []exchangecatalog.Provider{provider}
	}
	if options.walletProductTreasury != "" && (!walletaccounts.ValidID(options.walletProductTreasury) || options.catalogFile == "") {
		return errors.New("wallet product treasury requires an explicit wallet UUID and catalog configuration")
	}
	s.config.MediaStore = objectStore
	s.config.CatalogProviders = catalogProviders
	return nil
}

// configureAccountFeatures 应用账户、聊天与 OAuth 的显式授权条件。
func (s *backendStartup) configureAccountFeatures() error {
	options := s.options
	var err error
	if err := applyChatOrganization(&s.config, *options.chatOrganizationSettings); err != nil {
		return err
	}
	if err := applyChatProfile(&s.config, *options.chatProfileSettings); err != nil {
		return err
	}
	if options.enablePINEnrollment {
		s.config.PINEnrollmentPepperID = options.pinPepperID
	}
	if err = options.passkitSettings.apply(&s.config); err != nil {
		return err
	}
	if err = options.progressionSettings.apply(&s.config); err != nil {
		return err
	}
	if err = options.pushDeviceSettings.apply(&s.config); err != nil {
		return err
	}
	if err = options.accountConnectionSettings.apply(&s.config); err != nil {
		return err
	}
	if err = options.accountExtrasSettings.apply(&s.config); err != nil {
		return err
	}
	if options.enableOAuth {
		u, e := url.Parse(options.oauthConsentURL)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("explicit HTTPS OAuth consent UI required")
		}
		s.config.OAuthEnabled = true
		s.config.OAuthConsentURL = options.oauthConsentURL
	} else if options.oauthConsentURL != "" {
		return errors.New("OAuth consent UI requires explicit enablement")
	}
	return nil
}

// configureCommerceAndRealms 保留领域依赖顺序，例如先配置空间助力，再允许空间成员策略。
func (s *backendStartup) configureCommerceAndRealms() error {
	options := s.options
	var err error
	if err = options.externalSettings.apply(&s.config); err != nil {
		return err
	}
	if err = options.awardSettings.apply(&s.config); err != nil {
		return err
	}
	if err = options.publisherSettings.apply(&s.config); err != nil {
		return err
	}
	if err = options.postAwardSettings.apply(&s.config); err != nil {
		return err
	}
	if err = options.walletExchangeSettings.apply(&s.config); err != nil {
		return err
	}
	if err = options.affiliationPurchaseSettings.apply(&s.config); err != nil {
		return err
	}
	if err = options.operationsRetentionSettings.apply(&s.config); err != nil {
		return err
	}
	// 成员策略依赖空间助力的既有配置，顺序不可颠倒。
	if err = options.realmBoostSettings.apply(&s.config); err != nil {
		return err
	}
	if err = options.realmMemberSettings.apply(&s.config); err != nil {
		return err
	}
	if err = options.botExecutionSettings.apply(&s.config); err != nil {
		return err
	}
	return nil
}

// configureContentProviders 将运营方明确提供的内容与外部服务策略装入同一份配置。
func (s *backendStartup) configureContentProviders() error {
	options := s.options
	fortuneConfig, fortuneCorpus, err := options.fortuneSettings.config()
	if err != nil {
		return err
	}
	s.config.Fortunes = fortuneConfig
	s.config.CommerceCouponPolicy, err = options.couponSettings.config()
	if err != nil {
		return err
	}
	s.config.IPCheck, err = options.ipCheckSettings.config()
	if err != nil {
		return err
	}
	s.config.Translation, err = options.translationSettings.config()
	if err != nil {
		return err
	}
	s.config.Reader, err = options.readerSettings.config()
	if err != nil {
		return err
	}
	s.config.Blogs, err = options.blogSettings.config()
	if err != nil {
		return err
	}
	s.fortuneCorpus = fortuneCorpus // 校验阶段只解析；安装语料必须等数据库与表就绪。
	return nil
}

// describeOfflineConfiguration 仅输出策略承诺，不连接数据库，也不输出数据库 URL 或密钥。
func (s *backendStartup) describeOfflineConfiguration(out io.Writer) error {
	if s.config.WalletExchange != nil {
		policy, e := walletaccounts.NewExchangePolicy(*s.config.WalletExchange)
		if e != nil {
			return errWalletExchangeConfig
		}
		if _, e = fmt.Fprintf(out, "Wallet exchange policy version=%s sha256=%s. Active policy installation and treasury funding are separate operator actions.\n", policy.Config().Version, policy.Digest()); e != nil {
			return e
		}
	}
	_, err := fmt.Fprintln(out, "Backend configuration validated. No database connection, schema, account, key, grant, listener or transfer was created.")
	return err
}

// closeConfiguredResources 与打开顺序相反：媒体池/NFC、主媒体目录，最后擦除 PIN pepper。
func (s *backendStartup) closeConfiguredResources() {
	if s.closeMediaLocations != nil {
		s.closeMediaLocations()
	}
	if s.mediaRoot != nil {
		s.mediaRoot.Close()
	}
	clear(s.pinPepper)
}

// explicitInt 要求运营方提供带符号 int32 枚举，避免从零值推断数据库策略。
func explicitInt(value, name string) (int32, error) {
	n, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("explicit signed-int32 %s is required", name)
	}
	return int32(n), nil
}
