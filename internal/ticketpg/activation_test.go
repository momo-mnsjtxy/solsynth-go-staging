// SPDX-License-Identifier: AGPL-3.0-only
package ticketpg

import (
	"context"
	"solsynth.local/go-migration/ports/authority"
	"testing"
)

func TestActivationPolicyRejectsAdministrativeAmbiguousAndWildcardGrants(t *testing.T) {
	for _, keys := range [][]string{{"*"}, {"posts.*"}, {"accounts.manage"}, {"permissions.manage"}, {"affiliations.manage"}, {"affiliations.invites.issue"}, {"wallets.transactions.manage"}, {"posts.create", "posts.create"}, {"unknown.member"}, {" posts.create"}, {"POSTS.CREATE"}} {
		if ValidateActivationCompletion(ActivationCompletion{PolicyID: "member-v1", Permissions: keys}) == nil {
			t.Fatalf("unsafe grants accepted: %v", keys)
		}
	}
	for _, id := range []string{"", " padded ", "a/b", "a\n"} {
		if ValidateActivationCompletion(ActivationCompletion{PolicyID: id}) == nil {
			t.Fatal(id)
		}
	}
	if e := ValidateActivationCompletion(ActivationCompletion{PolicyID: "member-v1", Permissions: []string{"posts.create", "tests.take"}}); e != nil {
		t.Fatal(e)
	}
	if e := ValidateActivationCompletion(ActivationCompletion{PolicyID: "state-only"}); e != nil {
		t.Fatal(e)
	}
	if authority.MemberActivationPermission("tests.manage") {
		t.Fatal("administrative grant in member allowlist")
	}
}

func TestActivationAssuranceIsBoundToRecipient(t *testing.T) {
	actor := "00000000-0000-4000-8000-000000000001"
	other := "00000000-0000-4000-8000-000000000002"
	ctx := permitRootActivation(context.Background(), authority.Principal{Account: authority.Account{ID: actor}, Session: authority.Session{ID: other, AccountID: actor}})
	if _, ok := activationAssuranceFor(ctx, actor); !ok {
		t.Fatal("recipient lost its completion assurance")
	}
	for _, target := range []string{other, "", "invalid"} {
		if _, ok := activationAssuranceFor(ctx, target); ok {
			t.Fatalf("completion assurance crossed recipient boundary: %q", target)
		}
	}
	for _, ctx := range []context.Context{context.Background(), permitRootActivation(context.Background(), authority.Principal{Account: authority.Account{ID: actor}})} {
		if _, ok := activationAssuranceFor(ctx, actor); ok {
			t.Fatal("missing session assurance permitted activation")
		}
	}
	for _, p := range []activationProof{{subject: actor, session: other, purpose: "verify_email"}, {subject: actor, purpose: "reset_password"}, {subject: actor, purpose: "verify_contact"}} {
		if _, ok := activationAssuranceFor(context.WithValue(context.Background(), activationProofKey{}, p), actor); ok {
			t.Fatal("mixed or unrelated proof accepted", p)
		}
	}
}
