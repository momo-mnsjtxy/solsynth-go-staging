// SPDX-License-Identifier: AGPL-3.0-only
package federation

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"html"
	"mime"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// DiscoveryTarget is an operator-selected handle and exact actor URI. Discovery
// observes an already pinned identity. It never enrolls or replaces a key.
type DiscoveryTarget struct {
	Resource string `json:"resource"`
	Actor    string `json:"actor"`
}
type DiscoveryFeatures struct {
	Server  *ServerConfig
	Targets []DiscoveryTarget
}
type ServerConfig struct {
	Username     string
	Name         string
	Summary      string
	PublicKeyPEM string
}
type ServerActor struct {
	Context       []string  `json:"@context"`
	ID            string    `json:"id"`
	Type          string    `json:"type"`
	Username      string    `json:"preferredUsername"`
	Name          string    `json:"name"`
	Summary       string    `json:"summary"`
	URL           string    `json:"url"`
	Inbox         string    `json:"inbox"`
	Outbox        string    `json:"outbox"`
	Followers     string    `json:"followers"`
	Following     string    `json:"following"`
	AlsoKnownAs   []string  `json:"alsoKnownAs"`
	InstanceActor bool      `json:"instanceActor"`
	PublicKey     PublicKey `json:"publicKey"`
}
type KeyDocument struct {
	Context []string `json:"@context"`
	Type    string   `json:"type"`
	PublicKey
}

func PublicKeyDocument(k PublicKey) KeyDocument {
	return KeyDocument{Context: []string{"https://w3id.org/security/v1", Context}, Type: "RsaSignature2017", PublicKey: k}
}
func ParsePublicPEM(s string) (*rsa.PublicKey, error) {
	if len(s) > 16384 {
		return nil, ErrInvalid
	}
	block, rest := pem.Decode([]byte(s))
	if block == nil || block.Type != "PUBLIC KEY" || len(strings.TrimSpace(string(rest))) != 0 || len(block.Headers) != 0 {
		return nil, ErrInvalid
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, ErrInvalid
	}
	k, ok := parsed.(*rsa.PublicKey)
	if !ok || k.N == nil || k.N.BitLen() < 2048 || k.N.BitLen() > 8192 || k.E < 3 || k.E%2 == 0 {
		return nil, ErrInvalid
	}
	return k, nil
}
func KeyFingerprint(k *rsa.PublicKey) (string, error) {
	if k == nil || k.N == nil {
		return "", ErrInvalid
	}
	b, e := x509.MarshalPKIXPublicKey(k)
	if e != nil {
		return "", ErrInvalid
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
func (s ServerConfig) Actor(c Config) (ServerActor, error) {
	var out ServerActor
	if c.Validate() != nil || !ValidName(s.Username) || len(s.Username) > 128 || !boundedText(s.Name, 256) || !boundedText(s.Summary, 4096) {
		return out, ErrInvalid
	}
	key, e := ParsePublicPEM(s.PublicKeyPEM)
	if e != nil {
		return out, e
	}
	der, _ := x509.MarshalPKIXPublicKey(key)
	id := c.Origin + "/activitypub/actor"
	return ServerActor{Context: []string{Context, "https://w3id.org/security/v1"}, ID: id, Type: "Application", Username: s.Username, Name: html.EscapeString(s.Name), Summary: html.EscapeString(s.Summary), URL: c.Origin, Inbox: id + "/inbox", Outbox: id + "/outbox", Followers: id + "/followers", Following: id + "/following", AlsoKnownAs: []string{c.Origin + "/"}, InstanceActor: true, PublicKey: PublicKey{ID: id + "#main-key", Owner: id, PEM: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))}}, nil
}
func boundedText(s string, n int) bool {
	return len(s) <= n && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
func (t DiscoveryTarget) Validate(c Config) error {
	if !strings.HasPrefix(t.Resource, "acct:") || len(t.Resource) > 512 || !c.Allows(t.Actor) {
		return ErrInvalid
	}
	name, domain, ok := strings.Cut(strings.TrimPrefix(t.Resource, "acct:"), "@")
	if !ok || !ValidName(name) || strings.Contains(domain, "@") {
		return ErrInvalid
	}
	origin, e := Origin(t.Actor)
	if e != nil || origin != "https://"+domain {
		return ErrInvalid
	}
	return nil
}
func (t DiscoveryTarget) WebFingerURL() string {
	_, domain, _ := strings.Cut(strings.TrimPrefix(t.Resource, "acct:"), "@")
	return "https://" + domain + "/.well-known/webfinger?resource=" + url.QueryEscape(t.Resource)
}
func (t DiscoveryTarget) Binding(c Config, k RemoteKey) (string, error) {
	if t.Validate(c) != nil || !c.ValidRemote(k) || k.Actor != t.Actor {
		return "", ErrForbidden
	}
	fingerprint, e := KeyFingerprint(k.PublicKey)
	if e != nil {
		return "", e
	}
	origins := append([]string{}, c.AllowedOrigins...)
	sort.Strings(origins)
	b, _ := json.Marshal([]any{t.Resource, k.Actor, k.Inbox, k.KeyID, fingerprint, c.Origin, origins, strconv.Itoa(c.MaxAttempts), c.DeliveryTimeout.String()})
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:]), nil
}

// Fetcher receives only exact configured WebFinger/actor URLs. Implementations
// must enforce public-address DNS pinning, no proxies/redirects, time/body limits.
// The included HTTPFetcher does so. It is never invoked while holding SQL locks.
type Fetcher interface {
	Fetch(context.Context, string, string) (FetchResponse, error)
}
type FetchResponse struct {
	Status      int
	ContentType string
	Body        []byte
}

// DiscoveredActor is untrusted descriptive metadata checked against an existing
// pin. It is deliberately separate from Actor/RemoteKey and cannot verify inboxes.
type DiscoveredActor struct {
	Resource       string    `json:"resource"`
	ID             string    `json:"id"`
	Type           string    `json:"type"`
	Username       string    `json:"preferredUsername"`
	Name           string    `json:"name"`
	Summary        string    `json:"summary"`
	Inbox          string    `json:"inbox"`
	KeyID          string    `json:"key_id"`
	KeyFingerprint string    `json:"key_fingerprint"`
	Observation    string    `json:"observation"`
	AlsoKnownAs    []string  `json:"also_known_as,omitempty"`
	ObservedAt     time.Time `json:"observed_at"`
}
type DiscoveryJob struct {
	ID       string           `json:"id"`
	Resource string           `json:"resource"`
	State    string           `json:"state"`
	Attempts int              `json:"attempts"`
	Result   *DiscoveredActor `json:"result,omitempty"`
}
type DiscoveryRepository interface {
	RequestDiscovery(context.Context, string, string, string) (DiscoveryJob, error)
	Discovery(context.Context, string, string, string) (DiscoveryJob, error)
}
type ServerRepository interface {
	ServerActor(context.Context) (ServerActor, error)
}

// Discover performs precisely two bounded GETs. Only the exact configured self
// link is accepted, and actor/inbox/key owner/key bytes must match existing pins.
// It does not recursively load contexts, aliases, keys, media, or collections.
func Discover(ctx context.Context, c Config, t DiscoveryTarget, k RemoteKey, fetch Fetcher) (DiscoveredActor, bool, error) {
	var out DiscoveredActor
	if c.Validate() != nil || fetch == nil {
		return out, false, ErrUnavailable
	}
	if _, e := t.Binding(c, k); e != nil {
		return out, false, e
	}
	k = k.Snapshot()
	ctx, cancel := context.WithTimeout(ctx, c.DeliveryTimeout)
	defer cancel()
	get := func(uri, accept string) ([]byte, bool, error) {
		response, e := fetch.Fetch(ctx, uri, accept)
		if e != nil {
			return nil, !errors.Is(e, ErrInvalid) && !errors.Is(e, ErrForbidden), ErrUnavailable
		}
		if ctx.Err() != nil {
			return nil, true, ErrUnavailable
		}
		if response.Status != 200 {
			return nil, response.Status == 408 || response.Status == 429 || response.Status >= 500 && response.Status <= 599, ErrUnavailable
		}
		media, params, e := mime.ParseMediaType(response.ContentType)
		if e != nil || media != accept && (accept != MediaType || media != "application/ld+json" || params["profile"] != Context) || len(response.Body) > MaxBody || CheckJSON(response.Body) != nil {
			return nil, false, ErrInvalid
		}
		return response.Body, false, nil
	}
	b, retry, e := get(t.WebFingerURL(), "application/jrd+json")
	if e != nil {
		return out, retry, e
	}
	var jrd struct {
		Subject string `json:"subject"`
		Links   []struct {
			Rel  string `json:"rel"`
			Type string `json:"type"`
			Href string `json:"href"`
		} `json:"links"`
	}
	if json.Unmarshal(b, &jrd) != nil || jrd.Subject != t.Resource || len(jrd.Links) > 32 {
		return out, false, ErrInvalid
	}
	if !discoveryFieldCase(b, []string{"subject", "links", "rel", "type", "href"}) {
		return out, false, ErrInvalid
	}
	count := 0
	for _, l := range jrd.Links {
		if l.Rel == "self" {
			count++
			if l.Type != MediaType || l.Href != t.Actor {
				return out, false, ErrForbidden
			}
		}
	}
	if count != 1 {
		return out, false, ErrInvalid
	}
	b, retry, e = get(t.Actor, MediaType)
	if e != nil {
		return out, retry, e
	}
	var actor struct {
		ID          string    `json:"id"`
		Type        string    `json:"type"`
		Username    string    `json:"preferredUsername"`
		Name        string    `json:"name"`
		Summary     string    `json:"summary"`
		Inbox       string    `json:"inbox"`
		PublicKey   PublicKey `json:"publicKey"`
		AlsoKnownAs []string  `json:"alsoKnownAs"`
	}
	if json.Unmarshal(b, &actor) != nil {
		return out, false, ErrInvalid
	}
	if !discoveryFieldCase(b, []string{"id", "type", "preferredUsername", "name", "summary", "inbox", "publicKey", "owner", "publicKeyPem", "alsoKnownAs"}) {
		return out, false, ErrInvalid
	}
	name, _, _ := strings.Cut(strings.TrimPrefix(t.Resource, "acct:"), "@")
	if actor.ID != t.Actor || actor.Inbox != k.Inbox || actor.PublicKey.ID != k.KeyID || actor.PublicKey.Owner != t.Actor || actor.Username != name {
		return out, false, ErrForbidden
	}
	if !Has([]string{"Person", "Application", "Service", "Organization", "Group"}, actor.Type) || !boundedText(actor.Name, 256) || !boundedText(actor.Summary, 4096) {
		return out, false, ErrInvalid
	}
	if len(actor.AlsoKnownAs) > 16 {
		return out, false, ErrInvalid
	}
	aliases := make([]string, 0, len(actor.AlsoKnownAs))
	seenAliases := map[string]bool{}
	for _, alias := range actor.AlsoKnownAs {
		if len(alias) == 0 || len(alias) > 2048 || !c.Allows(alias) || seenAliases[alias] {
			return out, false, ErrInvalid
		}
		seenAliases[alias] = true
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	key, e := ParsePublicPEM(actor.PublicKey.PEM)
	if e != nil {
		return out, false, e
	}
	fingerprint, _ := KeyFingerprint(k.PublicKey)
	got, _ := KeyFingerprint(key)
	if got != fingerprint {
		return out, false, ErrForbidden
	}
	return DiscoveredActor{Resource: t.Resource, ID: actor.ID, Type: actor.Type, Username: actor.Username, Name: html.EscapeString(actor.Name), Summary: html.EscapeString(actor.Summary), Inbox: k.Inbox, KeyID: k.KeyID, KeyFingerprint: fingerprint, Observation: "unsigned_document_matches_configured_pin", AlsoKnownAs: aliases, ObservedAt: c.Clock()}, false, nil
}

// Go's JSON structs accept case-insensitive field aliases. Remote identity
// documents must not exploit that behavior to shadow correctly cased fields.
func discoveryFieldCase(b []byte, names []string) bool {
	var root any
	if json.Unmarshal(b, &root) != nil {
		return false
	}
	var check func(any) bool
	check = func(value any) bool {
		switch x := value.(type) {
		case map[string]any:
			for key, v := range x {
				for _, name := range names {
					if strings.EqualFold(key, name) && key != name {
						return false
					}
				}
				if !check(v) {
					return false
				}
			}
		case []any:
			for _, v := range x {
				if !check(v) {
					return false
				}
			}
		}
		return true
	}
	return check(root)
}
