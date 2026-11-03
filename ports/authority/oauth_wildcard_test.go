// SPDX-License-Identifier: AGPL-3.0-only
package authority

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOAuthWildcardExpandsOnlyDelegableResources(t *testing.T) {
	for _, pattern := range []string{"*", "posts.*", "chat.*", "chat.messages.*", "profile.*", "wallet.*", "notifications.*", "realms.*"} {
		resolved, err := ExpandOAuthScopes([]string{"openid", pattern})
		if err != nil || len(resolved) < 2 {
			t.Fatal(pattern, resolved, err)
		}
		for _, scope := range resolved {
			if strings.Contains(scope, "*") || scope == "profile" || scope == "email" || scope == "offline_access" || strings.HasPrefix(scope, "admin.") {
				t.Fatal("wildcard expanded outside resource grant", pattern, scope)
			}
		}
	}
	for _, pattern := range []string{"admin.*", "developers.*", "posts.create.*", "post*", "posts.*.delete", "unknown.*"} {
		if _, err := ExpandOAuthScopes([]string{"openid", pattern}); err == nil {
			t.Fatal("unknown or administrative scope accepted", pattern)
		}
	}
	for _, scope := range []string{"openid", "profile", "email", "offline_access", "admin.accounts.view", "posts.create.blog"} {
		if OAuthScopeCovers("*", scope) || OAuthResourceScope(Session{Type: 1, Category: OAuthSessionCategory, Scopes: []string{"*"}}, scope) {
			t.Fatal("resource wildcard covered unrelated permission/disclosure", scope)
		}
	}
}

func TestOAuthWildcardSignedSessionKeepsCanonicalDenies(t *testing.T) {
	base, memory, clock := setup(t)
	allowed := &atomic.Bool{}
	allowed.Store(true)
	store := oauthBoundMemory{memory, allowed}
	issuer, err := New(store, base.cfg)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewVerifier(store, base.cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	parentPair := issue(t, issuer, accountA, "*")
	parent, err := verifier.Authenticate(ctx, parentPair.Token)
	if err != nil {
		t.Fatal(err)
	}
	memory.state[accountA].rules = []Rule{{Pattern: "*"}}
	var pair Pair
	err = store.WithinAccount(ctx, accountA, func(tx Transaction) error {
		_, issued, e := issuer.IssueOAuthTransaction(ctx, tx, parent.Session.ID, accountB, []string{"openid", "*"}, time.Unix(clock.Load(), 0).Add(time.Hour))
		pair = issued
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	delegated, err := verifier.AuthenticateOAuth(ctx, pair.Token)
	if err != nil {
		t.Fatal(err)
	}
	if len(delegated.Session.Scopes) != 1+len(oauthResourceScopes) {
		t.Fatal("signed session did not record concrete resource consent", delegated.Session.Scopes)
	}
	if _, err = verifier.AuthorizePrincipal(ctx, delegated, "posts.create"); err != nil {
		t.Fatal("expanded resource not usable", err)
	}
	if _, err = verifier.AuthorizePrincipal(ctx, delegated, "admin.accounts.view"); !errors.Is(err, ErrForbidden) {
		t.Fatal("wildcard escaped resource boundary", err)
	}
	memory.state[accountA].rules = append(memory.state[accountA].rules, Rule{Pattern: "posts.create", Deny: true})
	if _, err = verifier.AuthorizePrincipal(ctx, delegated, "posts.create"); !errors.Is(err, ErrForbidden) {
		t.Fatal("wildcard bypassed fresh canonical deny", err)
	}
}
