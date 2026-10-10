// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"io"
	"os"
	"testing"
)

func TestRendererOutputBudgetCannotBeBypassedByCopy(t *testing.T) {
	var out boundedRenderOutput
	if _, e := io.Copy(&out, io.LimitReader(bytes.NewReader(make([]byte, 2<<20)), 2<<20)); e == nil || len(out.Bytes()) > 1<<20 {
		t.Fatal("copy bypassed output bound", len(out.Bytes()), e)
	}
}

func TestWebPDecodeHeaderBudget(t *testing.T) {
	data, e := base64.StdEncoding.DecodeString("UklGRjgAAABXRUJQVlA4ICwAAACQAQCdASoQABAAAgA0JaACdLoAA5gA/vmTb/+QH/+QH/+QH/8gP+IXeyAwAA==")
	if e != nil {
		t.Fatal(e)
	}
	cfg, format, e := image.DecodeConfig(bytes.NewReader(data))
	if e != nil || format != "webp" || cfg.Width != 16 || cfg.Height != 16 {
		t.Fatal(cfg, format, e)
	}
	decoded, _, e := image.Decode(bytes.NewReader(data))
	if e != nil || decoded.Bounds().Dx() != 16 {
		t.Fatal(e)
	}
}

func TestFFmpegRendererActualAVIFAndBounds(t *testing.T) {
	if _, e := os.Stat("/usr/bin/ffmpeg"); e != nil {
		t.Skip("optional explicitly provisioned ffmpeg unavailable")
	}
	if _, e := os.Stat("/usr/bin/prlimit"); e != nil {
		t.Skip("optional Linux process limiter unavailable")
	}
	r, e := NewFFmpegRenderer("/usr/bin/ffmpeg", "/usr/bin/prlimit")
	if e != nil {
		t.Fatal(e)
	}
	// A synthetic 16x16 blue AVIF encoded with libaom-av1; no network or DB.
	data, e := base64.StdEncoding.DecodeString("AAAAIGZ0eXBhdmlmAAAAAGF2aWZtaWYxbWlhZk1BMUIAAAD5bWV0YQAAAAAAAAAvaGRscgAAAAAAAAAAcGljdAAAAAAAAAAAAAAAAFBpY3R1cmVIYW5kbGVyAAAAAA5waXRtAAAAAAABAAAAHmlsb2MAAAAARAAAAQABAAAAAQAAASEAAAAiAAAAKGlpbmYAAAAAAAEAAAAaaW5mZQIAAAAAAQAAYXYwMUNvbG9yAAAAAGppcHJwAAAAS2lwY28AAAAUaXNwZQAAAAAAAAAQAAAAEAAAABBwaXhpAAAAAAMICAgAAAAMYXYxQ4EADAAAAAATY29scm5jbHgAAgACAAIAAAAAF2lwbWEAAAAAAAAAAQABBAECgwQAAAAqbWRhdAoKAAAAAZ/5tfMAgDIUEADAAAACgAAACeq401IFGveH5VA=")
	if e != nil {
		t.Fatal(e)
	}
	out, e := r.RenderPNG(context.Background(), "image/avif", data)
	if e != nil {
		t.Fatal(e)
	}
	cfg, e := png.DecodeConfig(bytes.NewReader(out))
	if e != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 256 || cfg.Height > 256 {
		t.Fatal(cfg, e)
	}
	if _, e = r.RenderPNG(context.Background(), "image/avif", []byte("https://private.example/source")); e == nil {
		t.Fatal("invalid AVIF accepted")
	}
	if _, e = r.RenderPNG(context.Background(), "image/avif", make([]byte, (20<<20)+1)); e == nil {
		t.Fatal("oversized input accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = r.RenderPNG(ctx, "image/avif", data); e == nil {
		t.Fatal("cancelled processing accepted")
	}
	if _, e = NewFFmpegRenderer("ffmpeg", "prlimit"); e == nil {
		t.Fatal("ambient executables accepted")
	}
}
