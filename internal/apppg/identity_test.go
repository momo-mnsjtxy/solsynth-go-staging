// SPDX-License-Identifier: AGPL-3.0-only
package apppg

import (
	"bytes"
	"context"
	"solsynth.local/go-migration/ports/develop/apps"
	"testing"
)

func TestClientDigestIsAppAndVersionBound(t *testing.T) {
	a := secretDigest("one", "synthetic-ephemeral-token")
	b := secretDigest("two", "synthetic-ephemeral-token")
	if len(a) != 32 || bytes.Equal(a, b) || bytes.Equal(a, secretDigest("one", "changed")) {
		t.Fatal("app/secret isolation")
	}
}
func TestMutationRequiresFreshTransactionAuthority(t *testing.T) {
	r := New(nil)
	if e := r.write(context.Background(), "anything", "*", nil); e != apps.ErrUnavailable {
		t.Fatal("unguarded writer", e)
	}
}
