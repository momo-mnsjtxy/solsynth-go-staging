// SPDX-License-Identifier: AGPL-3.0-only
// Package federation implements an explicitly provisioned ActivityPub S2S subset.
// Construction never provisions credentials or starts workers. Optional explicit
// discovery observes configured actor documents without importing key authority.
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
const CleanupPermission = "federation.cleanup.activate"
const ReadPermission = "federation.read"
const ModerationPermission = "federation.moderation.manage"
const KeyAdminPermission = "federation.keys.manage"
const RealmPermission = "realms.update"
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
	Context      any       `json:"@context"`
	ID           string    `json:"id"`
	Type         string    `json:"type"`
	Username     string    `json:"preferredUsername"`
	Name         string    `json:"name"`
	Summary      string    `json:"summary"`
	Inbox        string    `json:"inbox"`
	Outbox       string    `json:"outbox"`
	Followers    string    `json:"followers"`
	Following    string    `json:"following"`
	Published    time.Time `json:"published"`
	PublicKey    PublicKey `json:"publicKey"`
	Manual       bool      `json:"manuallyApprovesFollowers"`
	URL          string    `json:"url,omitempty"`
	Featured     string    `json:"featured,omitempty"`
	Icon         *Image    `json:"icon,omitempty"`
	Image        *Image    `json:"image,omitempty"`
	Discoverable bool      `json:"discoverable,omitempty"`
}
type Image struct {
	Type      string `json:"type"`
	MediaType string `json:"mediaType,omitempty"`
	URL       string `json:"url"`
}
type Attachment struct {
	Type      string `json:"type"`
	MediaType string `json:"mediaType,omitempty"`
	URL       string `json:"url"`
	Name      string `json:"name,omitempty"`
}
type Note struct {
	Quote              string       `json:"quote,omitempty"`
	QuoteAuthorization string       `json:"quoteAuthorization,omitempty"`
	Context            string       `json:"@context,omitempty"`
	ID                 string       `json:"id"`
	Type               string       `json:"type"`
	AttributedTo       string       `json:"attributedTo"`
	Content            string       `json:"content,omitempty"`
	MediaType          string       `json:"mediaType,omitempty"`
	To                 []string     `json:"to,omitempty"`
	CC                 []string     `json:"cc,omitempty"`
	Published          time.Time    `json:"published,omitzero"`
	Updated            time.Time    `json:"updated,omitzero"`
	Attachments        []Attachment `json:"attachment,omitempty"`
	Sensitive          bool         `json:"sensitive,omitempty"`
	Summary            string       `json:"summary,omitempty"`
}
type Activity struct {
	Instrument json.RawMessage `json:"instrument,omitempty"`
	Result     string          `json:"result,omitempty"`
	Context    any             `json:"@context,omitempty"`
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Actor      string          `json:"actor"`
	Object     json.RawMessage `json:"object"`
	To         []string        `json:"to,omitempty"`
	CC         []string        `json:"cc,omitempty"`
	Published  time.Time       `json:"published,omitzero"`
	Target     json.RawMessage `json:"target,omitempty"`
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

// RemoteProfile is a bounded, observed projection. It never represents a local
// account or grants signature authority; Provenance states how it was observed.
type RemoteProfile struct {
	ID         string    `json:"id"`
	Actor      string    `json:"actor"`
	Handle     string    `json:"handle"`
	Type       string    `json:"type"`
	Name       string    `json:"name"`
	Summary    string    `json:"summary"`
	Inbox      string    `json:"inbox"`
	Provenance string    `json:"provenance"`
	ObservedAt time.Time `json:"observed_at"`
}
type Availability struct {
	Enabled    bool     `json:"enabled"`
	Publishers []string `json:"publishers"`
}
type RelationshipView struct {
	RemoteActor string `json:"remote_actor"`
	Following   bool   `json:"following"`
	Pending     bool   `json:"pending"`
	FollowedBy  bool   `json:"followed_by"`
	Blocked     bool   `json:"blocked"`
}
type ClientRepository interface {
	RemoteProfile(context.Context, string) (RemoteProfile, error)
	RemoteProfileByHandle(context.Context, string) (RemoteProfile, error)
	SearchRemoteProfiles(context.Context, string, int, int) ([]RemoteProfile, error)
	RemotePosts(context.Context, string, string, string, int, int) ([]Note, error)
	Availability(context.Context, string) (Availability, error)
	RelationshipView(context.Context, string, string, string) (RelationshipView, error)
}

type RealmRepository interface {
	RealmActor(context.Context, string) (Actor, error)
	RealmCollection(context.Context, string, string, int, int) (Collection, error)
	ReceiveRealm(context.Context, string, Verified) error
}
type RealmAdminRepository interface {
	SetRealmFederation(context.Context, string, string, bool) error
}
type FeaturedRepository interface {
	Featured(context.Context, string, int, int) (Collection, error)
	SetFeatured(context.Context, string, string, []string) error
}

type ModerationRule struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Kind        string     `json:"type"`
	Action      string     `json:"action"`
	Pattern     string     `json:"pattern"`
	Regex       bool       `json:"is_regex"`
	Enabled     bool       `json:"enabled"`
	Priority    int        `json:"priority"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}
type ModerationDecision struct {
	Allowed bool   `json:"allowed"`
	Action  string `json:"action,omitempty"`
	RuleID  string `json:"rule_id,omitempty"`
}
type ModerationRepository interface {
	ModerationRules(context.Context, string, int, int) ([]ModerationRule, error)
	ModerationRule(context.Context, string, string) (ModerationRule, error)
	PutModerationRule(context.Context, string, ModerationRule) (ModerationRule, error)
	DeleteModerationRule(context.Context, string, string) error
	CheckModeration(context.Context, string, string, string) (ModerationDecision, error)
}

type KeyInfo struct {
	ActorID     string `json:"actor_id"`
	ActorURI    string `json:"actor_uri"`
	KeyID       string `json:"key_id"`
	HasKey      bool   `json:"has_key"`
	Fingerprint string `json:"fingerprint,omitempty"`
}
type KeyStatistics struct {
	LocalActors   int64 `json:"local_actors"`
	AvailableKeys int64 `json:"available_keys"`
	MissingKeys   int64 `json:"missing_keys"`
}
type AdminAudit struct {
	ID         int64     `json:"id"`
	AccountID  string    `json:"account_id"`
	Action     string    `json:"action"`
	Target     string    `json:"target"`
	OccurredAt time.Time `json:"occurred_at"`
}
type KeyAdminRepository interface {
	KeyAudit(context.Context, string, int, int) ([]AdminAudit, error)
	KeyStatistics(context.Context, string) (KeyStatistics, error)
	KeyInfo(context.Context, string, string) (KeyInfo, error)
	EnsureKey(context.Context, string, string) (KeyInfo, error)
	RotateKey(context.Context, string, string) (KeyInfo, error)
}

// KeyAdministrator is an optional external custody capability. Implementations
// generate/rotate keys and preserve old verification material as required by
// their policy. The federation database never stores private key bytes.
type KeyAdministrator interface {
	KeyProvider
	EnsureLocalKey(context.Context, string) error
	RotateLocalKey(context.Context, string) error
}

// RemoteKey is a trusted, preprovisioned binding. Key lookup must not perform
// discovery based on request data. Revocation/rotation belongs to its provider.
type RemoteKey struct {
	Incarnation string // empty for legacy operator pins; durable for admitted keys
	Actor       string
	Inbox       string
	KeyID       string
	PublicKey   *rsa.PublicKey
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
	// CleanupPolicyID opts into explicitly activated, non-reusable cleanup policy.
	CleanupPolicyID string
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
