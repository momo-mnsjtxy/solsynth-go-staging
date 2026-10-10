// SPDX-License-Identifier: AGPL-3.0-only
package oauthpg

import (
	"context"
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
	return r.apps.RegisterOAuth(ctx, apps.Scope{Actor: actor, Developer: req.Developer, Project: req.ProjectID}, req, scopes)
}
