// SPDX-License-Identifier: AGPL-3.0-only
// Source-informed from DysonNetwork Develop at
// 4ee2d0cebb9da8284a92483fee6dce795bf6947c. Modified 2026-10-08.
package develop

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

var (
	ErrPackagePolicyUnverified = errors.New("miniapp package policy unverified")
	ErrPackageNotFound         = errors.New("miniapp not found")
	ErrPackageMissing          = errors.New("miniapp has no published package")
	ErrPackageUnavailable      = errors.New("miniapp package unavailable")
	ErrPackageInvalidSlug      = errors.New("invalid miniapp slug")
	ErrPackageInvalidKey       = errors.New("invalid local package key")
)

// PackageRecord is an internal query projection, never entity response JSON.
type PackageRecord struct {
	ID         string
	Slug       string
	StorageKey *string
	URL        *string
}

type PackageRepository interface {
	FindPublishedPackage(context.Context, string) (PackageRecord, error)
	IncrementPackageDownloads(context.Context, string) error
}

type PackageStorage interface {
	ReadPackage(context.Context, string) ([]byte, error)
}

type PackageDownload struct {
	Slug        string
	RedirectURL string
	Data        []byte
}

type PackageService struct {
	repository PackageRepository
	storage    PackageStorage
	candidate  bool
}

// NewPackageService is disabled even when supplied working dependencies.
func NewPackageService(repository PackageRepository, storage PackageStorage) *PackageService {
	return &PackageService{repository: repository, storage: storage}
}

// NewSyntheticCompatibilityPackageService explicitly selects the isolated
// candidate. It grants no production identity or permission.
func NewSyntheticCompatibilityPackageService(repository PackageRepository, storage PackageStorage) *PackageService {
	return &PackageService{repository: repository, storage: storage, candidate: true}
}

func ValidatePackageSlug(slug string) error {
	if slug == "" || len(slug) > 1024 || strings.ContainsAny(slug, "/\\\x00\r\n") {
		return ErrPackageInvalidSlug
	}
	return nil
}

// Download intentionally commits the counter BEFORE resolving the redirect or
// reading storage, matching the retained controller/service ordering. A failed
// storage read is not a successful download, but does not undo this increment.
// There is no encompassing transaction and no automatic retry of the counter.
func (s *PackageService) Download(ctx context.Context, slug string) (PackageDownload, error) {
	if s == nil || !s.candidate {
		return PackageDownload{}, ErrPackagePolicyUnverified
	}
	if err := ctx.Err(); err != nil {
		return PackageDownload{}, err
	}
	if err := ValidatePackageSlug(slug); err != nil {
		return PackageDownload{}, err
	}
	if s.repository == nil {
		return PackageDownload{}, ErrPackageUnavailable
	}
	app, err := s.repository.FindPublishedPackage(ctx, slug)
	if err != nil {
		return PackageDownload{}, err
	}
	if app.StorageKey == nil || strings.TrimSpace(*app.StorageKey) == "" {
		return PackageDownload{}, ErrPackageMissing
	}
	if err := s.repository.IncrementPackageDownloads(ctx, app.ID); err != nil {
		return PackageDownload{}, err
	}
	if err := ctx.Err(); err != nil {
		return PackageDownload{}, err
	}
	result := PackageDownload{Slug: app.Slug}
	if app.URL != nil && strings.TrimSpace(*app.URL) != "" {
		// Candidate restriction: source Redirect accepts more URL forms. Do not
		// emit credentials, control characters or non-HTTP navigation schemes.
		u, err := url.Parse(*app.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || strings.IndexFunc(*app.URL, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
			return PackageDownload{}, ErrPackageUnavailable
		}
		result.RedirectURL = *app.URL
		return result, nil
	}
	if s.storage == nil {
		return PackageDownload{}, ErrPackageUnavailable
	}
	result.Data, err = s.storage.ReadPackage(ctx, *app.StorageKey)
	if err != nil {
		return PackageDownload{}, err
	}
	if err := ctx.Err(); err != nil {
		return PackageDownload{}, err
	}
	return result, nil
}
