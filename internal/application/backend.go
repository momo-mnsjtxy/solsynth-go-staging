// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/accountsettingspg"
	"solsynth.local/go-migration/internal/activitypg"
	"solsynth.local/go-migration/internal/affiliationpg"
	"solsynth.local/go-migration/internal/apppg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/autocompletepg"
	"solsynth.local/go-migration/internal/blogpg"
	"solsynth.local/go-migration/internal/boardpg"
	"solsynth.local/go-migration/internal/botpg"
	"solsynth.local/go-migration/internal/calendarmediapg"
	"solsynth.local/go-migration/internal/chatmediapg"
	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/internal/commercepg"
	"solsynth.local/go-migration/internal/fortunepg"
	"solsynth.local/go-migration/internal/interactionpg"
	"solsynth.local/go-migration/internal/livestreampg"
	"solsynth.local/go-migration/internal/locationpg"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/internal/merchantpg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/moderationpg"
	"solsynth.local/go-migration/internal/namecardpg"
	"solsynth.local/go-migration/internal/notabledaypg"
	"solsynth.local/go-migration/internal/notificationpg"
	"solsynth.local/go-migration/internal/oauthpg"
	"solsynth.local/go-migration/internal/postawardpg"
	"solsynth.local/go-migration/internal/postembedpg"
	"solsynth.local/go-migration/internal/postmediapg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/presencepg"
	"solsynth.local/go-migration/internal/profilemediapg"
	"solsynth.local/go-migration/internal/progressionpg"
	"solsynth.local/go-migration/internal/publisherratingpg"
	"solsynth.local/go-migration/internal/readerpg"
	"solsynth.local/go-migration/internal/realmboostpg"
	"solsynth.local/go-migration/internal/realmmemberpg"
	"solsynth.local/go-migration/internal/realtime"
	"solsynth.local/go-migration/internal/rewindpg"
	"solsynth.local/go-migration/internal/roommediapg"
	"solsynth.local/go-migration/internal/signinpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/internal/sponsorshippg"
	"solsynth.local/go-migration/internal/stickerpg"
	"solsynth.local/go-migration/internal/surveypg"
	"solsynth.local/go-migration/internal/taxonomypg"
	"solsynth.local/go-migration/internal/ticketmediapg"
	"solsynth.local/go-migration/internal/ticketpg"
	"solsynth.local/go-migration/internal/timelinepg"
	"solsynth.local/go-migration/internal/translationpg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/oauth"
	"solsynth.local/go-migration/ports/authority/signin"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/develop/bots"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/notifications"
	profiles "solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/accountsettings"
	"solsynth.local/go-migration/ports/passport/activities"
	"solsynth.local/go-migration/ports/passport/affiliation"
	"solsynth.local/go-migration/ports/passport/artwork"
	"solsynth.local/go-migration/ports/passport/board"
	"solsynth.local/go-migration/ports/passport/fortunes"
	"solsynth.local/go-migration/ports/passport/ipcheck"
	"solsynth.local/go-migration/ports/passport/notabledays"
	"solsynth.local/go-migration/ports/passport/passkit"
	"solsynth.local/go-migration/ports/passport/presence"
	"solsynth.local/go-migration/ports/passport/progression"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/passport/tickets"
	"solsynth.local/go-migration/ports/sphere/autocomplete"
	"solsynth.local/go-migration/ports/sphere/blogshttp"
	"solsynth.local/go-migration/ports/sphere/interactions"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
	"solsynth.local/go-migration/ports/sphere/locations"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/reader"
	"solsynth.local/go-migration/ports/sphere/stickers"
	"solsynth.local/go-migration/ports/sphere/surveys"
	"solsynth.local/go-migration/ports/sphere/taxonomy"
	"solsynth.local/go-migration/ports/sphere/timeline"
	"solsynth.local/go-migration/ports/sphere/translation"
	"solsynth.local/go-migration/ports/wallet/commerce"
	"solsynth.local/go-migration/ports/wallet/exchangecatalog"
	"solsynth.local/go-migration/ports/wallet/merchants"
)

// Config is injected by the embedding application. Enum values are mandatory
// explicit database-policy decisions; zero is never silently treated as public.
type Config struct {
	ChatSearchPerks map[string]int // Explicit canonical subscription-to-perk policy.
	RealmMembers    *realmmemberpg.Config
	BotExecution    *BotExecutionConfig
	RealmBoosts     *realmboostpg.Config
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
	BadgeIconsPath                string
	MediaStore                    media.ObjectStore
	MaxMediaBytes                 int64
	NotificationApp               string
	CommerceProviders             map[string]commerce.Provider
	CommerceCouponPolicy          commerce.CouponPolicy
	Signin                        *signin.Config
	Tickets                       *ticketpg.Config
	Calls                         *CallConfig
	Federation                    *FederationConfig
}
type Backend struct {
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
	notificationWorker       *notificationpg.Worker
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
	if authorityDB == nil {
		authorityDB = db
	}
	if c.AppleWallet != nil && authorityDB != db {
		return nil, errors.New("Apple Wallet requires canonical same-pool authority")
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
	var readerRepo *readerpg.Repository
	var readerHTTP http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 503, "READER_UNCONFIGURED") })
	if c.Reader != nil {
		if authorityDB != db || guard == nil {
			return nil, errors.New("reader requires canonical same-pool authority")
		}
		cfg := *c.Reader
		if cfg.Now == nil {
			cfg.Now = c.Authority.Now
		}
		readerRepo, err = readerpg.New(db, guard, cfg)
		if err != nil {
			return nil, err
		}
		readerHTTP = bindIdentity(v, bindTransactionAuthority(guard, reader.NewHandler(readerRepo, postIdentity{identity})))
	}
	var translationRepo *translationpg.Repository
	var translationHTTP http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 503, "TRANSLATION_UNCONFIGURED") })
	if c.Translation != nil {
		if authorityDB != db || guard == nil {
			return nil, errors.New("translation requires canonical same-pool authority")
		}
		cfg := *c.Translation
		if cfg.Now == nil {
			cfg.Now = c.Authority.Now
		}
		translationRepo, err = translationpg.New(db, guard, cfg)
		if err != nil {
			return nil, err
		}
		translationHTTP = bindIdentity(v, bindTransactionAuthority(guard, translation.NewHandler(translationRepo, postIdentity{identity})))
	}
	cache := board.NewGroupCache()
	appRepo := apppg.NewWithAuthority(db, guard).WithBoardCache(cache)
	appService := &apps.Service{Repository: appRepo, Authority: proofIdentity{identity}}
	merchantRepo := merchantpg.New(db, guard)
	merchantHTTP := merchants.NewHandler(merchantRepo, postIdentity{identity})
	if !c.CommerceCouponPolicy.Empty() && !c.CommerceCouponPolicy.Valid() {
		return nil, errors.New("invalid explicit coupon policy")
	}
	commerceRepo := commercepg.New(db, guard, commercepg.AppAdapters(appRepo)).WithPaymentAuthorization(paymentPIN).WithCouponPolicy(c.CommerceCouponPolicy)
	appRepo.WithProductCatalog(commerceRepo)
	appService.CustomOrders = commercepg.NewManagedOrders(commerceRepo, appRepo)
	if c.Authority.Now != nil {
		commerceRepo.WithClock(c.Authority.Now)
	}
	commerceHTTP := (&commerce.Service{Store: commerceRepo, Identity: commerceIdentity{identity}, Payments: paymentPIN.Atomic(), Providers: c.CommerceProviders}).Handler()
	appNormal := bindIdentity(v, bindTransactionAuthority(guard, appService.Handler()))
	appHTTP := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if apps.IsAppCredentialRoute(r) {
			appService.AppHandler().ServeHTTP(w, r)
			return
		}
		appNormal.ServeHTTP(w, r)
	})
	bRepo := boardpg.NewWithAuthority(db, guard, nil)
	bh := board.NewHTTPHandler(board.HTTPOptions{Service: board.Service{Repository: bRepo, Catalog: appRepo, Normalizer: board.DocumentedEnvelope{}, Cache: cache}, Authority: boardIdentity{identity}, Accounts: accountResolver{aStore}, AccountCache: cache, Delete: bRepo})
	mr, err := messagerpg.NewWithIndependentPolicy(db, *c.MessagerPolicy)
	if err != nil {
		return nil, err
	}
	cr, err := chatpg.NewWithAuthority(db, rooms.Policy{NoneEncryptionMode: c.MessagerPolicy.NoneEncryptionMode, NotifyAll: c.MessagerPolicy.NotifyAll, NotifyMentions: c.MessagerPolicy.NotifyMentions, NotifyNone: c.MessagerPolicy.NotifyNone}, guard)
	if err != nil {
		return nil, err
	}
	if c.EnableMLSDelivery {
		if c.MessagerPolicy.NoneEncryptionMode == rooms.MLSEncryptionMode {
			return nil, errors.New("plaintext mapping must differ from authored MLS mode 3")
		}
		cr.WithMLSDelivery()
	}
	chatMedia := chatmediapg.New(db, cr, c.MessagerPolicy.NoneEncryptionMode)
	cr.WithAssociations(chatMedia)
	roomMedia := roommediapg.New(db, cr, c.MessagerPolicy.NoneEncryptionMode)
	cr.WithRoomMedia(roomMedia)
	mr.WithMessageDecorator(chatMedia)
	var chatSearch rooms.SearchEntitlement
	if c.ChatSearchPerks != nil {
		chatSearch, err = commerceRepo.ChatSearchPerks(c.ChatSearchPerks)
		if err != nil {
			return nil, err
		}
	}
	chatOrganization := chatpg.NewOrganization(cr, mr, chatSearch)
	mh := (&rooms.Service{Repository: cr, Organization: chatOrganization, Authority: proofIdentity{identity}, Reader: mr}).Handler()
	th := taxonomy.NewAuthorityHandler(taxonomypg.NewWithAuthority(db, *c.PublicVisibility, guard), taxonomyIdentity{identity, publisherRepository{accountRepo}}, time.Now)
	presenceRepo := presencepg.NewWithAuthority(db, guard)
	var fortuneRepo *fortunepg.Repository
	var fortuneHTTP http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 503, "FORTUNE_UNCONFIGURED") })
	if c.Fortunes != nil {
		fortuneRepo, err = fortunepg.New(db, guard, *c.Fortunes)
		if err != nil {
			return nil, err
		}
		fortuneHTTP = (&fortunes.Service{Repository: fortuneRepo, Authority: v, AllowAnonymousCatalogue: c.Fortunes.AllowAnonymousCatalogue}).Handler()
	}
	progressionRepo := progressionpg.NewWithAuthority(db, guard)
	if fortuneRepo != nil {
		progressionRepo = progressionpg.NewWithCheckInReports(db, guard, fortuneRepo)
	}
	var bindProof func(context.Context, authority.Principal) context.Context
	if guard != nil {
		bindProof = guard.Bind
	}
	activityRepo := activitypg.New(db, guard)
	activityHTTP := (&activities.Service{Repository: activityRepo, Authority: v, Bind: bindProof}).Handler()
	presenceHTTP := (&presence.Service{Repository: presenceRepo, Authority: v, Bind: bindProof}).Handler()
	progressionHTTP := (&progression.Service{Repository: progressionRepo, Authority: v, Bind: bindProof}).Handler()
	var ticketRepo *ticketpg.Repository
	var ticketHTTP http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 503, "TICKETS_AUTHORITY_UNAVAILABLE") })
	if guard != nil {
		ticketConfig := ticketpg.Config{}
		if c.Tickets != nil {
			ticketConfig = *c.Tickets
		}
		if ticketConfig.Rewards == nil {
			ticketConfig.Rewards = progressionRepo
		}
		ticketRepo, err = ticketpg.New(db, guard, ticketConfig)
		if err != nil {
			return nil, err
		}
		ticketHTTP = bindIdentity(v, (&tickets.Service{Repository: ticketRepo, Authority: proofIdentity{identity}}).Handler())
	}
	var affiliationRepo *affiliationpg.Repository
	var affiliationHTTP http.Handler = (&affiliation.Service{}).Handler()
	if guard != nil {
		affiliationRepo, err = affiliationpg.New(db, guard, affiliationpg.Config{Now: c.Authority.Now, Activation: ticketRepo})
		if err != nil {
			return nil, err
		}
		ticketRepo.WithInviteAdmission(affiliationRepo)
		signinRepo.WithAffiliation(affiliationRepo).WithActivation(ticketRepo)
		affiliationHTTP = (&affiliation.Service{Repository: affiliationRepo, Authority: v}).Handler()
	}
	locationRepo := locationpg.NewWithAuthority(db, guard)
	locationHTTP := locations.NewHandler(locationRepo, postIdentity{identity})
	socialRepo := socialpg.NewWithAuthority(db, guard)
	cr.WithSocial(socialRepo)
	mr.WithRoomAccess(cr)
	if external.calls != nil {
		external.calls.WithRoomAccess(cr)
	}
	accountRepo.WithRealms(socialRepo)
	socialHTTP := (&social.Service{Repository: socialRepo, Authority: proofIdentity{identity}}).Handler()
	interactionRepo := interactionpg.NewWithAuthority(db, guard).WithVisibility(socialRepo)
	interactionHTTP := interactions.NewHandler(interactionRepo, postIdentity{identity})
	postRepository := postpg.NewWithAuthority(db, guard)
	if guard == nil {
		postRepository = postpg.New(db)
	}
	postMedia := postmediapg.New(db, interactionRepo).WithVisibility(socialRepo)
	postComposition := postembedpg.New(db, postMedia, socialRepo)
	timelineRepo := timelinepg.NewWithAuthority(db, guard).WithVisibility(socialRepo).WithAssociations(postComposition)
	timelineHTTP := timeline.NewHandler(timelineRepo, postIdentity{identity})
	postRepository.WithAssociations(timelineRepo).WithVisibility(socialRepo).WithAutomod(c.PostAutomodReview)
	timelineRepo.WithAutomod(c.PostAutomodReview)
	var scheduledPosts *postpg.Repository
	if guard != nil {
		scheduledPosts = postRepository.WithScheduling()
	}
	var blogRepo *blogpg.Repository
	var blogHTTP http.Handler = blogshttp.NewHandler(nil, nil)
	if c.Blogs != nil {
		if guard == nil || authorityDB != db {
			return nil, errors.New("blogs require canonical same-pool authority")
		}
		cfg := *c.Blogs
		if cfg.Now == nil {
			cfg.Now = c.Authority.Now
		}
		if cfg.Previewer == nil && readerRepo != nil {
			cfg.Previewer = readerRepo
		}
		blogRepo, err = blogpg.New(db, guard, cfg)
		if err != nil {
			return nil, err
		}
		postRepository.WithBlogs(blogRepo)
		blogHTTP = bindIdentity(v, bindTransactionAuthority(guard, blogshttp.NewHandler(blogRepo, postIdentity{identity})))
	}
	ph := posts.NewHandler(postRepository, postIdentity{identity})
	wh, wallet := walletHandler(db, identity, paymentPIN, guard, socialRepo)
	wallet.WithClock(c.Authority.Now)
	var signinHTTP http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 503, "SIGNIN_ISSUER_UNCONFIGURED") })
	var signinService *signin.Service
	if c.Signin != nil {
		if authorityDB != db {
			return nil, errors.New("sign-in requires same-pool canonical authority")
		}
		if c.SessionService == nil {
			c.SessionService, err = authority.New(aStore, c.Authority)
			if err != nil {
				return nil, err
			}
		}
		signinService, err = signin.New(signinRepo, c.SessionService, v, *c.Signin)
		if err != nil {
			return nil, err
		}
		signinHTTP = signinService.Handler()
	}
	var oauthRepo *oauthpg.Repository
	var oauthHTTP http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 503, "OAUTH_UNCONFIGURED") })
	if c.OAuthEnabled {
		if authorityDB != db || guard == nil {
			return nil, errors.New("OAuth requires canonical same-pool authority")
		}
		if c.SessionService == nil {
			c.SessionService, err = authority.New(aStore, c.Authority)
			if err != nil {
				return nil, err
			}
		}
		oauthRepo, err = oauthpg.NewWithAuthority(db, aStore, c.SessionService, guard, appRepo)
		if err != nil {
			return nil, err
		}
		oauthHTTP = (&oauth.Service{Repository: oauthRepo, Verifier: v, Metadata: c.SessionService, Bind: guard.Bind, AuthorizationEndpoint: c.OAuthConsentURL}).Handler()
	}
	session := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 501, "AUTHORITY_ISSUER_ROLE_REQUIRED") })
	var sessionHTTP http.Handler = session
	if c.SessionService != nil {
		sessionHTTP = c.SessionService.Handler()
		if signinService != nil {
			mux := http.NewServeMux()
			signinService.RegisterRoutes(mux)
			mux.Handle("/", sessionHTTP)
			sessionHTTP = mux
		}
	}
	var mediaRepo *mediapg.Repository
	var calendarMedia *calendarmediapg.Repository
	var artworkHTTP http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 503, "MEDIA_STORAGE_UNCONFIGURED") })
	var mediaHTTP http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 503, "MEDIA_STORAGE_UNCONFIGURED") })
	if c.MediaStore != nil {
		if authorityDB != db || c.MaxMediaBytes <= 0 {
			return nil, errors.New("media requires same-pool authority and explicit positive size limit")
		}
		mediaRepo, err = mediapg.New(db, c.MediaStore, guard, mediapg.Config{MaxFileBytes: c.MaxMediaBytes, PendingTTL: time.Hour})
		if err != nil {
			return nil, err
		}
		ticketMedia := ticketmediapg.New(db, ticketRepo)
		ticketRepo.WithMedia(ticketMedia)
		calendarMedia = calendarmediapg.New(db)
		profileMedia := profilemediapg.New(db, mediapg.AnyReadVisibility{postMedia, chatMedia, roomMedia, ticketMedia, calendarMedia})
		accountRepo.WithProfileMedia(profileMedia)
		mediaRepo.WithReadVisibility(profileMedia)
		artworkHTTP = (&artwork.Service{Repository: mediaRepo, Authority: proofIdentity{identity}}).Handler()
		mediaHTTP = (&media.Service{Repository: mediaRepo, Authority: proofIdentity{identity}, MaxFileBytes: c.MaxMediaBytes}).Handler()
	}
	passkitModule, err := passkitFor(db, c, v, guard)
	if err != nil {
		return nil, err
	}
	var calendarRepo *notabledaypg.Repository
	var calendarHTTP http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 503, "CALENDAR_AUTHORITY_UNAVAILABLE") })
	if guard != nil {
		var activity notabledaypg.ActivityOverlay = notabledaypg.CanonicalActivityOverlay{}
		if fortuneRepo != nil {
			activity = fortunepg.CalendarOverlay{Base: activity, Reports: fortuneRepo}
		}
		calendarRepo, err = notabledaypg.New(db, guard, notabledaypg.Config{Now: c.Authority.Now, Activity: activity})
		if err != nil {
			return nil, err
		}
		if calendarMedia != nil {
			calendarRepo.WithMedia(calendarMedia)
		}
		calendarHTTP = bindIdentity(v, (&notabledays.Service{Repository: calendarRepo, Authority: proofIdentity{identity}}).Handler())
	}
	var botRepo *botpg.Repository
	var botHTTP http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 503, "BOT_AUTHORITY_UNAVAILABLE") })
	if c.BotExecution != nil && guard == nil {
		return nil, bots.ErrUnavailable
	}
	if guard != nil {
		botRepo = botpg.NewWithAuthority(db, guard)
		if mediaRepo != nil {
			botRepo.WithMedia(mediaRepo)
		}
		if c.BotExecution != nil {
			if cr == nil || c.MessagerPolicy == nil || c.BotExecution.Runtime.NoneEncryptionMode != int(c.MessagerPolicy.NoneEncryptionMode) {
				return nil, bots.ErrInvalid
			}
			if err = botRepo.WithExecution(c.BotExecution.Runtime, c.BotExecution.Adapters...); err != nil {
				return nil, err
			}
			cr.WithTextMessageObserver(botRepo)
		}
		botHTTP = (&bots.Service{Repository: botRepo, Authority: v, Bind: guard.Bind}).Handler()
	}
	surveysRepo := surveypg.NewWithAuthority(db, guard).WithVisibility(socialRepo)
	stickersRepo := stickerpg.NewWithAuthority(db, guard).WithVisibility(socialRepo)
	if mediaRepo != nil {
		surveysRepo.WithMedia(mediaRepo)
		stickersRepo.WithMedia(mediaRepo)
	}
	var autocompleteRepo *autocompletepg.Repository
	var autocompleteHTTP http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 503, "AUTOCOMPLETE_AUTHORITY_UNAVAILABLE") })
	if guard != nil {
		autocompleteRepo, err = autocompletepg.New(db, guard, autocompletepg.Config{Stickers: stickersRepo, Rooms: cr})
		if err != nil {
			return nil, err
		}
		autocompleteHTTP = bindIdentity(v, bindTransactionAuthority(guard, autocomplete.NewHandler(autocompleteRepo, postIdentity{identity})))
	}
	surveysHTTP := surveys.NewHandler(surveysRepo, postIdentity{identity})
	stickersHTTP := stickers.NewHandler(stickersRepo, postIdentity{identity})
	if c.NotificationApp == "" {
		c.NotificationApp = "dev.solsynth.solian"
	}
	surveysRepo.WithNotificationApp(c.NotificationApp)
	var settingsRepo *accountsettingspg.Repository
	var settingsHTTP http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 503, "SETTINGS_AUTHORITY_UNAVAILABLE") })
	if guard != nil {
		settingsRepo, err = accountsettingspg.New(db, guard, c.NotificationApp)
		if err != nil {
			return nil, err
		}
		settingsHTTP = (&accountsettings.Service{Repository: settingsRepo, Authority: v}).Handler()
	}
	notificationRepo := notificationpg.New(db, guard, c.NotificationApp).WithRoomAccess(cr)
	timelineRepo.WithSubscriptionNotifications(publisherSubscriptionNotifications{notificationRepo, db, c.NotificationApp})
	if settingsRepo != nil {
		notificationRepo.WithPreferencePolicy(settingsRepo)
	}
	notificationIdentity := notificationIdentity{identity}
	notificationHTTP := (notifications.HTTP{Repository: notificationRepo, Identity: notificationIdentity, App: c.NotificationApp}).Handler()
	realtimeHTTP, err := realtime.New(notificationRepo, notificationIdentity, realtime.Config{App: c.NotificationApp})
	if err != nil {
		return nil, err
	}
	surveyNotifications := func(ctx context.Context, limit int) (int, error) {
		return surveysRepo.ProcessNotifications(ctx, notificationRepo, c.NotificationApp, limit)
	}
	notificationWorker := &notificationpg.Worker{LocalProjections: []func(context.Context, int) (int, error){surveyNotifications}, Repository: notificationRepo, Wallet: true, Chat: true, ChatNoneEncryptionMode: int(c.MessagerPolicy.NoneEncryptionMode)}
	catalogs, err := publicCatalogs(c)
	if err != nil {
		return nil, err
	}
	ipHTTP, err := ipCheckHandler(c, v)
	if err != nil {
		return nil, err
	}
	extras, err := accountExtrasFor(db, c, v, guard, commerceRepo, accountRepo, socialRepo, cr)
	if err != nil {
		return nil, err
	}
	var moderationRepo *moderationpg.Repository
	var moderationHTTP http.Handler = content.NewHandler(nil, nil)
	if guard != nil {
		moderationRepo, err = moderationpg.New(db, guard, moderationpg.Config{Now: c.Authority.Now, PostAssociations: timelineRepo, AutomodReview: c.PostAutomodReview})
		if err != nil {
			return nil, err
		}
		moderationHTTP = bindIdentity(v, bindTransactionAuthority(guard, content.NewHandler(moderationRepo, postIdentity{identity})))
	}
	streamHTTP, err := external.configureStreams(db, c, v, guard, accountRepo, socialRepo, mediaRepo, commerceRepo)
	if err != nil {
		return nil, err
	}
	if external.streams != nil {
		timelineRepo.WithSubscriptionStreams(external.streams.SubscriptionReader())
	} else {
		var subscriptionMedia live.MediaProvider
		if mediaRepo != nil {
			subscriptionMedia = mediaRepo
		}
		timelineRepo.WithSubscriptionStreams(livestreampg.NewSubscriptionReader(accountRepo, socialRepo, subscriptionMedia))
	}
	admin, err := adminModulesFor(db, guard, v, c.Authority.Now)
	if err != nil {
		return nil, err
	}
	publisherCommerce, err := publisherCommerceFor(db, c, guard, v, socialRepo, timelineRepo, commerceRepo)
	if err != nil {
		return nil, err
	}
	realmBoosts, err := realmBoostsFor(db, guard, v, commerceRepo, socialRepo, moderationRepo, c.RealmBoosts)
	if err != nil {
		return nil, err
	}
	realmMembers, err := realmMembersFor(db, guard, v, realmBoosts.repo, socialRepo, c.RealmMembers)
	if err != nil {
		return nil, err
	}
	h := router{modules{realmMemberHistory: realmMembers.repo != nil, realmMembers: realmMembers.handler, blogs: blogHTTP, realmBoosts: realmBoosts.handler, affiliations: affiliationHTTP, passkit: passkitModule, artwork: bindIdentity(v, bindTransactionAuthority(guard, artworkHTTP)), publisherCommerce: publisherCommerce, domainCollections: domainCollections, admin: admin, livestreams: streamHTTP, moderation: moderationHTTP, namecards: extras.cardsHTTP, rewind: extras.rewindHTTP, ipcheck: ipHTTP, accountSettings: settingsHTTP, translation: translationHTTP, autocomplete: autocompleteHTTP, reader: readerHTTP, oauth: oauthHTTP, fortunes: fortuneHTTP, calendar: calendarHTTP, bots: botHTTP, tickets: ticketHTTP, merchants: bindIdentity(v, bindTransactionAuthority(guard, merchantHTTP)), calls: external.callHTTP, federationPublic: external.federationPublic, federationPrivate: external.federationPrivate, presence: presenceHTTP, activities: activityHTTP, progression: progressionHTTP, locations: bindIdentity(v, bindTransactionAuthority(guard, locationHTTP)), surveys: bindIdentity(v, bindTransactionAuthority(guard, surveysHTTP)), stickers: bindIdentity(v, bindTransactionAuthority(guard, stickersHTTP)), timeline: bindIdentity(v, bindTransactionAuthority(guard, timelineHTTP)), signin: signinHTTP, commerce: bindIdentity(v, bindTransactionAuthority(guard, commerceHTTP)), apps: appHTTP, notifications: bindIdentity(v, bindTransactionAuthority(guard, notificationHTTP)), realtime: newStreamTickets(v, bindIdentity(v, bindTransactionAuthority(guard, realtimeHTTP))), media: bindIdentity(v, bindTransactionAuthority(guard, mediaHTTP)), social: bindIdentity(v, bindTransactionAuthority(guard, socialHTTP)), interactions: bindIdentity(v, bindTransactionAuthority(guard, interactionHTTP)), public: bindIdentity(v, catalogs), board: bindIdentity(v, bindTransactionAuthority(guard, bh)), taxonomy: bindIdentity(v, bindTransactionAuthority(guard, th)), posts: bindIdentity(v, bindTransactionAuthority(guard, ph)), messager: bindIdentity(v, bindTransactionAuthority(guard, mh)), wallet: wh, accounts: accountHTTP, session: sessionHTTP}}
	return &Backend{surveyNotifications: surveyNotifications, activities: activityRepo, chatOrganization: chatOrganization, postComposition: postComposition, realmMembers: realmMembers, blogs: blogRepo, realmBoosts: realmBoosts, scheduledPosts: scheduledPosts, affiliations: affiliationRepo, social: socialRepo, passkit: passkitModule, publisherCommerce: publisherCommerce, domainCollections: domainCollections, admin: admin, moderation: moderationRepo, moderationSink: notificationRepo, moderationApp: c.NotificationApp, accountExtras: extras, accountSettings: settingsRepo, translation: translationRepo, autocomplete: autocompleteRepo, chat: cr, reader: readerRepo, oauth: oauthRepo, fortunes: fortuneRepo, external: external, handler: h, verifier: v, wallet: wallet, media: mediaRepo, notificationWorker: notificationWorker, commerce: commerceRepo, locations: locationRepo, progression: progressionRepo, tickets: ticketRepo, bots: botRepo, calendar: calendarRepo, progressionNotifications: progressionNotifications{notificationRepo, c.NotificationApp}, signinEnabled: signinService != nil, db: db, authorityDB: authorityDB}, nil

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
	if b.blogs != nil && b.blogs.VerificationEnabled() {
		m, x := b.blogs.ProcessPending(ctx, min(limit, 100))
		n += m
		e = errors.Join(e, x)
	}
	if b.bots != nil && b.bots.ExecutionEnabled() {
		m, x := b.bots.ProcessMessagesDue(ctx, min(limit, 100))
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
	l, le := b.locations.Expire(ctx, limit)
	p, pe := b.progression.ProcessDue(ctx, limit)
	d, de := b.progression.DispatchCompletions(ctx, limit, b.progressionNotifications)
	if b.tickets != nil {
		rewards, re := b.tickets.RetryPendingRewards(ctx, limit)
		n += rewards
		e = errors.Join(e, re)
	}
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
