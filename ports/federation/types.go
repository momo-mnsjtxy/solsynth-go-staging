// SPDX-License-Identifier: AGPL-3.0-only
// Package federation implements an explicitly provisioned ActivityPub S2S subset.
// It never fetches actors or keys, provisions credentials, or starts workers.
package federation

import (
	"context"
	"crypto"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	"time"
)

const Context = "https://www.w3.org/ns/activitystreams"
const Public = Context + "#Public"
const MediaType = "application/activity+json"
const Permission = "publishers.update"
const MaxBody = 1 << 20

type Failure struct {
	Code   string `json:"code"`
	Status int    `json:"status"`
}

func (e *Failure) Error() string { return e.Code }

var (
	ErrInvalid     = &Failure{"FEDERATION_INVALID", 400}
	ErrSignature   = &Failure{"FEDERATION_SIGNATURE_INVALID", 401}
	ErrForbidden   = &Failure{"FEDERATION_FORBIDDEN", 403}
	ErrNotFound    = &Failure{"FEDERATION_NOT_FOUND", 404}
	ErrReplay      = &Failure{"FEDERATION_ACTIVITY_CONFLICT", 409}
	ErrUnsupported = &Failure{"FEDERATION_ACTIVITY_UNSUPPORTED", 422}
	ErrUnavailable = &Failure{"FEDERATION_UNCONFIGURED", 503}
)

type PublicKey struct {
	ID    string `json:"id"`
	Owner string `json:"owner"`
	PEM   string `json:"publicKeyPem"`
}
type Actor struct {
	Context   any       `json:"@context"`
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Username  string    `json:"preferredUsername"`
	Name      string    `json:"name"`
	Summary   string    `json:"summary"`
	Inbox     string    `json:"inbox"`
	Outbox    string    `json:"outbox"`
	Followers string    `json:"followers"`
	Following string    `json:"following"`
	Published time.Time `json:"published"`
	PublicKey PublicKey `json:"publicKey"`
	Manual    bool      `json:"manuallyApprovesFollowers"`
}
type Note struct {
	Context      string    `json:"@context,omitempty"`
	ID           string    `json:"id"`
	Type         string    `json:"type"`
	AttributedTo string    `json:"attributedTo"`
	Content      string    `json:"content,omitempty"`
	MediaType    string    `json:"mediaType,omitempty"`
	To           []string  `json:"to,omitempty"`
	CC           []string  `json:"cc,omitempty"`
	Published    time.Time `json:"published,omitzero"`
	Updated      time.Time `json:"updated,omitzero"`
}
type Activity struct {
	Context   any             `json:"@context,omitempty"`
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Actor     string          `json:"actor"`
	Object    json.RawMessage `json:"object"`
	To        []string        `json:"to,omitempty"`
	CC        []string        `json:"cc,omitempty"`
	Published time.Time       `json:"published,omitzero"`
}
type Collection struct {
	Context string `json:"@context"`
	ID      string `json:"id"`
	Type    string `json:"type"`
	PartOf  string `json:"partOf,omitempty"`
	Total   int64  `json:"totalItems"`
	First   string `json:"first,omitempty"`
	Next    string `json:"next,omitempty"`
	Items   []any  `json:"orderedItems,omitempty"`
}
type Settings struct {
	Enabled bool `json:"enabled"`
	Manual  bool `json:"manually_approves_followers"`
}
type Change struct {
	Action      string `json:"action"`
	RemoteActor string `json:"remote_actor"`
}
type Relationship struct {
	RemoteActor string `json:"remote_actor"`
	Direction   string `json:"direction"`
	Status      string `json:"status"`
	FollowID    string `json:"follow_id"`
}

// RemoteKey is a trusted, preprovisioned binding. Key lookup must not perform
// discovery based on request data. Revocation/rotation belongs to its provider.
type RemoteKey struct {
	Actor     string
	Inbox     string
	KeyID     string
	PublicKey *rsa.PublicKey
}
type KeyProvider interface {
	LocalKey(context.Context, string) (crypto.Signer, error) // canonical publisher ID
	RemoteKey(context.Context, string) (RemoteKey, error)    // exact key ID
	RemoteActor(context.Context, string) (RemoteKey, error)  // exact actor URI
}
type Authority interface {
	AuthenticateRequest(*http.Request) (authority.Principal, error)
	AuthorizePrincipal(context.Context, authority.Principal, ...string) (authority.Principal, error)
}
type Repository interface {
	Actor(context.Context, string) (Actor, error)
	Object(context.Context, string) (Note, error)
	Outbox(context.Context, string, int, int) (Collection, error)
	Collection(context.Context, string, string, int, int) (Collection, error)
	Receive(context.Context, string, Verified) error
	Settings(context.Context, string, string, Settings) error
	Change(context.Context, string, string, Change) error
	Relationships(context.Context, string, string, int, int) ([]Relationship, error)
}

// Transport is injected; default nil disables sending. Implementations must
// honor ctx, send exactly once, and never follow redirects or use ambient proxy
// credentials. NewHTTPTransport provides a destination-pinned implementation.
type Transport interface {
	Send(context.Context, *http.Request) (int, error)
}
type Config struct {
	Origin          string
	AllowedOrigins  []string
	Now             func() time.Time
	MaxAttempts     int
	DeliveryTimeout time.Duration
}

func (c Config) Clock() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}
