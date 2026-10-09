// SPDX-License-Identifier: AGPL-3.0-only
package connectionprovider

import (
	"context"
	"crypto/md5" // Last.fm's documented API signature, never a password digest.
	"encoding/hex"
	"net/http"
	"net/url"
	"solsynth.local/go-migration/ports/passport/activities"
	p "solsynth.local/go-migration/ports/passport/connections"
	"strings"
	"unicode/utf8"
)

type LastFM struct {
	key, secret, callback string
	client                *http.Client
}

func lastFMKey(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 16 && len(s) == 32
}
func lastFMName(s string) bool {
	if len(s) < 1 || len(s) > 64 || !utf8.ValidString(s) {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func NewLastFM(key, secret, callback string, client *http.Client) (*LastFM, error) {
	if !lastFMKey(key) || !lastFMKey(secret) {
		return nil, p.ErrInvalid
	}
	if !secureURL(callback) {
		return nil, p.ErrInvalid
	}
	return &LastFM{key, secret, callback, restrictedClient(client)}, nil
}
func (p *LastFM) Name() string { return "lastfm" }
func (v *LastFM) Start(state, challenge string) (string, error) {
	cb, _ := url.Parse(v.callback)
	q := cb.Query()
	q.Set("state", state)
	cb.RawQuery = q.Encode()
	q = url.Values{"api_key": {v.key}, "cb": {cb.String()}}
	return "https://www.last.fm/api/auth/?" + q.Encode(), nil
}
func (v *LastFM) Verify(ctx context.Context, in p.Completion, verifier string) (p.Verified, error) {
	if !lastFMKey(in.Code) || len(in.OpenID) != 0 {
		return p.Verified{}, p.ErrProof
	}
	sig := md5.Sum([]byte("api_key" + v.key + "methodauth.getSessiontoken" + in.Code + v.secret))
	q := url.Values{"method": {"auth.getSession"}, "api_key": {v.key}, "token": {in.Code}, "api_sig": {hex.EncodeToString(sig[:])}, "format": {"json"}}
	req, e := http.NewRequestWithContext(ctx, "POST", "https://ws.audioscrobbler.com/2.0/", strings.NewReader(q.Encode()))
	if e != nil {
		return p.Verified{}, p.ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var out struct {
		Error   int                        `json:"error"`
		Session struct{ Name, Key string } `json:"session"`
	}
	if e = fetchJSON(v.client, req, &out); e != nil {
		return p.Verified{}, e
	}
	if out.Error != 0 || !lastFMName(out.Session.Name) || !lastFMKey(out.Session.Key) {
		return p.Verified{}, p.ErrProof
	}
	// Public recent tracks need no lasting Last.fm session credential. Discard it.
	return p.Verified{Identifier: strings.ToLower(out.Session.Name), Username: out.Session.Name, DisplayName: out.Session.Name}, nil
}

type LastFMPresence struct {
	key    string
	client *http.Client
}

func NewLastFMPresence(key string, client *http.Client) (*LastFMPresence, error) {
	if !lastFMKey(key) {
		return nil, p.ErrInvalid
	}
	return &LastFMPresence{key, restrictedClient(client)}, nil
}
func (v *LastFMPresence) Observe(ctx context.Context, user string) (activities.Content, bool, error) {
	if !lastFMName(user) {
		return activities.Content{}, false, p.ErrProof
	}
	q := url.Values{"method": {"user.getRecentTracks"}, "api_key": {v.key}, "user": {user}, "limit": {"1"}, "format": {"json"}}
	req, e := http.NewRequestWithContext(ctx, "GET", "https://ws.audioscrobbler.com/2.0/?"+q.Encode(), nil)
	if e != nil {
		return activities.Content{}, false, p.ErrUnavailable
	}
	var out struct {
		Error  int `json:"error"`
		Recent struct {
			Attr struct {
				User string `json:"user"`
			} `json:"@attr"`
			Tracks []struct {
				Name   string `json:"name"`
				Artist struct {
					Text string `json:"#text"`
				} `json:"artist"`
				Attr struct {
					Playing string `json:"nowplaying"`
				} `json:"@attr"`
			} `json:"track"`
		} `json:"recenttracks"`
	}
	if e = fetchJSON(v.client, req, &out); e != nil {
		return activities.Content{}, false, e
	}
	if out.Error != 0 || !strings.EqualFold(out.Recent.Attr.User, user) || len(out.Recent.Tracks) > 1 {
		return activities.Content{}, false, p.ErrProof
	}
	if len(out.Recent.Tracks) == 0 || out.Recent.Tracks[0].Attr.Playing != "true" {
		return activities.Content{}, false, nil
	}
	t := out.Recent.Tracks[0]
	if len(t.Name) == 0 || len(t.Name) > 4096 || len(t.Artist.Text) > 4096 || !utf8.ValidString(t.Name) || !utf8.ValidString(t.Artist.Text) || strings.ContainsRune(t.Name+t.Artist.Text, 0) {
		return activities.Content{}, false, p.ErrProof
	}
	provider, kind := "lastfm", "music"
	return activities.Content{Provider: &provider, Type: &kind, Title: &t.Name, Subtitle: &t.Artist.Text}, true, nil
}

func fetchJSON(client *http.Client, req *http.Request, out any) error {
	resp, e := client.Do(req)
	if e != nil {
		return p.ErrUnavailable
	}
	return readJSON(resp, out)
}
