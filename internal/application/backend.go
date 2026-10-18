// SPDX-License-Identifier: AGPL-3.0-only
// 应用装配入口：校验显式政策，按业务依赖连接仓库、HTTP 入口与维护任务。
// 调用：宿主持有数据库连接，NewPostgres 只组装，不迁移、不访问数据库、不启动任务。
// 示例：backend, err := application.NewPostgres(db, db, config)；随后由宿主提供 HTTP 与调度。
package application

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"solsynth.local/go-migration/internal/accounthistorypg"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/accountsettingspg"
	"solsynth.local/go-migration/internal/activitypg"
	"solsynth.local/go-migration/internal/affiliationpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/autocompletepg"
	"solsynth.local/go-migration/internal/blogpg"
	"solsynth.local/go-migration/internal/botpg"
	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/internal/commercepg"
	"solsynth.local/go-migration/internal/connectionpg"
	"solsynth.local/go-migration/internal/fortunepg"
	"solsynth.local/go-migration/internal/livestreampg"
	"solsynth.local/go-migration/internal/locationpg"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/moderationpg"
	"solsynth.local/go-migration/internal/namecardpg"
	"solsynth.local/go-migration/internal/notabledaypg"
	"solsynth.local/go-migration/internal/notificationpg"
	"solsynth.local/go-migration/internal/oauthpg"
	"solsynth.local/go-migration/internal/postawardpg"
	"solsynth.local/go-migration/internal/postembedpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/progressionpg"
	"solsynth.local/go-migration/internal/publishernotificationpg"
	"solsynth.local/go-migration/internal/publisherratingpg"
	"solsynth.local/go-migration/internal/readerpg"
	"solsynth.local/go-migration/internal/realmboostpg"
	"solsynth.local/go-migration/internal/realmmemberpg"
	"solsynth.local/go-migration/internal/rewindpg"
	"solsynth.local/go-migration/internal/signinpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/internal/sponsorshippg"
	"solsynth.local/go-migration/internal/taxonomypg"
	"solsynth.local/go-migration/internal/ticketpg"
	"solsynth.local/go-migration/internal/translationpg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/notifications"
	profiles "solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/activities"
	"solsynth.local/go-migration/ports/passport/board"
	"solsynth.local/go-migration/ports/passport/fortunes"
	"solsynth.local/go-migration/ports/passport/ipcheck"
	"solsynth.local/go-migration/ports/passport/passkit"
	"solsynth.local/go-migration/ports/passport/social"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
	"solsynth.local/go-migration/ports/sphere/locations"
	"solsynth.local/go-migration/ports/sphere/taxonomy"
	walletaccounts "solsynth.local/go-migration/ports/wallet/accounts"
	"solsynth.local/go-migration/ports/wallet/commerce"
	"solsynth.local/go-migration/ports/wallet/exchangecatalog"
)

// Config is injected by the embedding application. Enum values are mandatory
// explicit database-policy decisions; zero is never silently treated as public.
type Config struct {
	AccountConnections      *connectionpg.Config
	SteamPresence           activitypg.ProviderObserver
	LastFMPresence          activitypg.ProviderObserver
	ProgressionSourcePoints *progressionpg.SourcePointConfig
	ProgressionCheckIn      *progressionpg.CheckInPolicy
	ProgressionCaptcha      signin.Captcha
	NotificationDevices     *notificationpg.DeviceConfig
	NightOwlReminders       bool
	WalletExchange          *walletaccounts.ExchangeConfig // Explicit opt-in funded treasury policy; no provisioning.
	AffiliationPurchase     *affiliationpg.PurchaseConfig
	ChatVoiceRetention      time.Duration  // Explicit owned retention; zero leaves voice storage unconfigured.
	ChatProfilePerks        map[string]int // Explicit level-2 nickname entitlement; nil fails closed.
	ChatSearchPerks         map[string]int // Explicit canonical subscription-to-perk policy.
	RealmMembers            *realmmemberpg.Config
	BotExecution            *BotExecutionConfig
	MiniAppUploadTimeout    time.Duration // Bounded package read, validation, storage, and association deadline.
	RealmBoosts             *realmboostpg.Config
	// PostAutomodReview enables the owned version-checked review extension.
	PostAutomodReview             bool
	AppleWallet                   *passkit.Config
	AppleWalletProgramGroup       string
	EnableMLSDelivery             bool // Explicit owned opaque delivery capability; requires MLSSchema.
	PostAwards                    *postawardpg.Config
	Sponsorship                   *sponsorshippg.Config
	PublisherRatings              *publisherratingpg.Config
	Livestreams                   *LivestreamConfig
	LivestreamAwardMaintenance    *livestreampg.AwardMaintenanceConfig
	NameCards                     *namecardpg.Options
	Rewind                        *rewindpg.Options
	IPCheck                       *ipcheck.Config
	Translation                   *translationpg.Config
	Reader                        *readerpg.Config
	Blogs                         *blogpg.Config
	OAuthEnabled                  bool
	OAuthConsentURL               string
	Fortunes                      *fortunepg.Config
	Authority                     authority.Config
	PublicVisibility              *int32
	MessagerPolicy                *messagerpg.IndependentPolicy
	PINPeppers                    map[string][]byte
	PINEnrollmentPepperID         string
	SessionService                *authority.Service
	AllowNonAtomicAuthorityWrites bool
	CatalogProviders              []exchangecatalog.Provider
	WalletProductTreasuryWalletID string
	BadgeIconsPath                string
	MediaStore                    media.ObjectStore
	MaxMediaBytes                 int64
	MediaPools                    map[string]media.ObjectStore
	MediaRemoteFetcher            media.RemoteFetcher
	MediaRenderer                 media.RasterRenderer
	NFCKeyEncryptionKey           []byte
	NFCLocker                     locations.NFCLocker
	LocationGeocoder              locations.Geocoder
	NotificationApp               string
	NotificationSender            notifications.Sender
	CommerceProviders             map[string]commerce.Provider
	CommerceCouponPolicy          commerce.CouponPolicy
	Signin                        *signin.Config
	Tickets                       *ticketpg.Config
	Calls                         *CallConfig
	Federation                    *FederationConfig
	OperationsRetention           *OperationsRetentionConfig
}
type Backend struct {
	accountConnections       accountConnectionModules
	notificationDevices      notificationDeviceModule
	accountHistory           *accounthistorypg.Repository
	surveyNotifications      func(context.Context, int) (int, error)
	activities               *activitypg.Repository
	chatOrganization         *chatpg.Organization
	postComposition          *postembedpg.Repository
	blogs                    *blogpg.Repository
	realmMembers             realmMemberModules
	realmBoosts              realmBoostModules
	scheduledPosts           *postpg.Repository
	affiliations             *affiliationpg.Repository
	passkit                  passkitModules
	social                   *socialpg.Repository
	publisherCommerce        publisherCommerceModules
	domainCollections        domainCollectionModules
	admin                    adminModules
	moderation               *moderationpg.Repository
	moderationSink           *notificationpg.Repository
	moderationApp            string
	accountExtras            accountExtraModules
	accountSettings          *accountsettingspg.Repository
	translation              *translationpg.Repository
	autocomplete             *autocompletepg.Repository
	chat                     *chatpg.Repository
	reader                   *readerpg.Repository
	oauth                    *oauthpg.Repository
	fortunes                 *fortunepg.Repository
	handler                  http.Handler
	verifier                 *authority.Verifier
	db, authorityDB          *sql.DB
	wallet                   *walletpg.Repository
	media                    *mediapg.Repository
	voiceEnabled             bool
	notificationWorker       *notificationpg.Worker
	publisherNotifications   *publishernotificationpg.Repository
	operationsRetention      *OperationsRetentionConfig
	collectionNotifications  func(context.Context, int) (int, error)
	commerce                 *commercepg.Repository
	locations                *locationpg.Repository
	progression              *progressionpg.Repository
	progressionNotifications progressionNotifications
	tickets                  *ticketpg.Repository
	bots                     *botpg.Repository
	calendar                 *notabledaypg.Repository
	signinEnabled            bool
	external                 externalModules
}

func (b *Backend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/readyz" {
		b.readinessHTTP(w, r)
		return
	}
	b.handler.ServeHTTP(w, r)
}

// NewPostgres builds a single coherent modular application and performs no I/O,
// migration, account enrollment, key generation or policy seeding. Its caller
// owns the DB pool and startup readiness. Authority data may be on a SELECT-only
// separate pool; nil authorityDB uses db for the explicitly combined runtime.
func NewPostgres(db, authorityDB *sql.DB, c Config) (*Backend, error) {
	if db == nil || c.PublicVisibility == nil || *c.PublicVisibility != 0 || c.MessagerPolicy == nil {
		return nil, errors.New("backend: database and explicit projection policies required")
	}
	if c.OperationsRetention != nil && !c.OperationsRetention.valid() {
		return nil, errors.New("invalid operations retention policy")
	}
	if authorityDB == nil {
		authorityDB = db
	}
	if c.AppleWallet != nil && authorityDB != db {
		return nil, errors.New("Apple Wallet requires canonical same-pool authority")
	}
	var exchangePolicy *walletaccounts.ExchangePolicy
	if c.WalletExchange != nil {
		if authorityDB != db {
			return nil, errors.New("wallet exchange requires canonical same-pool authority")
		}
		var e error
		exchangePolicy, e = walletaccounts.NewExchangePolicy(*c.WalletExchange)
		if e != nil {
			return nil, errors.New("invalid explicit wallet exchange policy")
		}
	}
	aStore := authoritypg.New(authorityDB)
	v, err := authority.NewVerifier(aStore, c.Authority)
	if err != nil {
		return nil, err
	}
	identity := identity{v}
	guard := authoritypg.NewTransactionGuard(aStore, v)
	if authorityDB != db && c.AllowNonAtomicAuthorityWrites {
		guard = nil
	}
	external, err := externalModulesFor(db, authorityDB, c, v, guard)
	if err != nil {
		return nil, err
	}
	domainCollections, err := domainCollectionsFor(db, guard, v, c.Authority.Now)
	if err != nil {
		return nil, err
	}
	paymentPIN := walletpg.NewPINVerifier(db, c.PINPeppers).WithAuthority(guard).WithClock(c.Authority.Now).WithEnrollmentPepper(c.PINEnrollmentPepperID)
	if c.PINEnrollmentPepperID != "" && (len(c.PINEnrollmentPepperID) > 64 || len(c.PINPeppers[c.PINEnrollmentPepperID]) < 32 || guard == nil || authorityDB != db || c.Signin == nil) {
		return nil, errors.New("PIN enrollment requires explicit pepper and same-pool sign-in authority")
	}
	signinRepo := signinpg.New(db, aStore, guard).WithPaymentPIN(paymentPIN)
	accountRepo := accountpg.NewWithAuthority(db, guard).WithPublicContacts(signinRepo)
	accountHTTP := bindIdentity(v, bindTransactionAuthority(guard, (&profiles.Service{Repository: accountRepo, Authority: proofIdentity{identity}}).Handler()))
	// 按依赖顺序组装业务；每个动作保留原有权限默认及配置校验。
	reading, err := readingContentFor(db, authorityDB, c, v, guard)
	if err != nil {
		return nil, err
	}
	developer, err := developerCommerceFor(db, c, v, guard, aStore, paymentPIN)
	if err != nil {
		return nil, err
	}
	chat, err := chatRoomsFor(db, c, v, guard, developer.commerceRepo, developer.botRepo)
	if err != nil {
		return nil, err
	}
	taxonomyHTTP := taxonomy.NewAuthorityHandler(taxonomypg.NewWithAuthority(db, *c.PublicVisibility, guard), taxonomyIdentity{identity, publisherRepository{accountRepo}}, time.Now)
	accounts, err := accountLifecycleFor(db, c, v, guard, developer.commerceRepo, signinRepo)
	if err != nil {
		return nil, err
	}
	locationRepo := locationpg.NewWithAuthority(db, guard)
	if err = locationRepo.WithEncryptedNFC(c.NFCKeyEncryptionKey, c.NFCLocker); err != nil {
		return nil, err
	}
	locationRepo.WithGeocoder(c.LocationGeocoder)
	locationHTTP := locations.NewHandler(locationRepo, postIdentity{identity})
	// 圈子访问权同时连接聊天、账号与呼叫，随后再创建内容投影。
	socialRepo := socialpg.NewWithAuthority(db, guard)
	chat.chatRepo.WithSocial(socialRepo)
	chat.messageReader.WithRoomAccess(chat.chatRepo)
	if external.calls != nil {
		external.calls.WithRoomAccess(chat.chatRepo)
	}
	accountRepo.WithRealms(socialRepo)
	socialHTTP := (&social.Service{Repository: socialRepo, Authority: proofIdentity{identity}}).Handler()
	publishing, err := postPublishingFor(db, authorityDB, c, v, guard, socialRepo, reading.readerRepo)
	if err != nil {
		return nil, err
	}
	walletHTTP, wallet := walletHandler(db, identity, paymentPIN, guard, socialRepo)
	wallet.WithClock(c.Authority.Now).WithExchangePolicy(exchangePolicy)
	auth, err := authenticationFor(db, authorityDB, c, v, guard, aStore, signinRepo, developer.appRepo)
	if err != nil {
		return nil, err
	}
	storage, err := mediaStorageFor(
		db,
		authorityDB,
		c,
		v,
		guard,
		accountRepo,
		developer.appRepo,
		developer.botRepo,
		chat.chatRepo,
		chat.chatMedia,
		chat.roomMedia,
		publishing.postMedia,
		accounts.ticketRepo,
		locationRepo,
		external,
	)
	if err != nil {
		return nil, err
	}
	passkitModule, err := passkitFor(db, c, v, guard)
	if err != nil {
		return nil, err
	}
	calendar, err := calendarFor(db, c, v, guard, accounts.fortuneRepo, storage.calendarMedia)
	if err != nil {
		return nil, err
	}
	bots, err := botDeliveryFor(c, v, guard, developer.botRepo, chat.chatRepo, storage.mediaRepo, storage.developerMedia)
	if err != nil {
		return nil, err
	}
	engagement, err := contentEngagementFor(db, v, guard, socialRepo, chat.chatRepo, wallet, storage.mediaRepo)
	if err != nil {
		return nil, err
	}
	if c.NotificationApp == "" {
		c.NotificationApp = "dev.solsynth.solian"
	}
	engagement.surveysRepo.WithNotificationApp(c.NotificationApp)
	publishing.interactionRepo.WithPublicationNotifications(c.NotificationApp)
	delivery, err := notificationsFor(
		db,
		c,
		v,
		guard,
		accounts.activityRepo,
		accounts.presenceRepo,
		accounts.progressionRepo,
		chat.chatRepo,
		chat.chatOrganization,
		engagement.surveysRepo,
		publishing.interactionRepo,
		publishing.postRepository,
		publishing.timelineRepo,
		socialRepo,
	)
	if err != nil {
		return nil, err
	}
	catalogs, err := publicCatalogs(c)
	if err != nil {
		return nil, err
	}
	ipHTTP, err := ipCheckHandler(c, v)
	if err != nil {
		return nil, err
	}
	extras, err := accountExtrasFor(db, c, v, guard, developer.commerceRepo, accountRepo, socialRepo, chat.chatRepo)
	if err != nil {
		return nil, err
	}
	var moderationRepo *moderationpg.Repository
	var moderationHTTP http.Handler = content.NewHandler(nil, nil)
	if guard != nil {
		moderationRepo, err = moderationpg.New(db, guard, moderationpg.Config{Now: c.Authority.Now, PostAssociations: publishing.timelineRepo, AutomodReview: c.PostAutomodReview})
		if err != nil {
			return nil, err
		}
		moderationHTTP = bindIdentity(v, bindTransactionAuthority(guard, content.NewHandler(moderationRepo, postIdentity{identity})))
	}
	if external.federation != nil {
		accountRepo.WithPublishingFederation(external.federation)
	}
	streamHTTP, err := external.configureStreams(db, c, v, guard, accountRepo, socialRepo, storage.mediaRepo, developer.commerceRepo)
	if err != nil {
		return nil, err
	}
	if external.streams != nil {
		external.streams.WithActivationObserver(delivery.publisherNotifications)
		publishing.timelineRepo.WithSubscriptionStreams(external.streams.SubscriptionReader())
	} else {
		var subscriptionMedia live.MediaProvider
		if storage.mediaRepo != nil {
			subscriptionMedia = storage.mediaRepo
		}
		publishing.timelineRepo.WithSubscriptionStreams(livestreampg.NewSubscriptionReader(accountRepo, socialRepo, subscriptionMedia))
	}
	admin, err := adminModulesFor(db, guard, v, c.Authority.Now)
	if err != nil {
		return nil, err
	}
	if admin.repo != nil {
		admin.repo.WithPresenceScanner(accounts.activityRepo)
		if c.NightOwlReminders {
			admin.repo.WithNightOwlSender(delivery.notificationRepo)
		}
	}
	publisherCommerce, err := publisherCommerceFor(db, c, guard, v, socialRepo, publishing.timelineRepo, developer.commerceRepo)
	if err != nil {
		return nil, err
	}
	realmBoosts, err := realmBoostsFor(db, guard, v, developer.commerceRepo, socialRepo, moderationRepo, c.RealmBoosts)
	if err != nil {
		return nil, err
	}
	realmMembers, err := realmMembersFor(db, guard, v, realmBoosts.repo, socialRepo, c.RealmMembers)
	if err != nil {
		return nil, err
	}
	// 业务 Handler 统一在此绑定身份与事务证明，避免漏掉入口的权限边界。
	h := router{modules: modules{
		notificationDevices: delivery.deviceModule.handler,
		presenceSensors:     (&activities.Service{Repository: accounts.activityRepo, Authority: v, Bind: accounts.bindProof}).SensorHandler(),
		accountConnections:  accounts.accountConnections.handler,
		accountHistory:      accounts.accountHistoryHTTP,
		realmMemberHistory:  realmMembers.repo != nil,
		realmMembers:        realmMembers.handler,
		blogs:               publishing.blogHTTP,
		realmBoosts:         realmBoosts.handler,
		affiliations:        accounts.affiliationHTTP,
		passkit:             passkitModule,
		artwork:             bindIdentity(v, bindTransactionAuthority(guard, storage.artworkHTTP)),
		publisherCommerce:   publisherCommerce,
		domainCollections:   domainCollections,
		admin:               admin,
		livestreams:         streamHTTP,
		moderation:          moderationHTTP,
		namecards:           extras.cardsHTTP,
		rewind:              extras.rewindHTTP,
		ipcheck:             ipHTTP,
		accountSettings:     delivery.settingsHTTP,
		translation:         reading.translationHTTP,
		autocomplete:        engagement.autocompleteHTTP,
		reader:              reading.readerHTTP,
		oauth:               auth.oauthHTTP,
		fortunes:            accounts.fortuneHTTP,
		calendar:            calendar.calendarHTTP,
		bots:                bots.botHTTP,
		tickets:             accounts.ticketHTTP,
		merchants:           bindIdentity(v, bindTransactionAuthority(guard, developer.merchantHTTP)),
		calls:               external.callHTTP,
		federationPublic:    external.federationPublic,
		federationPrivate:   external.federationPrivate,
		presence:            accounts.presenceHTTP,
		activities:          accounts.activityHTTP,
		friends:             accounts.friendsHTTP,
		progression:         accounts.progressionHTTP,
		locations:           bindIdentity(v, bindTransactionAuthority(guard, locationHTTP)),
		surveys:             bindIdentity(v, bindTransactionAuthority(guard, engagement.surveysHTTP)),
		stickers:            bindIdentity(v, bindTransactionAuthority(guard, engagement.stickersHTTP)),
		timeline:            bindIdentity(v, bindTransactionAuthority(guard, publishing.timelineHTTP)),
		signin:              auth.signinHTTP,
		commerce:            bindIdentity(v, bindTransactionAuthority(guard, developer.commerceHTTP)),
		apps:                developer.appHTTP,
		notifications:       bindIdentity(v, bindTransactionAuthority(guard, delivery.notificationHTTP)),
		realtime:            newSharedStreamTickets(authorityDB, v, bindIdentity(v, bindTransactionAuthority(guard, delivery.realtimeHTTP))),
		media:               bindIdentity(v, bindTransactionAuthority(guard, storage.mediaHTTP)),
		social:              bindIdentity(v, bindTransactionAuthority(guard, socialHTTP)),
		interactions:        bindIdentity(v, bindTransactionAuthority(guard, publishing.interactionHTTP)),
		public:              bindIdentity(v, catalogs),
		board:               bindIdentity(v, bindTransactionAuthority(guard, developer.boardHTTP)),
		taxonomy:            bindIdentity(v, bindTransactionAuthority(guard, taxonomyHTTP)),
		posts:               bindIdentity(v, bindTransactionAuthority(guard, publishing.postsHTTP)),
		messager:            bindIdentity(v, bindTransactionAuthority(guard, chat.chatHTTP)),
		wallet:              walletHTTP,
		accounts:            accountHTTP,
		session:             auth.sessionHTTP,
	}}
	return &Backend{
		notificationDevices:      delivery.deviceModule,
		accountConnections:       accounts.accountConnections,
		collectionNotifications:  delivery.collectionNotifications,
		publisherNotifications:   delivery.publisherNotifications,
		accountHistory:           accounts.accountHistoryRepo,
		surveyNotifications:      delivery.surveyNotifications,
		activities:               accounts.activityRepo,
		chatOrganization:         chat.chatOrganization,
		postComposition:          publishing.postComposition,
		realmMembers:             realmMembers,
		blogs:                    publishing.blogRepo,
		realmBoosts:              realmBoosts,
		scheduledPosts:           publishing.scheduledPosts,
		affiliations:             accounts.affiliationRepo,
		social:                   socialRepo,
		passkit:                  passkitModule,
		publisherCommerce:        publisherCommerce,
		domainCollections:        domainCollections,
		admin:                    admin,
		moderation:               moderationRepo,
		moderationSink:           delivery.notificationRepo,
		moderationApp:            c.NotificationApp,
		accountExtras:            extras,
		accountSettings:          delivery.settingsRepo,
		translation:              reading.translationRepo,
		autocomplete:             engagement.autocompleteRepo,
		chat:                     chat.chatRepo,
		reader:                   reading.readerRepo,
		oauth:                    auth.oauthRepo,
		fortunes:                 accounts.fortuneRepo,
		external:                 external,
		handler:                  captureRequestAudit(v, h),
		verifier:                 v,
		wallet:                   wallet,
		media:                    storage.mediaRepo,
		voiceEnabled:             c.ChatVoiceRetention != 0,
		notificationWorker:       delivery.notificationWorker,
		operationsRetention:      c.OperationsRetention,
		commerce:                 developer.commerceRepo,
		locations:                locationRepo,
		progression:              accounts.progressionRepo,
		tickets:                  accounts.ticketRepo,
		bots:                     developer.botRepo,
		calendar:                 calendar.calendarRepo,
		progressionNotifications: progressionNotifications{delivery.notificationRepo, c.NotificationApp},
		signinEnabled:            auth.signinService != nil,
		db:                       db,
		authorityDB:              authorityDB,
	}, nil

}

type accountLookup interface {
	LookupAccount(context.Context, string) (authority.Account, error)
}
type accountResolver struct{ store accountLookup }

func (a accountResolver) Resolve(ctx context.Context, reference string) (board.Account, error) {
	account, err := a.store.LookupAccount(ctx, reference)
	if errors.Is(err, authority.ErrNotFound) || account.Disabled {
		return board.Account{}, board.ErrAccountNotFound
	}
	if err != nil {
		return board.Account{}, &board.DependencyError{Adapter: "account directory"}
	}
	return board.Account{ID: account.ID, Name: account.Name}, nil
}

// ProcessDue advances configured lifecycle/projection workers in bounded batches.
// Construction starts no scheduler; cancellation leaves recoverable work durable.
func (b *Backend) ProcessDue(ctx context.Context, limit int) (int, error) {
	if b == nil || b.wallet == nil {
		return 0, errors.New("backend: wallet unavailable")
	}
	if limit < 1 || limit > 1000 {
		return 0, errors.New("lifecycle batch size must be between 1 and 1000")
	}
	n, e := b.wallet.ProcessDue(ctx, limit)
	if b.chat != nil {
		m, x := b.chat.ExpirePlaceholders(ctx, limit)
		n += m
		e = errors.Join(e, x)
	}
	connectionCount, connectionErr := b.accountConnections.processDue(ctx, limit)
	n += connectionCount
	e = errors.Join(e, connectionErr)
	if b.blogs != nil && b.blogs.VerificationEnabled() {
		m, x := b.blogs.ProcessPending(ctx, min(limit, 100))
		n += m
		e = errors.Join(e, x)
	}
	if b.bots != nil && b.bots.ExecutionEnabled() {
		m, x := b.bots.ProcessEnrollmentRenewalsDue(ctx, min(limit, 100))
		n += m
		e = errors.Join(e, x)
		m, x = b.bots.ProcessMessagesDue(ctx, min(limit, 100))
		n += m
		e = errors.Join(e, x)
	}
	if b.scheduledPosts != nil {
		m, x := b.scheduledPosts.ProcessScheduled(ctx, limit)
		n += m
		e = errors.Join(e, x)
	}
	if b.social != nil {
		m, x := b.social.ProcessExpiredRelationships(ctx, limit)
		n += m
		e = errors.Join(e, x)
	}
	funds, fundErr := b.wallet.ProcessFundsDue(ctx, limit)
	n += funds
	e = errors.Join(e, fundErr)
	if b.oauth != nil {
		m, x := b.oauth.ProcessDue(ctx, min(limit, 100))
		n += m
		e = errors.Join(e, x)
	}
	if b.commerce != nil {
		m, x := b.commerce.ProcessDue(ctx, limit)
		n += m
		e = errors.Join(e, x)
	}
	if b.moderation != nil {
		m, x := b.moderation.ProcessNotifications(ctx, b.moderationSink, b.moderationApp, min(limit, 100))
		n += m
		e = errors.Join(e, x)
	}
	if b.realmMembers.repo != nil {
		m, x := b.realmMembers.repo.ProcessDue(ctx, limit)
		n += m
		e = errors.Join(e, x)
	}
	l, le := b.locations.Expire(ctx, limit)
	if b.notificationDevices.repo != nil {
		m, x := b.notificationDevices.repo.ProcessDue(ctx, limit)
		n += m
		e = errors.Join(e, x)
	}
	providerCount, providerErr := b.activities.ProcessProviders(ctx, limit)
	n += providerCount
	e = errors.Join(e, providerErr)
	p, pe := b.progression.ProcessDue(ctx, limit)
	pointCount, pointErr := b.progression.ProcessSourcePoints(ctx, limit)
	n += pointCount
	e = errors.Join(e, pointErr)
	d, de := b.progression.DispatchCompletions(ctx, limit, b.progressionNotifications)
	if b.tickets != nil {
		rewards, re := b.tickets.RetryPendingRewards(ctx, limit)
		n += rewards
		e = errors.Join(e, re)
	}
	retained, retentionErr := b.processOperationsRetention(ctx)
	n += retained
	e = errors.Join(e, retentionErr)
	m, x := b.external.processDue(ctx, min(limit, 100))
	return n + int(l) + p + d + m, errors.Join(e, le, pe, de, x)
}

// InstallFortuneCorpus installs only explicitly provided operator configuration.
func (b *Backend) InstallFortuneCorpus(ctx context.Context, c fortunes.Corpus) error {
	if b == nil || b.fortunes == nil {
		return fortunes.ErrUnavailable
	}
	return b.fortunes.InstallCorpus(ctx, c)
}

// ProcessFortune advances one durable request; constructors never start work.
func (b *Backend) ProcessFortune(ctx context.Context) (bool, error) {
	if b == nil || b.fortunes == nil {
		return false, fortunes.ErrUnavailable
	}
	return b.fortunes.ProcessOne(ctx)
}
