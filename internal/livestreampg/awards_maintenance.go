// SPDX-License-Identifier: AGPL-3.0-only
package livestreampg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
	"strings"
)

// AwardMaintenance deliberately does not implement livestreams.Repository or
// AwardRepository. Only already-recorded monetary obligations are reachable.
type AwardMaintenance struct{ repository *Repository }
type AwardMaintenanceConfig struct{ PointsCurrency, TreasuryAccountID, TreasuryWalletID string }

func validAwardBinding(c AwardMaintenanceConfig) bool {
	return authority.ValidID(c.TreasuryAccountID) && authority.ValidID(c.TreasuryWalletID) && live.Text(c.PointsCurrency, 128) && strings.TrimSpace(c.PointsCurrency) != "" && strings.TrimSpace(c.PointsCurrency) == c.PointsCurrency
}
func NewAwardMaintenance(db *sql.DB, store AwardCommerce, c AwardMaintenanceConfig) (*AwardMaintenance, error) {
	if db == nil || store == nil || !validAwardBinding(c) {
		return nil, live.ErrInvalid
	}
	// These jobs validate immutable original payment/hold/receipt lineage and
	// current canonical account/wallet rows in their own transaction. They need no
	// bearer, signing key, provider, user Principal or mutation permission grant.
	r := &Repository{db: db, awardCommerce: store, awards: &AwardConfig{PointsCurrency: c.PointsCurrency, TreasuryAccountID: c.TreasuryAccountID, TreasuryWalletID: c.TreasuryWalletID}}
	return &AwardMaintenance{r}, nil
}
func (m *AwardMaintenance) Ready(ctx context.Context) error {
	if m == nil || m.repository == nil {
		return live.ErrUnavailable
	}
	return m.repository.AwardsReady(ctx)
}
func (m *AwardMaintenance) ProcessDue(ctx context.Context, limit int) (int, error) {
	if m == nil || m.repository == nil {
		return 0, live.ErrUnavailable
	}
	return m.repository.ProcessAwardsDue(ctx, limit)
}
