// SPDX-License-Identifier: AGPL-3.0-only
package profilemediapg

import (
	"context"
	"errors"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/accounts"
	"testing"
)

func TestProfileMediaFailClosedAndPassiveFormats(t *testing.T) {
	var r *Repository
	if e := r.Apply(context.Background(), nil, "", accounts.ProfilePatch{}); !errors.Is(e, accounts.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e := r.CanReadInTransaction(context.Background(), nil, "", ""); !errors.Is(e, media.ErrUnavailable) {
		t.Fatal(e)
	}
	for _, mime := range []string{"image/svg+xml", "text/html", "application/octet-stream", "image/png; charset=utf-8", "IMAGE/PNG", "image/png\x00"} {
		if imageType(mime) {
			t.Fatal("active/unknown media", mime)
		}
	}
	for _, mime := range []string{"image/png", "image/jpeg", "image/webp", "image/avif"} {
		if !imageType(mime) {
			t.Fatal(mime)
		}
	}
}
