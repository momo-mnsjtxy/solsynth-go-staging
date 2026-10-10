// SPDX-License-Identifier: AGPL-3.0-only
// Package media owns the explicitly documented filesystem-backed media contract.
package media

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"solsynth.local/go-migration/ports/authority"
)

type Failure struct {
	Status int    `json:"-"`
	Code   string `json:"code"`
}

func (f *Failure) Error() string { return f.Code }

var (
	ErrInvalid      = &Failure{400, "MEDIA_INVALID_REQUEST"}
	ErrTooLarge     = &Failure{413, "MEDIA_SIZE_LIMIT"}
	ErrDigest       = &Failure{422, "MEDIA_CONTENT_MISMATCH"}
	ErrNotFound     = &Failure{404, "MEDIA_NOT_FOUND"}
	ErrForbidden    = &Failure{403, "MEDIA_FORBIDDEN"}
	ErrQuota        = &Failure{403, "MEDIA_QUOTA_EXCEEDED"}
	ErrRateLimit    = &Failure{429, "MEDIA_RATE_LIMIT"}
	ErrQuotaMissing = &Failure{403, "MEDIA_QUOTA_NOT_CONFIGURED"}
	ErrConflict     = &Failure{409, "MEDIA_IDEMPOTENCY_CONFLICT"}
	ErrPending      = &Failure{409, "MEDIA_UPLOAD_IN_PROGRESS"}
	ErrGone         = &Failure{410, "MEDIA_UPLOAD_DELETED"}
	ErrUnavailable  = &Failure{503, "MEDIA_UNAVAILABLE"}
	ErrUnsupported  = &Failure{501, "MEDIA_FEATURE_UNSUPPORTED"}
)

const CreatePermission = "media.files.create"
const ReadPermission = "media.files.read"
const DeletePermission = "media.files.delete"

type File struct {
	ID        string    `json:"id"`
	AccountID string    `json:"account_id"`
	Name      string    `json:"name"`
	MIMEType  string    `json:"mime_type"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	Public    bool      `json:"is_public"`
	State     string    `json:"state"`
	Usage     string    `json:"usage,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
type Quota struct {
	LimitBytes    int64 `json:"limit_bytes"`
	UsedBytes     int64 `json:"used_bytes"`
	ReservedBytes int64 `json:"reserved_bytes"`
}
type Upload struct {
	Name, MIMEType, SHA256, IdempotencyKey string
	Usage                                  string
	Size                                   int64
	Public                                 bool
	Options                                Options
}

// Object keys are generated opaque UUIDs, never names or request paths.
// Put must consume exactly size bytes, check SHA256, durably publish, and leave
// no readable partial object. Remove must also remove abandoned staging bytes.
type ObjectStore interface {
	Put(context.Context, string, io.Reader, int64, string) error
	Open(context.Context, string) (io.ReadSeekCloser, error)
	Remove(context.Context, string) error
	Ready(context.Context) error
}

// MeasuredObjectStore supports the documented direct multipart upload, whose
// exact file length is not known until the part's EOF. limit is a hard bound.
type MeasuredObjectStore interface {
	PutMeasured(context.Context, string, io.Reader, int64) (int64, string, error)
}
type DirectRepository interface {
	UploadDirect(context.Context, string, Upload, io.Reader) (File, error)
}
type Repository interface {
	Upload(context.Context, string, Upload, io.Reader) (File, bool, error)
	Open(context.Context, string, string) (File, io.ReadSeekCloser, error)
	Info(context.Context, string, string) (File, error)
	Delete(context.Context, string, string) (File, error)
	Quota(context.Context, string) (Quota, error)
}
type Authority interface {
	AuthenticateRequest(*http.Request) (authority.Principal, error)
	AuthenticatePrincipal(context.Context, authority.Principal) (authority.Principal, error)
	AuthorizePrincipal(context.Context, authority.Principal, ...string) (authority.Principal, error)
}

func Normalize(err error) error {
	if err == nil {
		return nil
	}
	var f *Failure
	var a *authority.Failure
	if errors.As(err, &f) || errors.As(err, &a) {
		return err
	}
	return ErrUnavailable
}
