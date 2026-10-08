// SPDX-License-Identifier: AGPL-3.0-only
package livestreams

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/messager/calls"
	"solsynth.local/go-migration/ports/passport/accounts"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
)

// Values come from the licensed authored Sphere/Models/LiveStream.cs.
const (
	Pending = 0
	Active  = 1
	Ended   = 2
	Failed  = 3
)
const (
	Regular     = 0
	Interactive = 1
)
const (
	Public   = 0
	Unlisted = 1
	Private  = 2
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (e *Failure) Error() string { return e.Code }

var (
	ErrInvalid      = &Failure{"LIVE_STREAM_INVALID", "Invalid live stream request.", 400}
	ErrUnauthorized = &Failure{"UNAUTHORIZED", "Authentication is required.", 401}
	ErrForbidden    = &Failure{"LIVE_STREAM_FORBIDDEN", "Current publisher authority is required.", 403}
	ErrNotFound     = &Failure{"LIVE_STREAM_NOT_FOUND", "Live stream not found.", 404}
	ErrConflict     = &Failure{"LIVE_STREAM_CONFLICT", "The transition conflicts with current state or is still pending.", 409}
	ErrUnsupported  = &Failure{"LIVE_STREAM_UNSUPPORTED", "This live stream capability is not configured in this implementation.", 501}
	ErrUnavailable  = &Failure{"LIVE_STREAM_UNAVAILABLE", "Live stream dependency unavailable.", 503}
	ErrUnconfigured = &Failure{"LIVE_STREAM_PROVIDER_UNCONFIGURED", "A supported provider with strict revocation must be configured.", 503}
	ErrProvider     = &Failure{"LIVE_STREAM_PROVIDER_UNAVAILABLE", "Provider completion has not been confirmed.", 503}
)

type Stream struct {
	ID                     string              `json:"id"`
	ResourceIdentifier     string              `json:"resource_identifier"`
	Title                  *string             `json:"title"`
	Description            *string             `json:"description"`
	Slug                   *string             `json:"slug"`
	Type                   int                 `json:"type"`
	Visibility             int                 `json:"visibility"`
	Status                 int                 `json:"status"`
	RoomName               string              `json:"room_name"`
	IngressID              *string             `json:"ingress_id"`
	IngressStreamKey       *string             `json:"ingress_stream_key"`
	EgressID               *string             `json:"egress_id"`
	HLSEgressID            *string             `json:"hls_egress_id"`
	HLSPlaylistPath        *string             `json:"hls_playlist_path"`
	HLSStartedAt           *time.Time          `json:"hls_started_at"`
	StartedAt              *time.Time          `json:"started_at"`
	EndedAt                *time.Time          `json:"ended_at"`
	TotalDurationSeconds   int64               `json:"total_duration_seconds"`
	ViewerCount            int                 `json:"viewer_count"`
	PeakViewerCount        int                 `json:"peak_viewer_count"`
	TotalAwardScore        wallet.Amount       `json:"total_award_score"`
	DistributedAwardAmount wallet.Amount       `json:"distributed_award_amount"`
	Thumbnail              *media.File         `json:"thumbnail"`
	Metadata               map[string]any      `json:"metadata"`
	PublisherID            string              `json:"publisher_id"`
	Publisher              *accounts.Publisher `json:"publisher"`
	CreatedAt              time.Time           `json:"created_at"`
	UpdatedAt              time.Time           `json:"updated_at"`
	DeletedAt              *time.Time          `json:"deleted_at"`
	OperationState         string              `json:"operation_state"`
	Generation             int64               `json:"-"`
	ThumbnailID            *string             `json:"-"`
}
type Patch struct {
	Title       *string        `json:"title"`
	Description *string        `json:"description"`
	Slug        *string        `json:"slug"`
	ThumbnailID *string        `json:"thumbnail_id"`
	Type        *int           `json:"type"`
	Visibility  *int           `json:"visibility"`
	Metadata    map[string]any `json:"metadata"`
}
type StartRequest struct {
	ParticipantName   *string `json:"participant_name"`
	NoIngress         *bool   `json:"no_ingress"`
	UseWHIP           *bool   `json:"use_whip"`
	EnableTranscoding *bool   `json:"enable_transcoding"`
}
type StartResponse struct {
	URL       string  `json:"url"`
	StreamKey *string `json:"stream_key,omitempty"`
	RoomName  string  `json:"room_name"`
}
type TokenResponse struct {
	Token      string    `json:"token"`
	RoomName   string    `json:"room_name"`
	URL        string    `json:"url"`
	IsStreamer bool      `json:"is_streamer"`
	Identity   string    `json:"identity"`
	ExpiresAt  time.Time `json:"expires_at"`
}
type Details struct {
	ParticipantCount int `json:"participant_count"`
	ViewerCount      int `json:"viewer_count"`
	PeakViewerCount  int `json:"peak_viewer_count"`
}
type IngressRequest struct {
	Room, Identity, Name, ParticipantName string
	WHIP, Transcoding                     bool
}
type Ingress struct {
	ID        string `json:"ingress_id"`
	Name      string `json:"name"`
	Room      string `json:"room_name"`
	Identity  string `json:"participant_identity"`
	URL       string `json:"url"`
	Key       string `json:"stream_key"`
	InputType int    `json:"input_type"`
}
type Grant struct {
	Room, Identity      string
	Publish             bool
	IssuedAt, ExpiresAt time.Time
}

// CreateIngress is deliberately not declared idempotent. Callers durably record
// an attempt before issuing it and reconcile uncertain results with ListIngress.
type Provider interface {
	Name() string
	Endpoint() string
	StrictRevocation() bool
	EnsureRoom(context.Context, string) error
	DeleteRoom(context.Context, string) error
	StreamParticipants(context.Context, string) ([]calls.Participant, error)
	Remove(context.Context, string, string, time.Time) error
	CreateIngress(context.Context, IngressRequest) (Ingress, error)
	ListIngress(context.Context, string) ([]Ingress, error)
	DeleteIngress(context.Context, string) error
	StreamToken(context.Context, Grant) (string, error)
}
type PublisherProvider interface {
	ObservePostPublisherID(context.Context, *sql.Tx, string, string) (string, error)
	RequirePostEditor(context.Context, *sql.Tx, string, string) (accounts.Publisher, error)
	VisiblePublisherInTransaction(context.Context, *sql.Tx, string, string) (accounts.Publisher, error)
	CanEditPublisherInTransaction(context.Context, *sql.Tx, string, string) (bool, error)
}
type MediaProvider interface {
	ResolvePublicInTransaction(context.Context, *sql.Tx, string, string) (media.File, error)
}
type SocialProvider interface {
	PublisherVisibilitySQL(string, string) string
	PublisherMembershipSQL(string, string) string
	IsBlockedInTransaction(context.Context, *sql.Tx, string, string) (bool, error)
}
type Authority interface {
	AuthenticateRequest(*http.Request) (authority.Principal, error)
	AuthenticatePrincipal(context.Context, authority.Principal) (authority.Principal, error)
	AuthorizePrincipal(context.Context, authority.Principal, ...string) (authority.Principal, error)
}
type Repository interface {
	Create(context.Context, string, string, string, Patch) (Stream, error)
	Get(context.Context, string, string) (Stream, error)
	List(context.Context, string, string, int, int) ([]Stream, error)
	Update(context.Context, string, string, Patch) (Stream, error)
	Thumbnail(context.Context, string, string, *string) (Stream, error)
	Start(context.Context, string, string, StartRequest) (StartResponse, error)
	Token(context.Context, string, string, bool, bool) (TokenResponse, error)
	Details(context.Context, string, string) (Details, error)
	End(context.Context, string, string, bool) error
}
