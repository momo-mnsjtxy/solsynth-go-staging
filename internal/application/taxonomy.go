// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"solsynth.local/go-migration/ports/sphere/taxonomy"
)

// Permission names follow retained PERMISSIONS.md and are mapped explicitly to
// this port's symbolic grants; no excluded Shared constants are imported.
var taxonomyPermissions = map[taxonomy.Grant]string{
	"": "", taxonomy.CategoriesSubscribe: "post.categories.subscribe",
	taxonomy.Moderate: "posts.moderate", taxonomy.CategoriesManage: "post.categories.manage",
	taxonomy.TagsCreate: "posts.tags.create", taxonomy.TagsUpdate: "posts.tags.update",
	taxonomy.TagsClaim: "posts.tags.claim", taxonomy.TagsAssign: "posts.tags.assign",
	taxonomy.TagsProtect: "posts.tags.protect", taxonomy.TagsEvent: "posts.tags.event",
	taxonomy.TagsDelete: "posts.tags.delete",
}

// PublisherPolicy is server-owned persisted policy. It must not accept an account
// ID as proof: taxonomyIdentity always authorizes the private principal first.
type PublisherPolicy interface {
	Resolve(context.Context, string, string) (string, error)
	RequireManager(context.Context, string, string) error
	Entitlement(context.Context, string) (taxonomy.Entitlement, error)
}
type taxonomyIdentity struct {
	identity
	publishers PublisherPolicy
}

func (a taxonomyIdentity) Require(ctx context.Context, g taxonomy.Grant) (string, error) {
	key, ok := taxonomyPermissions[g]
	if !ok {
		return "", taxonomy.ErrForbidden
	}
	var permissions []string
	if key != "" {
		permissions = []string{key}
	}
	p, err := a.require(ctx, permissions...)
	if err != nil {
		return "", taxonomyError(err)
	}
	return p.Account.ID, nil
}
func (a taxonomyIdentity) Publisher(ctx context.Context, selector string) (string, error) {
	actor, err := a.Require(ctx, "")
	if err != nil {
		return "", err
	}
	if a.publishers == nil {
		return "", taxonomy.ErrPolicyUnverified
	}
	return a.publishers.Resolve(ctx, actor, selector)
}
func (a taxonomyIdentity) Manager(ctx context.Context, publisher string) error {
	actor, err := a.Require(ctx, "")
	if err != nil {
		return err
	}
	if a.publishers == nil {
		return taxonomy.ErrPolicyUnverified
	}
	return a.publishers.RequireManager(ctx, actor, publisher)
}
func (a taxonomyIdentity) Entitlement(ctx context.Context, publisher string) (taxonomy.Entitlement, error) {
	if _, err := a.Require(ctx, ""); err != nil {
		return taxonomy.Entitlement{}, err
	}
	if a.publishers == nil {
		return taxonomy.Entitlement{}, taxonomy.ErrPolicyUnverified
	}
	return a.publishers.Entitlement(ctx, publisher)
}
