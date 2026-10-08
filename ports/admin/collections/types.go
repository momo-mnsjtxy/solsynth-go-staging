// SPDX-License-Identifier: AGPL-3.0-only
package collections

import (
	"context"
	"net/http"
	"solsynth.local/go-migration/ports/passport/accounts"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (f *Failure) Error() string { return f.Code }

var (
	ErrInvalid     = &Failure{"COLLECTION_ADMIN_INVALID", "Invalid collection administration request.", 400}
	ErrNotFound    = &Failure{"COLLECTION_NOT_FOUND", "Collection not found.", 404}
	ErrUnavailable = &Failure{"COLLECTION_ADMIN_UNAVAILABLE", "Collection administration is unavailable.", 503}
	ErrConflict    = &Failure{"COLLECTION_ADMIN_CONFLICT", "Collection state changed; retry the request.", 409}
)

type Collection struct {
	ID          string             `json:"id"`
	PublisherID string             `json:"publisher_id"`
	Publisher   accounts.Publisher `json:"publisher"`
	Slug        string             `json:"slug"`
	Name        *string            `json:"name"`
	Description *string            `json:"description"`
	Background  any                `json:"background"`
	Icon        any                `json:"icon"`
	ItemCount   int                `json:"item_count"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
	DeletedAt   *time.Time         `json:"deleted_at"`
}
type Patch struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

func (p Patch) Validate() error {
	for _, v := range []struct {
		s *string
		n int
	}{{p.Name, 256}, {p.Description, 4096}} {
		if v.s != nil && (!utf8.ValidString(*v.s) || strings.ContainsRune(*v.s, 0) || len(utf16.Encode([]rune(*v.s))) > v.n) {
			return ErrInvalid
		}
	}
	return nil
}

type Page struct {
	Query, PublisherID string
	Offset, Take       int
}

func (p Page) Bounds() Page {
	p.Query = strings.TrimSpace(p.Query)
	if p.Offset < 0 {
		p.Offset = 0
	}
	if p.Offset > 100000 {
		p.Offset = 100000
	}
	if p.Take <= 0 {
		p.Take = 50
	}
	if p.Take > 200 {
		p.Take = 200
	}
	return p
}

type Repository interface {
	List(context.Context, string, Page) ([]Collection, int, error)
	Get(context.Context, string, string) (Collection, error)
	Update(context.Context, string, string, Patch) (Collection, error)
	Delete(context.Context, string, string) error
}
type Auth func(*http.Request, bool) (string, error)
