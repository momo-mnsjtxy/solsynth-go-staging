// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/boardpg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/taxonomypg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager"
	profiles "solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/board"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/taxonomy"
)

// Config is injected by the embedding application. Enum values are mandatory
// explicit database-policy decisions; zero is never silently treated as public.
type Config struct {
	Authority        authority.Config
	PublicVisibility *int32
	MessagerPolicy   *messagerpg.IndependentPolicy
	PINPeppers       map[string][]byte
	SessionService   *authority.Service
}
type Backend struct {
	handler         http.Handler
	verifier        *authority.Verifier
	db, authorityDB *sql.DB
	wallet          *walletpg.Repository
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
	if db == nil || c.PublicVisibility == nil || c.MessagerPolicy == nil {
		return nil, errors.New("backend: database and explicit projection policies required")
	}
	if authorityDB == nil {
		authorityDB = db
	}
	aStore := authoritypg.New(authorityDB)
	v, err := authority.NewVerifier(aStore, c.Authority)
	if err != nil {
		return nil, err
	}
	identity := identity{v}
	accountRepo := accountpg.New(db)
	accountHTTP := bindIdentity(v, (&profiles.Service{Repository: accountRepo, Authority: proofIdentity{identity}}).Handler())
	cache := board.NewGroupCache()
	bRepo := boardpg.NewWithIndependentPolicy(db, nil)
	bh := board.NewHTTPHandler(board.HTTPOptions{Service: board.Service{Repository: bRepo, Normalizer: board.DocumentedEnvelope{}, Cache: cache}, Authority: boardIdentity{identity}, Accounts: accountResolver{aStore}, AccountCache: cache, Delete: bRepo})
	mr, err := messagerpg.NewWithIndependentPolicy(db, *c.MessagerPolicy)
	if err != nil {
		return nil, err
	}
	mh := messager.NewAuthoritySyncHandler(mr, syncIdentity{identity})
	th := taxonomy.NewAuthorityHandler(taxonomypg.NewWithIndependentPolicy(db, *c.PublicVisibility), taxonomyIdentity{identity, publisherRepository{accountRepo}}, time.Now)
	ph := posts.NewHandler(postpg.New(db), postIdentity{identity})
	wh, wallet := walletHandler(db, identity, c.PINPeppers)
	session := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 501, "AUTHORITY_ISSUER_ROLE_REQUIRED") })
	var sessionHTTP http.Handler = session
	if c.SessionService != nil {
		sessionHTTP = c.SessionService.Handler()
	}
	h := router{modules{board: bindIdentity(v, bh), taxonomy: bindIdentity(v, th), posts: bindIdentity(v, ph), messager: bindIdentity(v, mh), wallet: wh, accounts: accountHTTP, session: sessionHTTP}}
	return &Backend{handler: h, verifier: v, wallet: wallet, db: db, authorityDB: authorityDB}, nil
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

// ProcessDue applies bounded, durable wallet expiry/settlement transitions. A
// scheduler is owned by the executable, never started by construction.
func (b *Backend) ProcessDue(ctx context.Context, limit int) (int, error) {
	if b == nil || b.wallet == nil {
		return 0, errors.New("backend: wallet unavailable")
	}
	return b.wallet.ProcessDue(ctx, limit)
}
