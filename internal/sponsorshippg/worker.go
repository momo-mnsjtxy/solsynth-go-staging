// SPDX-License-Identifier: AGPL-3.0-only
package sponsorshippg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/internal/postpg"
	s "solsynth.local/go-migration/ports/sphere/sponsorship"
	"time"
)

// AuctionWorker exposes only placement maintenance over existing paid bids.
// It needs no bearer, signing key, treasury authority or user mutation grants.
type AuctionWorker struct{ repository *Repository }

func NewAuctionWorker(db *sql.DB, visibility postpg.VisibilityProvider, now func() time.Time) (*AuctionWorker, error) {
	if db == nil || visibility == nil {
		return nil, s.ErrUnavailable
	}
	if now == nil {
		now = time.Now
	}
	return &AuctionWorker{&Repository{db: db, visibility: visibility, cfg: Config{Now: now}}}, nil
}
func (w *AuctionWorker) Ready(ctx context.Context) error {
	if w == nil || w.repository == nil {
		return s.ErrUnavailable
	}
	_, e := w.repository.db.ExecContext(ctx, `SELECT id,post_id,transaction_id,fulfilled_at,expires_at FROM owned_post_sponsorships LIMIT 0;SELECT valid_from,valid_until FROM owned_post_sponsor_placements LIMIT 0;SELECT action FROM owned_post_sponsor_audit LIMIT 0`)
	return e
}
func (w *AuctionWorker) Run(ctx context.Context) (bool, error) {
	if w == nil || w.repository == nil {
		return false, s.ErrUnavailable
	}
	return w.repository.Auction(ctx)
}
