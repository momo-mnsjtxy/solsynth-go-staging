// SPDX-License-Identifier: AGPL-3.0-only
package stickers

import (
	"context"
	"net/http"
	"regexp"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/taxonomy"
	"strings"
	"time"
	"unicode/utf8"
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (f *Failure) Error() string { return f.Code }

var (
	ErrInvalid         = &Failure{"STICKER_INVALID_REQUEST", "Invalid sticker request.", 400}
	ErrNotFound        = &Failure{"STICKER_NOT_FOUND", "Sticker or pack not found.", 404}
	ErrForbidden       = &Failure{"STICKER_FORBIDDEN", "Publisher editor access is required.", 403}
	ErrUnauthenticated = &Failure{"UNAUTHORIZED", "Authentication is required.", 401}
	ErrConflict        = &Failure{"STICKER_CONFLICT", "Sticker identifier already exists.", 409}
	ErrCapacity        = &Failure{"STICKER_PACK_CAPACITY_REACHED", "A pack can contain at most 24 stickers.", 409}
	ErrUnavailable     = &Failure{"STICKERS_UNAVAILABLE", "Sticker dependency unavailable.", 503}
	ErrUnsupported     = &Failure{"STICKER_FEATURE_UNSUPPORTED", "Requested feature unavailable.", 501}
)

const MaxStickers = 24

type Pack struct {
	ID          string             `json:"id"`
	PublisherID string             `json:"publisher_id"`
	Publisher   accounts.Publisher `json:"publisher"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Prefix      string             `json:"prefix"`
	Icon        *media.File        `json:"icon"`
	Stickers    []Sticker          `json:"stickers"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
}
type Sticker struct {
	ID         string     `json:"id"`
	PackID     string     `json:"pack_id"`
	Slug       string     `json:"slug"`
	Name       string     `json:"name"`
	Size       int        `json:"size"`
	Mode       int        `json:"mode"`
	Order      int        `json:"order"`
	Image      media.File `json:"image"`
	Identifier string     `json:"identifier"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}
type Ownership struct {
	ID        string    `json:"id"`
	AccountID string    `json:"account_id"`
	PackID    string    `json:"pack_id"`
	Order     int       `json:"order"`
	Pack      *Pack     `json:"pack,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}
type PackPatch struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Prefix      *string `json:"prefix"`
	IconID      *string `json:"icon_id"`
}
type StickerPatch struct {
	Slug    *string `json:"slug"`
	Name    *string `json:"name"`
	ImageID *string `json:"image_id"`
	Size    *int    `json:"size"`
	Mode    *int    `json:"mode"`
}
type Rendering struct {
	StickerIDs []string `json:"sticker_ids"`
	Size       *int     `json:"size"`
	Mode       *int     `json:"mode"`
}
type OrderItem struct {
	ID    string `json:"id"`
	Order int    `json:"order"`
}
type LookupItem struct {
	Placeholder string   `json:"placeholder"`
	Sticker     *Sticker `json:"sticker"`
}
type Page struct {
	Take, Offset     int
	Publisher, Query string
}
type Repository interface {
	CreatePack(context.Context, string, string, PackPatch) (Pack, error)
	UpdatePack(context.Context, string, string, PackPatch) (Pack, error)
	DeletePack(context.Context, string, string) error
	GetPack(context.Context, string, string) (Pack, error)
	List(context.Context, string, Page) ([]Pack, int64, error)
	CreateSticker(context.Context, string, string, StickerPatch) (Sticker, error)
	UpdateSticker(context.Context, string, string, string, StickerPatch) (Sticker, error)
	DeleteSticker(context.Context, string, string, string) error
	Content(context.Context, string, string) ([]Sticker, error)
	GetSticker(context.Context, string, string, string) (Sticker, error)
	Lookup(context.Context, string, string) (Sticker, error)
	Search(context.Context, string, Page) ([]Sticker, int64, error)
	Rendering(context.Context, string, string, Rendering) ([]Sticker, error)
	Reorder(context.Context, string, string, []OrderItem) error
	Own(context.Context, string, string) (Ownership, error)
	Ownership(context.Context, string, string) (Ownership, error)
	Release(context.Context, string, string) error
	Owned(context.Context, string) ([]Ownership, error)
}
type Auth interface {
	Authenticate(*http.Request) (string, error)
	Authorize(*http.Request, string) error
}

var alpha = regexp.MustCompile(`^[a-zA-Z0-9]{1,128}$`)

func text(s string, n int) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0) && taxonomy.UTF16Length(s) <= n
}
func ValidatePack(p Pack) error {
	if !alpha.MatchString(p.Prefix) || !text(p.Name, 1024) || strings.TrimSpace(p.Name) == "" || !text(p.Description, 4096) {
		return ErrInvalid
	}
	return nil
}
func ValidateSticker(s Sticker) error {
	if !alpha.MatchString(s.Slug) || !text(s.Name, 1024) || s.Size < 0 || s.Size > 3 || s.Mode < 0 || s.Mode > 1 || !authority.ValidID(s.Image.ID) {
		return ErrInvalid
	}
	return nil
}
func ValidateOrder(v []OrderItem) error {
	if len(v) == 0 || len(v) > 100 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, x := range v {
		if !authority.ValidID(x.ID) || seen[x.ID] || x.Order < 0 || x.Order > 100000 {
			return ErrInvalid
		}
		seen[x.ID] = true
	}
	return nil
}
