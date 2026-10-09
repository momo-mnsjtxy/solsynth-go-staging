// SPDX-License-Identifier: AGPL-3.0-only
package passkit_test

import (
	"testing"

	"solsynth.local/go-migration/internal/testpasskit"
	"solsynth.local/go-migration/ports/passport/passkit"
)

func TestPasskitRejectsTruncatedPNGAfterValidHeader(t *testing.T) {
	f := testpasskit.New(t)
	// Signature + IHDR is enough for DecodeConfig but is not a complete image.
	f.Config.Assets["icon.png"] = f.Config.Assets["icon.png"][:33]
	if _, e := passkit.NewGenerator(f.Config); e == nil {
		t.Fatal("incomplete PNG accepted as a signable pass asset")
	}
}
