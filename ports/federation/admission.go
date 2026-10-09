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
	"mime"
	"solsynth.local/go-migration/ports/authority"
	"strings"
)

// PublicAdmissionPolicy explicitly trusts HTTPS documents at one public origin
// for first contact only. Incarnation is an operator-supplied non-secret UUID.
// The durable policy and actor reservations must never be deleted or recycled.
type PublicAdmissionPolicy struct {
	Origin      string `json:"origin"`
	Incarnation string `json:"incarnation"`
}

func (p PublicAdmissionPolicy) Validate(c Config) error {
	o, e := Origin(p.Origin)
	if e != nil || o != p.Origin || strings.Contains(p.Origin, ":443") || !c.Allows(p.Origin) || !authority.ValidID(p.Incarnation) {
		return ErrInvalid
	}
	return nil
}
func (p PublicAdmissionPolicy) Binding(c Config, t DiscoveryTarget) (string, error) {
	if p.Validate(c) != nil || t.Validate(c) != nil {
		return "", ErrForbidden
	}
	u, e := ParseURL(t.Actor)
	if e != nil || u.String() != t.Actor {
		return "", ErrForbidden
	}
	o, _ := Origin(t.Actor)
	if o != p.Origin || strings.Contains(t.Actor, ":443") {
		return "", ErrForbidden
	}
	b, _ := json.Marshal([]any{p, t, c.Origin, c.AllowedOrigins, c.MaxAttempts, c.DeliveryTimeout.String()})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

type PublicAdmissionRepository interface {
	RequestPublicAdmission(context.Context, string, string, DiscoveryTarget) (DiscoveryJob, error)
	PublicAdmission(context.Context, string, string, DiscoveryTarget) (DiscoveryJob, error)
}

// FirstContactProvider must distinguish genuinely unknown identities from
// revoked, unavailable or retired identities. Only true authorizes enrollment;
// unknown errors and ordinary KeyProvider lookup failures never do. A nil
// public key is only the preliminary request check. The final check includes
// candidate key bytes and must reject collisions with current or retired pins.
// Implementations are local, immutable snapshots; this method performs no I/O.
type FirstContactProvider interface {
	FirstContact(context.Context, RemoteKey) (bool, error)
}

// DiscoverFirstContact reads HTTPS WebFinger and an exact nominated actor. Its
// result is only a candidate; the repository must still commit policy, collision,
// lease and local authority checks atomically. No aliases or rotations are adopted.
func DiscoverFirstContact(ctx context.Context, c Config, p PublicAdmissionPolicy, t DiscoveryTarget, fetch Fetcher) (DiscoveredActor, RemoteKey, bool, error) {
	var out DiscoveredActor
	var key RemoteKey
	if c.Validate() != nil || fetch == nil {
		return out, key, false, ErrUnavailable
	}
	if _, e := p.Binding(c, t); e != nil {
		return out, key, false, e
	}
	ctx, cancel := context.WithTimeout(ctx, c.DeliveryTimeout)
	defer cancel()
	cache := map[string]FetchResponse{}
	get := func(uri, accept string) (FetchResponse, bool, error) {
		v, e := fetch.Fetch(ctx, uri, accept)
		if e != nil {
			return v, !errors.Is(e, ErrInvalid) && !errors.Is(e, ErrForbidden), ErrUnavailable
		}
		if ctx.Err() != nil {
			return v, true, ErrUnavailable
		}
		if v.Status != 200 {
			return v, v.Status == 408 || v.Status == 429 || v.Status >= 500 && v.Status <= 599, ErrUnavailable
		}
		media, params, e := mime.ParseMediaType(v.ContentType)
		if e != nil || media != accept && (accept != MediaType || media != "application/ld+json" || params["profile"] != Context) || CheckJSON(v.Body) != nil {
			return v, false, ErrInvalid
		}
		cache[uri] = v
		return v, false, nil
	}
	jrd, retry, e := get(t.WebFingerURL(), "application/jrd+json")
	if e != nil {
		return out, key, retry, e
	}
	var doc struct {
		Subject string   `json:"subject"`
		Aliases []string `json:"aliases"`
		Links   []struct {
			Rel  string `json:"rel"`
			Type string `json:"type"`
			Href string `json:"href"`
		} `json:"links"`
	}
	if json.Unmarshal(jrd.Body, &doc) != nil || !discoveryFieldCase(jrd.Body, []string{"subject", "aliases", "links", "rel", "type", "href"}) || doc.Subject != t.Resource || len(doc.Links) > 32 || len(doc.Aliases) > 0 {
		return out, key, false, ErrInvalid
	}
	count := 0
	for _, l := range doc.Links {
		if l.Rel == "self" {
			count++
			if l.Type != MediaType || l.Href != t.Actor {
				return out, key, false, ErrForbidden
			}
		}
	}
	if count != 1 {
		return out, key, false, ErrInvalid
	}
	actor, retry, e := get(t.Actor, MediaType)
	if e != nil {
		return out, key, retry, e
	}
	var a struct {
		ID          string          `json:"id"`
		Inbox       string          `json:"inbox"`
		PublicKey   PublicKey       `json:"publicKey"`
		AlsoKnownAs []string        `json:"alsoKnownAs"`
		MovedTo     json.RawMessage `json:"movedTo"`
	}
	if json.Unmarshal(actor.Body, &a) != nil || !discoveryFieldCase(actor.Body, []string{"id", "inbox", "publicKey", "owner", "publicKeyPem", "alsoKnownAs", "movedTo"}) || a.ID != t.Actor || a.PublicKey.Owner != t.Actor || a.PublicKey.ID != t.Actor+"#main-key" || len(a.AlsoKnownAs) > 0 || len(a.MovedTo) > 0 && string(a.MovedTo) != "null" {
		return out, key, false, ErrForbidden
	}
	public, e := ParsePublicPEM(a.PublicKey.PEM)
	if e != nil {
		return out, key, false, e
	}
	key = RemoteKey{Actor: a.ID, Inbox: a.Inbox, KeyID: a.PublicKey.ID, PublicKey: public, Incarnation: p.Incarnation}
	if !c.ValidRemote(key) {
		return out, RemoteKey{}, false, ErrForbidden
	}
	out, retry, e = Discover(ctx, c, t, key, cachedAdmissionFetch(cache))
	if e == nil {
		out.Observation = "https_origin_first_contact"
	}
	return out, key, retry, e
}

type cachedAdmissionFetch map[string]FetchResponse

func (f cachedAdmissionFetch) Fetch(_ context.Context, u, a string) (FetchResponse, error) {
	v, ok := f[u]
	if !ok {
		return v, ErrForbidden
	}
	return v, nil
}

func PublicPEM(k *rsa.PublicKey) (string, error) {
	if k == nil {
		return "", ErrInvalid
	}
	b, e := x509.MarshalPKIXPublicKey(k)
	if e != nil {
		return "", ErrInvalid
	}
	v := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: b}))
	if _, e = ParsePublicPEM(v); e != nil {
		return "", e
	}
	return v, nil
}
