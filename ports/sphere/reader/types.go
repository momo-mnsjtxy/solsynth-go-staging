// SPDX-License-Identifier: AGPL-3.0-only
package reader

import (
	"context"
	"net/http"
	"time"
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (e *Failure) Error() string { return e.Code }

var (
	ErrRequired        = &Failure{"SCRAP_URL_REQUIRED", "URL parameter is required.", 400}
	ErrInvalid         = &Failure{"SCRAP_URL_INVALID_FORMAT", "A valid configured HTTPS URL is required.", 400}
	ErrBlocked         = &Failure{"SCRAP_URL_BLOCKED", "The destination is unavailable under the configured reader policy.", 400}
	ErrFetch           = &Failure{"SCRAP_LINK_ERROR", "The remote preview could not be read.", 502}
	ErrLimit           = &Failure{"SCRAP_LIMIT_EXCEEDED", "The preview exceeded a configured limit.", 413}
	ErrBusy            = &Failure{"SCRAP_BUSY", "The preview reader is busy.", 429}
	ErrConflict        = &Failure{"SCRAP_CACHE_CHANGED", "The cache changed while the preview was loading. Retry the request.", 409}
	ErrUnavailable     = &Failure{"SCRAP_UNAVAILABLE", "The preview reader is unavailable.", 503}
	ErrUnauthenticated = &Failure{"UNAUTHORIZED", "Authentication is required.", 401}
)

// Link is the owned JSON projection of properties used by the authored reader
// service and Solian callers; excluded shared/generated models are not copied.
type Link struct {
	URL           string     `json:"url"`
	Title         string     `json:"title"`
	Description   *string    `json:"description"`
	ImageURL      *string    `json:"image_url"`
	SiteName      *string    `json:"site_name"`
	ContentType   *string    `json:"content_type"`
	FaviconURL    *string    `json:"favicon_url"`
	Author        *string    `json:"author"`
	PublishedDate *time.Time `json:"published_date"`
}
type Fetcher interface {
	Fetch(context.Context, string) (Link, error)
}
type Repository interface {
	Preview(context.Context, string) (Link, error)
	Invalidate(context.Context, string, string) error
	InvalidateAll(context.Context, string) error
}
type Auth interface {
	Authenticate(*http.Request) (string, error)
	Authorize(*http.Request, string) error
}
