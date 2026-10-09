// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestFederationIdentityCommandValidatesWithoutDatabase(t *testing.T) {
	var out bytes.Buffer
	if e := run([]string{"initialize-federation-identities", "--origin=https://synthetic.example.test", "--dsn-env=SYNTHETIC_UNSET_IDENTITY_DSN"}, &out); e != nil || !strings.Contains(out.String(), "no database connection made") {
		t.Fatal(out.String(), e)
	}
	for _, args := range [][]string{{"--origin=http://synthetic.example.test"}, {"--origin=https://synthetic.example.test", "--limit=101"}, {"--origin=https://synthetic.example.test", "--timeout=0s"}, {"--origin=https://synthetic.example.test", "--publisher=arbitrary-handle"}} {
		if e := initializeFederationIdentities(args, &bytes.Buffer{}); e == nil {
			t.Fatal(args)
		}
	}
}
