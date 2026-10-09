// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"errors"
	"solsynth.local/go-migration/internal/accountpg"
	profiles "solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/taxonomy"
)

type publisherRepository struct{ repo *accountpg.Repository }

func publisherError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, profiles.ErrForbidden), errors.Is(err, profiles.ErrNotFound):
		return taxonomy.ErrForbidden
	case errors.Is(err, profiles.ErrAmbiguous):
		return taxonomy.Problem("PUBLISHER_SELECTION_REQUIRED", "Select a publisher or configure a default.", 409)
	default:
		return taxonomy.ErrPolicyUnverified
	}
}
func (p publisherRepository) Resolve(ctx context.Context, actor, selector string) (string, error) {
	id, err := p.repo.Resolve(ctx, actor, selector)
	return id, publisherError(err)
}
func (p publisherRepository) RequireManager(ctx context.Context, actor, id string) error {
	return publisherError(p.repo.RequireManager(ctx, actor, id))
}
func (p publisherRepository) Entitlement(ctx context.Context, id string) (taxonomy.Entitlement, error) {
	e, err := p.repo.PublisherEntitlement(ctx, id)
	if err != nil {
		return taxonomy.Entitlement{}, publisherError(err)
	}
	return taxonomy.Entitlement{Total: e.Total, Level: e.Level, PerkLevel: e.PerkLevel}, nil
}
