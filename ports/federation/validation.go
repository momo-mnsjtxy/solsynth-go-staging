// SPDX-License-Identifier: AGPL-3.0-only
package federation

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/url"
	"path"
	"strings"
	"time"
)

func ParseURL(raw string) (*url.URL, error) {
	if len(raw) == 0 || len(raw) > 2048 || strings.ContainsAny(raw, "\r\n\t \\") {
		return nil, ErrInvalid
	}
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.RawPath != "" || u.Port() != "" && u.Port() != "443" {
		return nil, ErrInvalid
	}
	host := u.Hostname()
	if strings.ToLower(host) != host || strings.HasSuffix(host, ".") || !strings.Contains(host, ".") || net.ParseIP(host) != nil || len(host) > 253 {
		return nil, ErrInvalid
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return nil, ErrInvalid
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
				return nil, ErrInvalid
			}
		}
	}
	if u.Path != "" && (path.Clean(u.Path) != u.Path || strings.Contains(u.Path, "//")) {
		return nil, ErrInvalid
	}
	return u, nil
}
func Origin(raw string) (string, error) {
	u, e := ParseURL(raw)
	if e != nil {
		return "", e
	}
	return u.Scheme + "://" + u.Host, nil
}
func (c Config) Validate() error {
	u, e := ParseURL(c.Origin)
	if e != nil || u.Path != "" || u.Fragment != "" || u.String() != c.Origin {
		return ErrInvalid
	}
	if c.MaxAttempts < 1 || c.MaxAttempts > 12 || c.DeliveryTimeout < time.Second || c.DeliveryTimeout > time.Minute {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, o := range c.AllowedOrigins {
		v, e := ParseURL(o)
		if e != nil || v.Path != "" || v.Fragment != "" || o == c.Origin || seen[o] {
			return ErrInvalid
		}
		seen[o] = true
	}
	return nil
}
func (c Config) Allows(raw string) bool {
	u, e := ParseURL(raw)
	if e != nil || u.Fragment != "" {
		return false
	}
	o := u.Scheme + "://" + u.Host
	for _, allow := range c.AllowedOrigins {
		if o == allow {
			return true
		}
	}
	return false
}
func (c Config) ValidRemote(k RemoteKey) bool {
	if !c.Allows(k.Actor) || !c.Allows(k.Inbox) || k.PublicKey == nil || k.PublicKey.N == nil || k.PublicKey.N.BitLen() < 2048 || k.PublicKey.N.BitLen() > 8192 || k.PublicKey.E < 3 || k.PublicKey.E%2 == 0 {
		return false
	}
	a, _ := Origin(k.Actor)
	b, _ := Origin(k.Inbox)
	key, e := ParseURL(k.KeyID)
	return e == nil && key.Fragment != "" && a == b && key.Scheme+"://"+key.Host == a
}
func ValidName(s string) bool {
	if len(s) < 1 || len(s) > 256 {
		return false
	}
	for _, c := range s {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return s != "." && s != ".."
}
func Has(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
func Addressed(a Activity, target string) bool { return Has(a.To, target) || Has(a.CC, target) }
func IsPublic(a Activity) bool                 { return Has(a.To, Public) || Has(a.CC, Public) }
func ObjectID(raw json.RawMessage) (string, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return s, nil
	}
	var v struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &v) != nil || v.ID == "" {
		return "", ErrInvalid
	}
	return v.ID, nil
}

// CheckJSON rejects ambiguous duplicate keys, hidden-recipient fields, trailing
// values, and deep recursion before a typed decoder interprets an activity.
func CheckJSON(b []byte) error {
	if len(b) == 0 || len(b) > MaxBody {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 12 {
			return ErrInvalid
		}
		t, e := d.Token()
		if e != nil {
			return ErrInvalid
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return ErrInvalid
				}
				key, ok := k.(string)
				if !ok || seen[key] || key == "bto" || key == "bcc" {
					return ErrInvalid
				}
				seen[key] = true
				if e = walk(depth + 1); e != nil {
					return e
				}
			}
			t, e = d.Token()
			if e != nil || t != json.Delim('}') {
				return ErrInvalid
			}
		case '[':
			for d.More() {
				if e = walk(depth + 1); e != nil {
					return e
				}
			}
			t, e = d.Token()
			if e != nil || t != json.Delim(']') {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
		return nil
	}
	if e := walk(0); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return ErrInvalid
	}
	return nil
}
