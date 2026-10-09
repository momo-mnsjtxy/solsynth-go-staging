// SPDX-License-Identifier: AGPL-3.0-only
package taxonomy

import (
	"context"
	"net/http"
	"time"
)

// Authority is supplied by the host, never by request data. Each operation must
// revalidate a proof-bearing principal and use current server-side policy.
// Publisher identity, membership and entitlements are separate from account IDs.
type Authority interface {
	Require(context.Context, Grant) (string, error)
	Publisher(context.Context, string) (string, error)
	Manager(context.Context, string) error
	Entitlement(context.Context, string) (Entitlement, error)
}
type Entitlement struct {
	Total            int64
	Level, PerkLevel int
}

func NewAuthorityHandler(repo Repository, identity Authority, now func() time.Time) http.Handler {
	if identity == nil {
		return NewHandler(NewService(repo))
	}
	if now == nil {
		now = time.Now
	}
	return &Handler{s: &Service{repo: repo, trusted: identity, now: now}}
}
func (s *Service) authorize(ctx context.Context, grant Grant) error {
	if s.trusted != nil {
		_, err := s.trusted.Require(ctx, grant)
		return err
	}
	if s.authority.ActorID == "" {
		return ErrUnauthenticated
	}
	if grant != "" && !s.authority.allows(grant) {
		return ErrForbidden
	}
	return nil
}
func (s *Service) actor(ctx context.Context) (string, error) {
	if s.trusted != nil {
		return s.trusted.Require(ctx, "")
	}
	if err := s.authorize(ctx, ""); err != nil {
		return "", err
	}
	return s.authority.ActorID, nil
}
func (s *Service) publisher(ctx context.Context, name string) (string, error) {
	if s.trusted != nil {
		return s.trusted.Publisher(ctx, name)
	}
	return s.authority.publisher(name)
}
func (s *Service) manager(ctx context.Context, id *string) error {
	if id == nil {
		return ErrForbidden
	}
	if s.trusted != nil {
		return s.trusted.Manager(ctx, *id)
	}
	if !s.authority.manager(id) {
		return Problem("FORBIDDEN", "Publisher manager permission required.", 403)
	}
	return nil
}
func (s *Service) entitlement(ctx context.Context, id string) (Entitlement, error) {
	if s.trusted != nil {
		return s.trusted.Entitlement(ctx, id)
	}
	return Entitlement{s.authority.ProtectedTagTotal, s.authority.Level, s.authority.PerkLevel}, nil
}
