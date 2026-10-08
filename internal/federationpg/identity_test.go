// SPDX-License-Identifier: AGPL-3.0-only
package federationpg

import (
	f "solsynth.local/go-migration/ports/federation"
	"testing"
)

func TestFederationIdentityAnchorIsIndependentOfMutableDisplayFields(t *testing.T) {
	r := &Repository{config: f.Config{Origin: "https://local.example.test"}}
	actor, key, e := r.identityFor("Original_Handle")
	if e != nil {
		t.Fatal(e)
	}
	p := publisher{name: "renamed_handle", nick: "New display name", identityHandle: "Original_Handle", actorURI: actor, keyID: key}
	if !r.identityMatches(p) || r.actorID(p) != actor {
		t.Fatal("mutable fields changed identity")
	}
	other := &Repository{config: f.Config{Origin: "https://other.example.test"}}
	if other.identityMatches(p) {
		t.Fatal("origin rewrite accepted")
	}
	p.keyID = actor + "#another-key"
	if r.identityMatches(p) {
		t.Fatal("key-ID rewrite accepted")
	}
}
