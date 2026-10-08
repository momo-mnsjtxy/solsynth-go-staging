// SPDX-License-Identifier: AGPL-3.0-only
// Source-informed request shapes from licensed DysonNetwork Develop controllers;
// independently authored identity, visibility and persistence policy.
package apps

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"time"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop"
	"solsynth.local/go-migration/ports/passport/board"
)

const BoardScope = "accounts.profile.board"
const OAuthClientAuthScope = "oauth.client.authenticate"

// Client identifies an authenticated app, never an account principal.
type Client struct {
	AppID       string
	PublisherID string
}

func SupportedClientScope(scope string) bool {
	switch scope {
	case BoardScope, "orders.create", "orders.status.manage", "orders.metrics.read", OAuthClientAuthScope:
		return true
	}
	return false
}

const (
	Developing = 0
	Production = 1
	Suspended  = 2
)

var (
	ErrNotFound    = errors.New("app or resource not found")
	ErrForbidden   = errors.New("app access denied")
	ErrInvalid     = errors.New("invalid app request")
	ErrConflict    = errors.New("app resource already exists")
	ErrUnavailable = errors.New("app storage unavailable")
	ErrProvider    = errors.New("requested app provider is not configured")
)

type Developer struct {
	ID          string    `json:"id"`
	PublisherID string    `json:"publisher_id"`
	Name        string    `json:"name"`
	CreatedAt   time.Time `json:"created_at"`
}
type Project struct {
	ID          string    `json:"id"`
	DeveloperID string    `json:"developer_id"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Description *string   `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
type ProjectPatch struct {
	Slug        *string `json:"slug"`
	Name        *string `json:"name"`
	Description *string `json:"description"`
}
type OAuthConfig struct {
	ClientURI              *string  `json:"client_uri,omitempty"`
	RedirectURIs           []string `json:"redirect_uris"`
	PostLogoutRedirectURIs []string `json:"post_logout_redirect_uris,omitempty"`
	AllowedScopes          []string `json:"allowed_scopes"`
	AllowedGrantTypes      []string `json:"allowed_grant_types"`
	RequirePKCE            bool     `json:"require_pkce"`
	AllowOfflineAccess     bool     `json:"allow_offline_access"`
	IsPublicClient         bool     `json:"is_public_client"`
}
type Links struct {
	HomePage       *string `json:"home_page"`
	PrivacyPolicy  *string `json:"privacy_policy"`
	TermsOfService *string `json:"terms_of_service"`
}
type App struct {
	ID          string       `json:"id"`
	ProjectID   string       `json:"project_id"`
	PublisherID string       `json:"publisher_id"`
	Slug        string       `json:"slug"`
	Name        string       `json:"name"`
	Description *string      `json:"description"`
	Status      int          `json:"status"`
	Links       *Links       `json:"links"`
	OAuthConfig *OAuthConfig `json:"oauth_config"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}
type AppPatch struct {
	Slug         *string           `json:"slug"`
	Name         *string           `json:"name"`
	Description  *string           `json:"description"`
	Status       *int              `json:"status"`
	Links        *Links            `json:"links"`
	OAuthConfig  *OAuthConfig      `json:"oauth_config"`
	BoardWidgets *[]board.Manifest `json:"board_widgets"`
	PictureID    *string           `json:"picture_id"`
	BackgroundID *string           `json:"background_id"`
}
type ProductState struct {
	IsEnabled     bool   `json:"is_enabled"`
	StockMode     string `json:"stock_mode"`
	StockQuantity *int64 `json:"stock_quantity"`
}
type Fulfillment struct {
	IsAddressRequired bool     `json:"is_address_required"`
	RequiredScopes    []string `json:"required_scopes"`
}
type Product struct {
	ID              string       `json:"id"`
	AppID           string       `json:"app_id"`
	Identifier      string       `json:"identifier"`
	DisplayName     string       `json:"display_name"`
	Description     *string      `json:"description"`
	Currency        string       `json:"currency"`
	Price           json.Number  `json:"price"`
	Recurrence      string       `json:"recurrence"`
	GroupIdentifier *string      `json:"group_identifier"`
	Fulfillment     *Fulfillment `json:"fulfillment"`
	State           ProductState `json:"state"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
}
type ProductStatePatch struct {
	IsEnabled     *bool   `json:"is_enabled"`
	StockMode     *string `json:"stock_mode"`
	StockQuantity *int64  `json:"stock_quantity"`
}
type ProductPatch struct {
	Identifier      *string            `json:"identifier"`
	DisplayName     *string            `json:"display_name"`
	Description     *string            `json:"description"`
	Currency        *string            `json:"currency"`
	Price           *json.Number       `json:"price"`
	Recurrence      *string            `json:"recurrence"`
	GroupIdentifier *string            `json:"group_identifier"`
	Fulfillment     *Fulfillment       `json:"fulfillment"`
	State           *ProductStatePatch `json:"state"`
	PictureID       *string            `json:"picture_id"`
	BackgroundID    *string            `json:"background_id"`
}
type Secret struct {
	ID          string    `json:"id"`
	Secret      *string   `json:"secret"`
	Description *string   `json:"description"`
	ExpiresAt   time.Time `json:"expires_at"`
	Type        int       `json:"type"`
	Scopes      []string  `json:"scopes"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
type SecretRequest struct {
	Description *string  `json:"description"`
	ExpiresIn   *string  `json:"expires_in"`
	Type        int      `json:"type"`
	Scopes      []string `json:"scopes"`
}
type Consent struct {
	AppID     string    `json:"app_id"`
	Scopes    []string  `json:"scopes"`
	ExpiresAt time.Time `json:"expires_at"`
}
type ConsentRequest struct {
	Scopes    []string `json:"scopes"`
	ExpiresIn *string  `json:"expires_in"`
}
type Scope struct {
	Actor     string
	Developer string
	Project   string
	App       string
}
type PayloadRequest struct {
	AccountID   string        `json:"account_id"`
	BoardItemID *string       `json:"board_item_id"`
	WidgetKey   string        `json:"widget_key"`
	Payload     board.Payload `json:"payload"`
}

type Repository interface {
	develop.Reader
	Developers(context.Context, string, string, bool) ([]Developer, error)
	Projects(context.Context, Scope, string, *ProjectPatch, string) ([]Project, error)
	Apps(context.Context, Scope, *AppPatch, string) ([]App, error)
	PublicApp(context.Context, string) (App, error)
	Products(context.Context, Scope, string, *ProductPatch, string) ([]Product, error)
	PublicProducts(context.Context, string, string) ([]Product, error)
	Widgets(context.Context, Scope, string, *board.Manifest, string) ([]board.Manifest, error)
	Secrets(context.Context, Scope, string, *SecretRequest, string) ([]Secret, error)
	Consent(context.Context, string, string, *ConsentRequest, bool) (Consent, error)
	AppWidgets(context.Context, string, string) ([]board.Manifest, error)
	PushPayload(context.Context, string, string, PayloadRequest) (board.Item, error)
}
type Authority interface {
	Require(context.Context, ...string) (authority.Principal, error)
}
type Service struct {
	Repository Repository
	Authority  Authority
}

var keyPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

func ValidKey(v string) bool { return keyPattern.MatchString(v) }
func ValidText(v *string, max int, required bool) bool {
	return v == nil && !required || v != nil && len(*v) <= max && (!required || strings.TrimSpace(*v) != "")
}
func ValidateProject(p ProjectPatch, create bool) error {
	if !ValidText(p.Name, 1024, create) || p.Name != nil && strings.TrimSpace(*p.Name) == "" || !ValidText(p.Slug, 128, create) || p.Slug != nil && !ValidKey(*p.Slug) || !ValidText(p.Description, 4096, false) {
		return ErrInvalid
	}
	return nil
}
func ValidateApp(p AppPatch, create bool) error {
	if err := ValidateProject(ProjectPatch{p.Slug, p.Name, p.Description}, create); err != nil {
		return err
	}
	if p.Status != nil && (*p.Status < 0 || *p.Status > 2) {
		return ErrInvalid
	}
	if p.PictureID != nil || p.BackgroundID != nil {
		return ErrProvider
	}
	if p.OAuthConfig != nil {
		if len(p.OAuthConfig.AllowedScopes) > 32 {
			return ErrInvalid
		}
		for _, s := range p.OAuthConfig.AllowedScopes {
			if !SupportedClientScope(s) && !authority.OAuthScope(s) {
				return ErrInvalid
			}
		}
	}
	if p.BoardWidgets != nil {
		seen := map[string]bool{}
		if len(*p.BoardWidgets) > 32 {
			return ErrInvalid
		}
		for _, w := range *p.BoardWidgets {
			if ValidateWidget(w) != nil || seen[strings.ToLower(w.Key)] {
				return ErrInvalid
			}
			seen[strings.ToLower(w.Key)] = true
		}
	}
	return nil
}
func ValidateWidget(w board.Manifest) error {
	if !ValidKey(w.Key) || strings.EqualFold(w.Key, "widgets") || strings.EqualFold(w.Key, "payload") || strings.TrimSpace(w.Name) == "" || len(w.Name) > 1024 || len(w.Description) > 4096 || w.PayloadType != "object" || w.RendererType != "data" && w.RendererType != "" || w.MaxPayloadBytes != nil && (*w.MaxPayloadBytes < 1 || *w.MaxPayloadBytes > 65536) || len(w.FieldTypes) > 64 || len(w.RequiredFields) > 64 {
		return ErrInvalid
	}
	for _, f := range w.FieldTypes {
		if !ValidKey(f.Name) {
			return ErrInvalid
		}
		switch f.Type {
		case "string", "number", "boolean", "object", "array", "null":
		default:
			return ErrInvalid
		}
	}
	for _, f := range w.RequiredFields {
		if !ValidKey(f) {
			return ErrInvalid
		}
	}
	return nil
}
func ValidateProduct(p ProductPatch, create bool) error {
	if p.DisplayName != nil && strings.TrimSpace(*p.DisplayName) == "" || p.Currency != nil && strings.TrimSpace(*p.Currency) == "" || !ValidText(p.Identifier, 128, create) || p.Identifier != nil && !ValidKey(*p.Identifier) || !ValidText(p.DisplayName, 1024, create) || !ValidText(p.Currency, 128, create) || !ValidText(p.Description, 4096, false) {
		return ErrInvalid
	}
	if p.PictureID != nil || p.BackgroundID != nil {
		return ErrProvider
	}
	if p.Recurrence != nil && *p.Recurrence != "none" && *p.Recurrence != "weekly" && *p.Recurrence != "monthly" && *p.Recurrence != "yearly" {
		return ErrProvider
	}
	if p.Price != nil {
		s := p.Price.String()
		n, ok := new(big.Rat).SetString(s)
		if !ok || n.Sign() < 0 || len(s) > 40 || strings.ContainsAny(s, "eE/") {
			return ErrInvalid
		}
	}
	if p.State != nil {
		if p.State.StockQuantity != nil && *p.State.StockQuantity < 0 {
			return ErrInvalid
		}
		if p.State.StockMode != nil && *p.State.StockMode != "unlimited" && *p.State.StockMode != "manual" {
			return ErrProvider
		}
	}
	return nil
}
func Duration(raw *string, def, max time.Duration) (time.Duration, error) {
	if raw == nil {
		return def, nil
	}
	s := *raw
	d, e := time.ParseDuration(s)
	if e != nil {
		var days int
		parts := strings.Split(s, ":")
		if len(parts) != 3 {
			return 0, ErrInvalid
		}
		h := parts[0]
		if i := strings.Index(h, "."); i >= 0 {
			n, ok := new(big.Int).SetString(h[:i], 10)
			if !ok || !n.IsInt64() || n.Int64() < 0 || n.Int64() > 30 {
				return 0, ErrInvalid
			}
			days = int(n.Int64())
			h = h[i+1:]
		}
		d, e = time.ParseDuration(h + "h" + parts[1] + "m" + parts[2] + "s")
		d += time.Duration(days) * 24 * time.Hour
	}
	if e != nil || d <= 0 || d > max {
		return 0, ErrInvalid
	}
	return d, nil
}
func IsAppCredentialRoute(r *http.Request) bool {
	p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	return len(p) == 6 && p[0] == "api" && p[1] == "private" && p[2] == "apps" && authority.ValidID(p[3]) && p[4] == "board" && (p[5] == "widgets" && r.Method == http.MethodGet || p[5] == "payload" && r.Method == http.MethodPost)
}
