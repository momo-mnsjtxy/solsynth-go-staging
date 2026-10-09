// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/callpg"
	"solsynth.local/go-migration/internal/federationhttp"
	"solsynth.local/go-migration/internal/federationpg"
	"solsynth.local/go-migration/internal/livestreampg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/federation"
	"solsynth.local/go-migration/ports/messager/calls"
	"time"
)

// CallConfig is opt-in. Provider credentials and revocation capabilities must be
// provisioned by the host; construction does not create a provider or a call.
type CallConfig struct {
	Provider          calls.Provider
	TokenTTL, CallTTL time.Duration
}

// FederationConfig keeps key provisioning and outbound networking explicit.
// A nil Transport allows local opt-in/inbox/public reads but leaves delivery pending.
type FederationConfig struct {
	Policy    federation.Config
	Keys      federation.KeyProvider
	Transport federation.Transport
	Discovery federation.DiscoveryFeatures
	Fetcher   federation.Fetcher
}
type externalModules struct {
	streams                                       *livestreampg.Repository
	awardMaintenance                              *livestreampg.AwardMaintenance
	calls                                         *callpg.Repository
	federation                                    *federationpg.Repository
	transport                                     federation.Transport
	fetcher                                       federation.Fetcher
	discoveryEnabled                              bool
	callHTTP, federationPublic, federationPrivate http.Handler
}

func externalModulesFor(db, authorityDB *sql.DB, c Config, v *authority.Verifier, guard *authoritypg.TransactionGuard) (x externalModules, err error) {
	x.callHTTP = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 503, "CHAT_CALL_PROVIDER_UNCONFIGURED") })
	x.federationPublic = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 503, "FEDERATION_UNCONFIGURED") })
	x.federationPrivate = x.federationPublic
	if c.Calls != nil || c.Federation != nil || c.Livestreams != nil || c.LivestreamAwardMaintenance != nil {
		if authorityDB != db || guard == nil {
			return x, errors.New("calls and federation require same-pool atomic authority")
		}
	}
	identity := identity{v}
	if c.Calls != nil {
		if c.Calls.Provider == nil {
			return x, errors.New("calls require an explicit provider")
		}
		app := c.NotificationApp
		if app == "" {
			app = "dev.solsynth.solian"
		}
		x.calls, err = callpg.New(db, guard, c.Calls.Provider, callpg.Config{NoneEncryptionMode: c.MessagerPolicy.NoneEncryptionMode, TokenTTL: c.Calls.TokenTTL, CallTTL: c.Calls.CallTTL, NotificationApp: app})
		if err != nil {
			return x, err
		}
		x.callHTTP = bindIdentity(v, bindTransactionAuthority(guard, (&calls.Service{Repository: x.calls, Authority: proofIdentity{identity}}).Handler()))
	}
	if c.Federation != nil {
		if c.Federation.Keys == nil {
			return x, errors.New("federation requires explicitly provisioned keys")
		}
		x.federation, err = federationpg.NewWithDiscovery(db, c.Federation.Policy, c.Federation.Keys, guard, c.Federation.Discovery)
		if err != nil {
			return x, err
		}
		x.transport = c.Federation.Transport
		x.fetcher = c.Federation.Fetcher
		x.discoveryEnabled = c.Federation.Discovery.Server != nil || len(c.Federation.Discovery.Targets) > 0
		if x.fetcher != nil && len(c.Federation.Discovery.Targets) == 0 {
			return x, errors.New("discovery transport requires configured targets")
		}
		handler := federationhttp.Handler{Repository: x.federation, Config: c.Federation.Policy, Keys: c.Federation.Keys, Authority: proofIdentity{identity}}
		x.federationPublic = handler
		x.federationPrivate = bindIdentity(v, bindTransactionAuthority(guard, handler))
	}
	return x, nil
}
func (x externalModules) ready(ctx context.Context, db *sql.DB) error {
	if x.awardMaintenance != nil {
		if e := x.awardMaintenance.Ready(ctx); e != nil {
			return e
		}
	}
	if x.streams != nil {
		if e := x.streams.Ready(ctx); e != nil {
			return e
		}
	}
	if x.calls != nil {
		var ready bool
		e := db.QueryRowContext(ctx, `SELECT bool_and(to_regclass(name)IS NOT NULL)FROM(VALUES('call_v1.calls'),('call_v1.members'),('call_v1.events'),('call_v1.timeouts'),('call_v1.invites'))required(name)`).Scan(&ready)
		if e != nil || !ready {
			return errors.New("call schema unavailable")
		}
	}
	if x.federation != nil {
		if x.discoveryEnabled {
			if e := x.federation.DiscoveryReady(ctx); e != nil {
				return errors.New("federation discovery schema or server anchor unavailable")
			}
		}
		if e := x.federation.Ready(ctx); e != nil {
			return errors.New("federation schema unavailable")
		}
	}
	return nil
}
func (x externalModules) processDue(ctx context.Context, limit int) (count int, err error) {
	if limit < 1 || limit > 100 {
		return 0, errors.New("lifecycle batch size must be between 1 and 100")
	}
	if x.awardMaintenance != nil {
		n, e := x.awardMaintenance.ProcessDue(ctx, limit)
		count += n
		err = errors.Join(err, e)
	}
	if x.streams != nil {
		n, e := x.streams.ProcessDue(ctx, limit)
		count += n
		err = errors.Join(err, e)
	}
	if x.calls != nil {
		n, e := x.calls.ProcessDue(ctx, limit)
		count += n
		err = errors.Join(err, e)
	}
	if x.federation != nil {
		if x.fetcher != nil {
			n, e := x.federation.ProcessDiscoveries(ctx, x.fetcher, limit)
			count += n
			err = errors.Join(err, e)
		}
		n, e := x.federation.ReconcilePosts(ctx, limit)
		count += n
		err = errors.Join(err, e)
		if x.transport != nil {
			n, e = x.federation.ProcessDeliveries(ctx, x.transport, limit)
			count += n
			err = errors.Join(err, e)
		}
	}
	return
}

// ProcessExternalDue exposes the faster optional-provider maintenance schedule
// independently of wallet settlement. ProcessDue also includes these modules for
// embedders that schedule one combined worker; duplicate runs are idempotent.
func (b *Backend) ProcessExternalDue(ctx context.Context, limit int) (int, error) {
	if b == nil {
		return 0, errors.New("backend unavailable")
	}
	return b.external.processDue(ctx, limit)
}
