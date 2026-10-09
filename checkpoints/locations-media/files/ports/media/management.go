// SPDX-License-Identifier: AGPL-3.0-only
package media

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"solsynth.local/go-migration/ports/authority"
	"time"
)

const UpdatePermission = "media.files.update"

type Options struct {
	PoolID              string     `json:"pool_id,omitempty"`
	BundleID            string     `json:"bundle_id,omitempty"`
	ParentID            string     `json:"parent_id,omitempty"`
	EncryptionScheme    string     `json:"encryption_scheme,omitempty"`
	EncryptionHeader    string     `json:"encryption_header,omitempty"`
	EncryptionSignature string     `json:"encryption_signature,omitempty"`
	ExpiredAt           *time.Time `json:"expired_at,omitempty"`
}
type UploadOptionsSource interface{ UploadOptions() (Options, error) }

func OptionsFromFields(fields map[string]string) (Options, error) {
	v := Options{PoolID: fields["pool_id"], BundleID: fields["bundle_id"], ParentID: fields["parent_id"], EncryptionScheme: fields["encryption_scheme"], EncryptionHeader: fields["encryption_header"], EncryptionSignature: fields["encryption_signature"]}
	if raw := fields["expired_at"]; raw != "" {
		expiry, e := time.Parse(time.RFC3339Nano, raw)
		if e != nil {
			return v, ErrInvalid
		}
		v.ExpiredAt = &expiry
	}
	return v, ValidateOptions(v, time.Now())
}

func ValidateOptions(v Options, now time.Time) error {
	for _, id := range []string{v.PoolID, v.BundleID, v.ParentID} {
		if id != "" && !authority.ValidID(id) {
			return ErrUnsupported
		}
	}
	if v.ExpiredAt != nil && (!v.ExpiredAt.After(now) || v.ExpiredAt.After(now.Add(366*24*time.Hour))) {
		return ErrInvalid
	}
	if v.EncryptionScheme != "" {
		if v.EncryptionScheme != "file.aesgcm.v1" || v.EncryptionHeader == "" || v.EncryptionSignature == "" {
			return ErrUnsupported
		}
		for _, s := range []string{v.EncryptionHeader, v.EncryptionSignature} {
			b, e := base64.StdEncoding.Strict().DecodeString(s)
			if e != nil || len(b) < 16 || len(b) > 16384 {
				return ErrInvalid
			}
		}
	} else if v.EncryptionHeader != "" || v.EncryptionSignature != "" {
		return ErrInvalid
	}
	return nil
}

type FileRecord struct {
	File
	Options
	IsFolder  bool                       `json:"is_folder"`
	Indexed   bool                       `json:"indexed"`
	Meta      map[string]json.RawMessage `json:"user_meta"`
	Sensitive []string                   `json:"sensitive_marks"`
}
type FileQuery struct {
	ParentID, Query, Order, PoolID string
	Unindexed, Descending, All     bool
	Take, Offset                   int
}
type FilePatch struct {
	Name          *string                    `json:"name"`
	ParentID      *string                    `json:"parent_id"`
	ParentPresent bool                       `json:"-"`
	Indexed       *bool                      `json:"indexed"`
	Meta          map[string]json.RawMessage `json:"user_meta"`
	Sensitive     []string                   `json:"sensitive_marks"`
}
type Pool struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	AccountID       string `json:"account_id"`
	Public          bool   `json:"is_public"`
	AllowEncryption bool   `json:"allow_encryption"`
	MaxFileBytes    int64  `json:"max_file_bytes"`
}
type UploadStats struct {
	TotalTasks         int64      `json:"total_tasks"`
	InProgressTasks    int64      `json:"in_progress_tasks"`
	CompletedTasks     int64      `json:"completed_tasks"`
	FailedTasks        int64      `json:"failed_tasks"`
	ExpiredTasks       int64      `json:"expired_tasks"`
	TotalUploadedBytes int64      `json:"total_uploaded_bytes"`
	AverageProgress    float64    `json:"average_progress"`
	RecentActivity     *time.Time `json:"recent_activity"`
}
type TaskDetails struct {
	Task             UploadTask `json:"task"`
	Options          Options    `json:"options"`
	UploadedBytes    int64      `json:"uploaded_bytes"`
	UploadSpeedBytes float64    `json:"upload_speed_bytes"`
	EstimatedSeconds *float64   `json:"estimated_seconds_remaining"`
}
type ManagementRepository interface {
	FileRecord(context.Context, string, string) (FileRecord, error)
	ListFiles(context.Context, string, FileQuery) ([]FileRecord, int64, error)
	CreateFolder(context.Context, string, string, *string) (FileRecord, error)
	PatchFile(context.Context, string, string, FilePatch) (FileRecord, error)
	DeleteFiles(context.Context, string, []string) (int, error)
	Pools(context.Context, string) ([]Pool, error)
	CreatePool(context.Context, string, Pool) (Pool, error)
	UploadStatistics(context.Context, string) (UploadStats, error)
	UploadTaskDetails(context.Context, string, string) (TaskDetails, error)
	CleanupUploadTasks(context.Context, string) (int, error)
	FileReferences(context.Context, string, string) ([]File, error)
}
type ThumbnailRepository interface {
	Thumbnail(context.Context, string, string) (File, io.ReadSeekCloser, error)
}

// RasterRenderer processes untrusted AVIF/video bytes with explicit CPU, memory,
// duration and output bounds. Nil disables these formats; no executable is
// discovered from PATH or inferred from uploaded names.
type RasterRenderer interface {
	RenderPNG(context.Context, string, []byte) ([]byte, error)
}
type LivePhotoRequest struct {
	ImageID string `json:"image_id"`
	VideoID string `json:"video_id"`
}
type LivePhoto struct {
	ID              string    `json:"id"`
	AccountID       string    `json:"account_id"`
	ApplicationType string    `json:"application_type"`
	Image           File      `json:"image"`
	Video           File      `json:"video"`
	CreatedAt       time.Time `json:"created_at"`
}
type CompositionRepository interface {
	ComposeLivePhoto(context.Context, string, LivePhotoRequest) (LivePhoto, error)
	LivePhoto(context.Context, string, string) (LivePhoto, error)
	DeleteLivePhoto(context.Context, string, string) error
}
type RemoteUploadRequest struct {
	URL            string  `json:"url"`
	Name           string  `json:"name"`
	SHA256         string  `json:"sha256"`
	Size           int64   `json:"size"`
	ContentType    string  `json:"content_type"`
	IdempotencyKey string  `json:"idempotency_key"`
	Options        Options `json:"options"`
}

// RemoteFetcher must enforce an explicit HTTPS host allowlist, validate every
// redirect, and reject non-public destinations before it connects. Nil disables
// remote fetches; user-controlled URLs never enter the default HTTP client.
type RemoteFetcher interface {
	Fetch(context.Context, string, int64) (io.ReadCloser, error)
}
type RemoteUploadRepository interface {
	UploadRemote(context.Context, string, RemoteUploadRequest) (File, bool, error)
}
type bundlePasscodeKey struct{}

func WithBundlePasscode(ctx context.Context, passcode string) context.Context {
	return context.WithValue(ctx, bundlePasscodeKey{}, passcode)
}
func BundlePasscode(ctx context.Context) string {
	v, _ := ctx.Value(bundlePasscodeKey{}).(string)
	return v
}

type BundleRequest struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Passcode    *string    `json:"passcode"`
	ExpiredAt   *time.Time `json:"expired_at"`
	Public      bool       `json:"is_public"`
}
type Bundle struct {
	ID          string     `json:"id"`
	AccountID   string     `json:"account_id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	ExpiredAt   *time.Time `json:"expired_at"`
	Public      bool       `json:"is_public"`
	Protected   bool       `json:"has_passcode"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	Files       []File     `json:"files"`
}
type BundleRepository interface {
	SaveBundle(context.Context, string, string, BundleRequest) (Bundle, error)
	Bundle(context.Context, string, string, string) (Bundle, error)
	Bundles(context.Context, string, int, int) ([]Bundle, error)
	DeleteBundle(context.Context, string, string) error
}
type UsageDetails struct {
	PoolID     string `json:"pool_id"`
	PoolName   string `json:"pool_name"`
	UsageBytes int64  `json:"usage_bytes"`
	FileCount  int64  `json:"file_count"`
}
type StorageUsage struct {
	PoolUsages      []UsageDetails `json:"pool_usages"`
	TotalUsageBytes int64          `json:"total_usage_bytes"`
	TotalFileCount  int64          `json:"total_file_count"`
	TotalQuota      int64          `json:"total_quota"`
	UsedQuota       int64          `json:"used_quota"`
	ReservedQuota   int64          `json:"reserved_quota"`
}
type QuotaRecord struct {
	ID          string     `json:"id"`
	AccountID   string     `json:"account_id"`
	SourceKey   string     `json:"source_key"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	QuotaBytes  int64      `json:"quota_bytes"`
	ExpiredAt   *time.Time `json:"expired_at"`
	CreatedAt   time.Time  `json:"created_at"`
}
type BillingRepository interface {
	StorageUsage(context.Context, string, string) (StorageUsage, error)
	QuotaRecords(context.Context, string, bool, int, int) ([]QuotaRecord, int64, error)
	AddQuotaRecord(context.Context, string, QuotaRecord) (QuotaRecord, error)
	CleanupRecycled(context.Context, string, string) (int, error)
}
