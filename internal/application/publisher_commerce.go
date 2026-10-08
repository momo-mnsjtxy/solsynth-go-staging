// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/commercepg"
	"solsynth.local/go-migration/internal/postawardpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/publisherratingpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/internal/sponsorshippg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/postawards"
	rating "solsynth.local/go-migration/ports/sphere/publisherrating"
	"solsynth.local/go-migration/ports/sphere/sponsorship"
	"time"
)

type publisherCommerceModules struct {
	awards                  *postawardpg.Repository
	awardHTTP               http.Handler
	sponsors                *sponsorshippg.Repository
	auction                 *sponsorshippg.AuctionWorker
	ratings                 *publisherratingpg.Repository
	ratingConfig            *publisherratingpg.Config
	sponsorHTTP, ratingHTTP http.Handler
}

func publisherCommerceFor(db *sql.DB, c Config, g *authoritypg.TransactionGuard, v *authority.Verifier, social *socialpg.Repository, associations postpg.AssociationStore, checkout *commercepg.Repository) (out publisherCommerceModules, err error) {
	var awards postawards.Repository
	var sponsors sponsorship.Repository
	var ratings rating.Repository
	if (c.Sponsorship != nil || c.PublisherRatings != nil || c.PostAwards != nil) && g == nil {
		return out, errors.New("publisher commerce requires canonical shared authority")
	}
	if c.PostAwards != nil {
		cfg := *c.PostAwards
		if c.Authority.Now != nil {
			cfg.Now = c.Authority.Now
		}
		out.awards, err = postawardpg.New(db, g, checkout, social, cfg)
		if err != nil {
			return
		}
		checkout.WithPostAwards(out.awards)
		awards = out.awards
	}
	if c.Sponsorship != nil {
		cfg := *c.Sponsorship
		if c.Authority.Now != nil {
			cfg.Now = c.Authority.Now
		}
		out.sponsors, err = sponsorshippg.New(db, g, checkout, social, associations, cfg)
		if err != nil {
			return
		}
		checkout.WithSponsorship(out.sponsors)
		sponsors = out.sponsors
		out.auction, err = sponsorshippg.NewAuctionWorker(db, social, cfg.Now)
		if err != nil {
			return
		}
	}
	if c.PublisherRatings != nil {
		cfg := *c.PublisherRatings
		if c.Authority.Now != nil {
			cfg.Now = c.Authority.Now
		}
		if cfg.Now == nil {
			cfg.Now = time.Now
		}
		out.ratings, err = publisherratingpg.New(db, social, cfg)
		if err != nil {
			return
		}
		out.ratingConfig = &cfg
		ratings = out.ratings
	}
	auth := func(r *http.Request, required bool) (string, error) {
		return (postIdentity{identity{v}}).Authenticate(r)
	}
	wrap := func(h http.Handler) http.Handler { return bindIdentity(v, bindTransactionAuthority(g, h)) }
	out.awardHTTP = wrap(postawards.NewHandler(awards, auth))
	out.sponsorHTTP = wrap(sponsorship.NewHandler(sponsors, auth))
	out.ratingHTTP = wrap(rating.NewHandler(ratings, auth))
	return
}
func (m publisherCommerceModules) route(path string) http.Handler {
	if postawards.Handles(path) {
		return m.awardHTTP
	}
	if sponsorship.Handles(path) {
		return m.sponsorHTTP
	}
	if rating.Handles(path) {
		return m.ratingHTTP
	}
	return nil
}
func (m publisherCommerceModules) ready(ctx context.Context) error {
	if m.awards != nil {
		if e := m.awards.Ready(ctx); e != nil {
			return e
		}
	}
	if m.sponsors != nil {
		if e := m.sponsors.Ready(ctx); e != nil {
			return e
		}
	}
	if m.auction != nil {
		if e := m.auction.Ready(ctx); e != nil {
			return e
		}
	}
	if m.ratings != nil {
		return m.ratings.Ready(ctx)
	}
	return nil
}

// Independent bounded maintenance needs no new payment/session authority.
func (b *Backend) ProcessPublisherCommerceDue(ctx context.Context, limit int) (int, error) {
	if b == nil || limit < 1 || limit > 1000 {
		return 0, errors.New("invalid publisher commerce maintenance batch")
	}
	m := b.publisherCommerce
	total := 0
	if m.auction != nil {
		created, e := m.auction.Run(ctx)
		if e != nil {
			return 0, e
		}
		if created {
			total++
		}
	}
	if m.ratings == nil || m.ratingConfig == nil || (!m.ratingConfig.ReactionOnly && !m.ratingConfig.PaidAwards) || total >= limit {
		return total, nil
	}
	today := m.ratingConfig.Now().UTC().Truncate(24 * time.Hour)
	day := today.AddDate(0, 0, -31)
	if day.Before(m.ratingConfig.EarliestDay) {
		day = m.ratingConfig.EarliestDay
	}
	for ; day.Before(today) && total < limit; day = day.AddDate(0, 0, 1) {
		n, e := m.ratings.ProcessDay(ctx, day, limit-total)
		total += n
		if e != nil {
			return total, e
		}
	}
	return total, nil
}

// ProcessPostAwardsDue releases only preexisting confirmed award reserves.
func (b *Backend) ProcessPostAwardsDue(ctx context.Context, limit int) (int, error) {
	if b == nil || b.publisherCommerce.awards == nil {
		return 0, postawards.ErrUnavailable
	}
	return b.publisherCommerce.awards.ProcessDue(ctx, limit)
}
