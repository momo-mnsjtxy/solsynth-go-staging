// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"database/sql"
	"net/http"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/internal/commercepg"
	"solsynth.local/go-migration/internal/namecardpg"
	"solsynth.local/go-migration/internal/rewindpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/namecards"
	"solsynth.local/go-migration/ports/passport/rewind"
)

type accountExtraModules struct {
	cards                 *namecardpg.Repository
	rewinds               *rewindpg.Repository
	cardsHTTP, rewindHTTP http.Handler
}

func accountExtrasFor(db *sql.DB, c Config, v *authority.Verifier, g *authoritypg.TransactionGuard, commerce *commercepg.Repository, accounts *accountpg.Repository, social *socialpg.Repository, chat *chatpg.Repository) (out accountExtraModules, err error) {
	out.cardsHTTP = (&namecards.Service{}).Handler()
	out.rewindHTTP = (&rewind.Service{}).Handler()
	if g == nil {
		return out, nil
	}
	nc := namecardpg.Options{Now: c.Authority.Now}
	if c.NameCards != nil {
		nc = *c.NameCards
		if nc.Now == nil {
			nc.Now = c.Authority.Now
		}
	}
	out.cards, err = namecardpg.New(db, g, commerce, accounts, social, nc)
	if err != nil {
		return
	}
	commerce.WithNameCards(out.cards)
	out.cardsHTTP = (&namecards.Service{Repository: out.cards, Authority: v}).Handler()
	rc := rewindpg.Options{Now: c.Authority.Now}
	if c.Rewind != nil {
		rc = *c.Rewind
		if rc.Now == nil {
			rc.Now = c.Authority.Now
		}
	}
	out.rewinds, err = rewindpg.New(db, g, accounts, chat, social, rc)
	if err != nil {
		return
	}
	out.rewindHTTP = (&rewind.Service{Repository: out.rewinds, Authority: v}).Handler()
	return
}
func (a accountExtraModules) ready(ctx context.Context) error {
	if a.cards != nil {
		if e := a.cards.Ready(ctx); e != nil {
			return e
		}
	}
	if a.rewinds != nil {
		return a.rewinds.Ready(ctx)
	}
	return nil
}
