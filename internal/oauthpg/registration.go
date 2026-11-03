// SPDX-License-Identifier: AGPL-3.0-only
package oauthpg

import (
	"context"
	"errors"
	"solsynth.local/go-migration/ports/authority/oauth"
	"solsynth.local/go-migration/ports/develop/apps"
)

func (r *Repository) Register(ctx context.Context, actor string, req oauth.Registration) (oauth.RegistrationResult, error) {
	scopes, e := oauth.ValidateRegistration(req)
	if e != nil {
		return oauth.RegistrationResult{}, e
	}
	if r == nil {
		return oauth.RegistrationResult{}, oauth.ErrUnavailable
	}
	result, err := r.apps.RegisterOAuth(ctx, apps.Scope{Actor: actor, Developer: req.Developer, Project: req.ProjectID}, req, scopes)
	// Translate known domain rejections at the adapter boundary. A developer or
	// project ownership denial is not a transient provider failure, and private
	// project existence must not leak through a distinct not-found response.
	switch {
	case errors.Is(err, apps.ErrForbidden), errors.Is(err, apps.ErrNotFound):
		return oauth.RegistrationResult{}, oauth.ErrDenied
	case errors.Is(err, apps.ErrInvalid):
		return oauth.RegistrationResult{}, oauth.ErrRequest
	default:
		return result, err
	}
}
