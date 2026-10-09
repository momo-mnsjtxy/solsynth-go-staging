// SPDX-License-Identifier: AGPL-3.0-only
package blogs

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"solsynth.local/go-migration/ports/sphere/reader"
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (e *Failure) Error() string { return e.Code }

var (
	ErrInvalid     = &Failure{"PUBLISHER_DOMAIN_INVALID", "A valid public DNS domain is required.", 400}
	ErrNotFound    = &Failure{"PUBLISHER_DOMAIN_NOT_FOUND", "Publisher domain not found.", 404}
	ErrConflict    = &Failure{"PUBLISHER_DOMAIN_CONFLICT", "The domain already exists or its verification changed. Retry the request.", 409}
	ErrUnverified  = &Failure{"POST_BLOG_DOMAIN_UNVERIFIED", "The domain is not verified for this publisher.", 403}
	ErrURL         = &Failure{"POST_BLOG_INVALID_URL", "Blog post content must be a valid HTTPS URL.", 400}
	ErrPreview     = &Failure{"POST_BLOG_PREVIEW_FAILED", "Unable to fetch a link preview for this blog URL.", 400}
	ErrUnavailable = &Failure{"POST_BLOG_UNAVAILABLE", "The blog dependency is unavailable.", 503}
)

// Values come from the licensed root-owned PublisherVerifiedDomain.cs enum.
const (
	Pending = iota
	Verified
	Failed
	Revoked
)

type Domain struct {
	ID             string     `json:"id"`
	PublisherID    string     `json:"publisher_id"`
	Domain         string     `json:"domain"`
	Status         int        `json:"status"`
	VerifiedAt     *time.Time `json:"verified_at"`
	LastCheckedAt  *time.Time `json:"last_checked_at"`
	FailedAttempts int        `json:"failed_attempts"`
	LastError      *string    `json:"last_error"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	DeletedAt      *time.Time `json:"deleted_at"`
	Revision       int64      `json:"-"`
	VerifiedName   *string    `json:"-"`
}
type Permission struct {
	CanCreate             bool   `json:"can_create"`
	PermissionNodeGranted bool   `json:"permission_node_granted"`
	PermissionGranted     bool   `json:"permission_granted"`
	DomainVerified        bool   `json:"domain_verified"`
	Domain                string `json:"domain"`
	PublisherID           string `json:"publisher_id"`
	PublisherName         string `json:"publisher_name"`
}
type Verifier interface {
	VerifyDomain(context.Context, string, string) (bool, error)
}
type Previewer interface {
	Preview(context.Context, string) (reader.Link, error)
}
type Repository interface {
	List(context.Context, string, string) ([]Domain, error)
	Add(context.Context, string, string, string) (Domain, error)
	Recheck(context.Context, string, string, string) (Domain, error)
	Remove(context.Context, string, string, string) error
}
type Auth interface {
	Authenticate(*http.Request) (string, error)
}

func NormalizeDomain(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" || len(s) > 253 || strings.ContainsAny(s, "/:?#@\\") {
		return "", ErrInvalid
	}
	p, e := reader.NewPolicy([]string{"https://" + s})
	if e != nil {
		return "", ErrInvalid
	}
	if _, e = p.Normalize("https://" + s + "/"); e != nil {
		return "", ErrInvalid
	}
	return s, nil
}
func NormalizeURL(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	u, e := url.Parse(raw)
	if e != nil || u == nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil || u.Opaque != "" {
		return "", "", ErrURL
	}
	domain, e := NormalizeDomain(u.Hostname())
	if e != nil {
		return "", "", ErrURL
	}
	p, e := reader.NewPolicy([]string{"https://" + domain})
	if e != nil {
		return "", "", ErrURL
	}
	canonical, e := p.Normalize(raw)
	if e != nil {
		return "", "", ErrURL
	}
	return canonical, domain, nil
}
func MatchesPublisher(body []byte, name string) bool {
	if len(body) > 65536 || !utf8.Valid(body) || name == "" {
		return false
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.EqualFold(strings.TrimSpace(line), name) {
			return true
		}
	}
	return false
}
