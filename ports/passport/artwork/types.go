// SPDX-License-Identifier: AGPL-3.0-only
package artwork

import (
	"context"
	"encoding/hex"
	"io"
	"mime"
	"strings"

	"solsynth.local/go-migration/ports/media"
)

const ManagePermission = "presences.artwork.manage"
const MaxBytes = 1 << 20

type Artwork struct {
	Hash     string `json:"hash"`
	MIMEType string `json:"mime_type"`
	Size     int64  `json:"size"`
	URL      string `json:"url"`
}
type Repository interface {
	UploadArtwork(context.Context, string, string, []byte) (Artwork, error)
	OpenArtwork(context.Context, string, string) (Artwork, io.ReadSeekCloser, error)
}

func NormalizeHash(value string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(value))
	v = strings.TrimPrefix(v, "sha256:")
	if len(v) != 64 {
		return "", media.ErrInvalid
	}
	if _, err := hex.DecodeString(v); err != nil {
		return "", media.ErrInvalid
	}
	return "sha256:" + v, nil
}
func ImageType(value string) (string, error) {
	typ, params, err := mime.ParseMediaType(value)
	if err != nil || len(params) != 0 || !strings.HasPrefix(typ, "image/") || len(typ) > 255 {
		return "", media.ErrInvalid
	}
	return typ, nil
}
