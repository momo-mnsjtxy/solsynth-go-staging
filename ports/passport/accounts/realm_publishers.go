// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"context"
	"errors"
	"net/http"

	"solsynth.local/go-migration/ports/authority"
)

// These additive capabilities keep standalone individual-only repositories
// compatible. A realm-enabled repository must implement actor-scoped reads.
type OrganizationPublisherRepository interface {
	CreateOrganizationPublisher(context.Context, string, string, PublisherPatch) (Publisher, error)
}
type ScopedPublisherReader interface {
	LookupPublisherForViewer(context.Context, string, string) (Publisher, error)
	ListPublishersForViewer(context.Context, string, string) ([]Publisher, error)
	ListMembersForViewer(context.Context, string, string, int, int) ([]Member, int64, error)
}

func (s *Service) publisherViewer(r *http.Request) (string, error) {
	present := len(r.Header.Values("Authorization")) != 0
	if bound, ok := s.Authority.(interface{ RequestIdentityPresent(*http.Request) bool }); ok {
		present = present || bound.RequestIdentityPresent(r)
	}
	p, e := s.principal(r)
	if !present && errors.Is(e, authority.ErrInvalid) {
		return "", nil
	}
	return p.Account.ID, e
}
func (s *Service) lookupPublisher(ctx context.Context, viewer, ref string) (Publisher, error) {
	if scoped, ok := s.Repository.(ScopedPublisherReader); ok {
		return scoped.LookupPublisherForViewer(ctx, viewer, ref)
	}
	return s.Repository.LookupPublisher(ctx, ref)
}
func (s *Service) listPublishers(ctx context.Context, viewer, account string) ([]Publisher, error) {
	if scoped, ok := s.Repository.(ScopedPublisherReader); ok {
		return scoped.ListPublishersForViewer(ctx, viewer, account)
	}
	return s.Repository.ListPublishers(ctx, account)
}
func (s *Service) listPublisherMembers(ctx context.Context, viewer, ref string, offset, take int) ([]Member, int64, error) {
	if scoped, ok := s.Repository.(ScopedPublisherReader); ok {
		return scoped.ListMembersForViewer(ctx, viewer, ref, offset, take)
	}
	return s.Repository.ListMembers(ctx, ref, offset, take)
}
