// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"net/http/httptest"
	"testing"
)

func TestFixtureAuthenticationMustBeExplicit(t *testing.T) {
	r := httptest.NewRequest("GET", "http://127.0.0.1/api/posts/watch", nil)
	r.Header.Set("X-Test-Account", "synthetic-a")
	if _, err := (fixtureAuth{}).AccountID(r); err == nil {
		t.Fatal("default configuration accepted test header")
	}
	a := fixtureAuth{account: "synthetic-a"}
	if id, err := a.AccountID(r); err != nil || id != "synthetic-a" {
		t.Fatal(id, err)
	}
	r.Header.Set("X-Test-Account", "synthetic-b")
	if _, err := a.AccountID(r); err == nil {
		t.Fatal("other account accepted")
	}
	if err := a.CanManagePostSubscriptions(r, "synthetic-b"); err == nil {
		t.Fatal("cross-account permission accepted")
	}
}
