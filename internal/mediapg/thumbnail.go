// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	_ "golang.org/x/image/webp"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"solsynth.local/go-migration/ports/media"
	"strings"
)

type thumbnailBody struct{ *bytes.Reader }

func (thumbnailBody) Close() error { return nil }
func (r *Repository) Thumbnail(ctx context.Context, actor, id string) (media.File, io.ReadSeekCloser, error) {
	ctx, e := r.prepareBundleAccess(ctx, actor, "", id, media.BundlePasscode(ctx))
	if e != nil {
		return media.File{}, nil, e
	}
	f, body, e := r.Open(ctx, actor, id)
	if e != nil {
		return f, nil, e
	}
	defer body.Close()
	if f.Size > 20<<20 || !strings.HasPrefix(f.MIMEType, "image/") {
		return f, nil, media.ErrUnsupported
	}
	data, e := io.ReadAll(io.LimitReader(body, (20<<20)+1))
	if e != nil || len(data) > 20<<20 {
		return f, nil, media.ErrTooLarge
	}
	if f.MIMEType == "image/avif" {
		if r.renderer == nil {
			return f, nil, media.ErrUnavailable
		}
		data, e = r.renderer.RenderPNG(ctx, f.MIMEType, data)
		if e != nil {
			return f, nil, e
		}
	}
	cfg, _, e := image.DecodeConfig(bytes.NewReader(data))
	if e != nil || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 16000000 {
		return f, nil, media.ErrInvalid
	}
	src, _, e := image.Decode(bytes.NewReader(data))
	if e != nil {
		return f, nil, media.ErrInvalid
	}
	width, height := cfg.Width, cfg.Height
	if width > 256 || height > 256 {
		if width >= height {
			height = max(1, height*256/width)
			width = 256
		} else {
			width = max(1, width*256/height)
			height = 256
		}
	}
	dst := image.NewNRGBA(image.Rect(0, 0, width, height))
	bounds := src.Bounds()
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			dst.Set(x, y, src.At(bounds.Min.X+x*cfg.Width/width, bounds.Min.Y+y*cfg.Height/height))
		}
	}
	var out bytes.Buffer
	if e = png.Encode(&out, dst); e != nil {
		return f, nil, e
	}
	if _, e = r.Info(ctx, actor, id); e != nil {
		return f, nil, e
	}
	sum := sha256.Sum256(out.Bytes())
	f.Size = int64(out.Len())
	f.SHA256 = hex.EncodeToString(sum[:])
	f.MIMEType = "image/png"
	f.Name += ".thumbnail.png"
	return f, thumbnailBody{bytes.NewReader(out.Bytes())}, nil
}
