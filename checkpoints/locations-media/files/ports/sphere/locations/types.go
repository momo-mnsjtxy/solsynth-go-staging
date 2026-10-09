// SPDX-License-Identifier: AGPL-3.0-only
package locations

import (
	"context"
	"encoding/json"
	"net/http"
	"solsynth.local/go-migration/ports/passport/accounts"
	"time"
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (f *Failure) Error() string { return f.Code }

var (
	ErrInvalid         = &Failure{"LOCATION_INVALID_REQUEST", "Invalid location request.", 400}
	ErrNotFound        = &Failure{"LOCATION_NOT_FOUND", "Location resource not found.", 404}
	ErrForbidden       = &Failure{"LOCATION_FORBIDDEN", "Permission denied.", 403}
	ErrUnauthenticated = &Failure{"UNAUTHORIZED", "Authentication is required.", 401}
	ErrConflict        = &Failure{"LOCATION_INVALID_STATE", "Resource does not accept this change.", 409}
	ErrRateLimit       = &Failure{"LOCATION_RATE_LIMIT", "Wait five seconds between updates.", 429}
	ErrUnavailable     = &Failure{"LOCATIONS_UNAVAILABLE", "Location dependency unavailable.", 503}
	ErrUnsupported     = &Failure{"LOCATION_FEATURE_UNSUPPORTED", "Requested provider or hardware operation is unavailable.", 501}
)

// These numeric values are explicit in authored Solian parsers and API docs.
const (
	Public     = 0
	Private    = 1
	Unlisted   = 2
	Active     = 0
	Completed  = 1
	Expired    = 2
	Cancelled  = 3
	PinActive  = 0
	PinOffline = 1
	PinRemoved = 2
)

type Point struct {
	Longitude float64
	Latitude  float64
}
type Participant struct {
	ID        string           `json:"id"`
	MeetID    string           `json:"meet_id"`
	AccountID string           `json:"account_id"`
	Account   accounts.Account `json:"account"`
	JoinedAt  time.Time        `json:"joined_at"`
}
type Meet struct {
	ID              string                     `json:"id"`
	HostID          string                     `json:"host_id"`
	Host            accounts.Account           `json:"host"`
	Status          int                        `json:"status"`
	Visibility      int                        `json:"visibility"`
	Notes           *string                    `json:"notes"`
	LocationName    *string                    `json:"location_name"`
	LocationAddress *string                    `json:"location_address"`
	LocationWKT     *string                    `json:"location_wkt"`
	Metadata        map[string]json.RawMessage `json:"metadata"`
	ExpiresAt       time.Time                  `json:"expires_at"`
	CompletedAt     *time.Time                 `json:"completed_at"`
	CreatedAt       time.Time                  `json:"created_at"`
	UpdatedAt       time.Time                  `json:"updated_at"`
	Participants    []Participant              `json:"participants"`
	Pins            []Pin                      `json:"pins"`
	Image           any                        `json:"image"`
	ImageID         *string                    `json:"-"`
}
type CreateMeet struct {
	Visibility       *int                       `json:"visibility"`
	Notes            *string                    `json:"notes"`
	ImageID          *string                    `json:"image_id"`
	LocationName     *string                    `json:"location_name"`
	LocationAddress  *string                    `json:"location_address"`
	LocationWKT      *string                    `json:"location_wkt"`
	Metadata         map[string]json.RawMessage `json:"metadata"`
	ExpiresInSeconds *int                       `json:"expires_in_seconds"`
}
type Pin struct {
	ID               string                     `json:"id"`
	AccountID        string                     `json:"account_id"`
	Account          accounts.Account           `json:"account"`
	MeetID           *string                    `json:"meet_id"`
	DeviceID         string                     `json:"device_id,omitempty"`
	Visibility       int                        `json:"visibility"`
	Status           int                        `json:"status"`
	LocationName     *string                    `json:"location_name"`
	LocationAddress  *string                    `json:"location_address"`
	LocationWKT      *string                    `json:"location_wkt"`
	Metadata         map[string]json.RawMessage `json:"metadata"`
	KeepOnDisconnect bool                       `json:"keep_on_disconnect"`
	LastHeartbeatAt  time.Time                  `json:"last_heartbeat_at"`
	ExpiresAt        *time.Time                 `json:"expires_at"`
	CreatedAt        time.Time                  `json:"created_at"`
	UpdatedAt        time.Time                  `json:"updated_at"`
}
type PinRequest struct {
	Visibility       *int                       `json:"visibility"`
	LocationName     *string                    `json:"location_name"`
	LocationAddress  *string                    `json:"location_address"`
	LocationWKT      *string                    `json:"location_wkt"`
	Metadata         map[string]json.RawMessage `json:"metadata"`
	KeepOnDisconnect *bool                      `json:"keep_on_disconnect"`
}
type Tag struct {
	ID         string     `json:"id"`
	UID        string     `json:"uid"`
	Label      *string    `json:"label"`
	Active     bool       `json:"is_active"`
	Locked     bool       `json:"is_locked"`
	Encrypted  bool       `json:"is_encrypted"`
	LastSeenAt *time.Time `json:"last_seen_at"`
	CreatedAt  time.Time  `json:"created_at"`
}
type RegisterTag struct {
	UID   string  `json:"uid"`
	Label *string `json:"label"`
}
type TagPatch struct {
	Label  *string `json:"label"`
	Active *bool   `json:"is_active"`
}
type TagResolution struct {
	ID      string           `json:"id"`
	Account accounts.Account `json:"account"`
	Friend  bool             `json:"is_friend"`
	Claimed bool             `json:"is_claimed"`
	Actions []string         `json:"actions"`
}
type Page struct {
	Take, Offset int
	Status       *int
	Visibility   *int
	HostOnly     bool
	Origin       *Point
	Radius       float64
	Nearby       bool
}
type Repository interface {
	CreateMeet(context.Context, string, CreateMeet) (Meet, error)
	Meet(context.Context, string, string, *Point) (Meet, error)
	Meets(context.Context, string, Page) ([]Meet, int64, error)
	Join(context.Context, string, string, *Point) (Meet, error)
	Complete(context.Context, string, string) (Meet, error)
	Visibility(context.Context, string, string, int) (Meet, error)
	DeleteMeet(context.Context, string, string) error
	PutPin(context.Context, string, string, string, PinRequest) (Pin, error)
	Pin(context.Context, string, string) (Pin, error)
	Pins(context.Context, string, string, Page) ([]Pin, int64, error)
	Disconnect(context.Context, string, string, bool) error
	DeletePin(context.Context, string, string) error
	DeleteMeetPin(context.Context, string, string) error
	RegisterTag(context.Context, string, RegisterTag) (Tag, error)
	Tags(context.Context, string) ([]Tag, error)
	UpdateTag(context.Context, string, string, TagPatch) (Tag, error)
	DeleteTag(context.Context, string, string) error
	ResolveTag(context.Context, string, string, bool) (TagResolution, error)
}
type Auth interface {
	Authenticate(*http.Request) (string, error)
	Authorize(*http.Request, string) error
}
