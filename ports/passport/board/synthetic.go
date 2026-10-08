// SPDX-License-Identifier: AGPL-3.0-only
package board

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// SyntheticConfig is deliberately not a production account/permission format.
// It contains no bearer token, secret, or upstream permission decision endpoint.
type SyntheticConfig struct {
	Synthetic        bool                 `json:"synthetic"`
	AllowPublicReads bool                 `json:"allow_public_reads"`
	Accounts         []Account            `json:"accounts"`
	Principals       []SyntheticPrincipal `json:"principals"`
	Apps             []SyntheticApp       `json:"apps"`
}
type SyntheticPrincipal struct {
	Name        string   `json:"name"`
	AccountID   string   `json:"account_id"`
	AppID       string   `json:"app_id"`
	Permissions []string `json:"permissions"`
}
type SyntheticApp struct {
	ID                 string     `json:"id"`
	BoardCapability    bool       `json:"board_capability"`
	ConsentingAccounts []string   `json:"consenting_accounts"`
	Widgets            []Manifest `json:"widgets"`
}
type SyntheticFixture struct {
	accounts   map[string]Account
	principals map[string]SyntheticPrincipal
	apps       map[string]SyntheticApp
	public     bool
	Cache      *GroupCache
}

func DecodeSyntheticConfig(r io.Reader) (SyntheticConfig, error) {
	var c SyntheticConfig
	const maxFixtureBytes = 2 << 20
	raw, err := io.ReadAll(io.LimitReader(r, maxFixtureBytes+1))
	if err != nil || len(raw) > maxFixtureBytes {
		return c, errors.New("synthetic fixture exceeds size limit or cannot be read")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	var tail any
	if err := d.Decode(&tail); err != io.EOF {
		return c, errors.New("synthetic fixture must contain exactly one JSON value")
	}
	return c, nil
}
func NewSyntheticFixture(config SyntheticConfig) (*SyntheticFixture, error) {
	if !config.Synthetic {
		return nil, errors.New("explicit synthetic fixture opt-in required")
	}
	f := &SyntheticFixture{accounts: map[string]Account{}, principals: map[string]SyntheticPrincipal{}, apps: map[string]SyntheticApp{}, public: config.AllowPublicReads, Cache: NewGroupCache()}
	for _, a := range config.Accounts {
		id, ok := CanonicalUUID(a.ID)
		if !ok || id != a.ID || a.Name == "" || strings.Contains(a.Name, "/") {
			return nil, errors.New("synthetic accounts need a canonical UUID and name")
		}
		if _, ok := f.accounts[a.ID]; ok {
			return nil, errors.New("duplicate synthetic account")
		}
		if _, ok := f.accounts[a.Name]; ok {
			return nil, errors.New("duplicate synthetic account name")
		}
		f.accounts[a.ID] = a
		f.accounts[a.Name] = a
	}
	for _, a := range config.Apps {
		id, ok := CanonicalUUID(a.ID)
		if !ok || id != a.ID {
			return nil, errors.New("synthetic apps need canonical UUIDs")
		}
		if _, ok := f.apps[id]; ok {
			return nil, errors.New("duplicate synthetic app")
		}
		keys := map[string]bool{}
		for _, w := range a.Widgets {
			key := strings.ToLower(w.Key)
			if strings.TrimSpace(key) == "" || keys[key] {
				return nil, errors.New("synthetic widget keys must be nonblank and unique ignoring case")
			}
			keys[key] = true
			if w.MaxPayloadBytes != nil && *w.MaxPayloadBytes < 0 {
				return nil, errors.New("negative payload limit")
			}
		}
		for _, account := range a.ConsentingAccounts {
			if a, ok := f.accounts[account]; !ok || a.ID != account {
				return nil, errors.New("synthetic consent account must be an existing canonical account UUID")
			}
		}
		// Freeze caller-owned slices and pointer-backed widget definitions.
		raw, err := json.Marshal(a)
		if err != nil {
			return nil, err
		}
		var detached SyntheticApp
		if err = json.Unmarshal(raw, &detached); err != nil {
			return nil, err
		}
		f.apps[id] = detached
	}
	for _, p := range config.Principals {
		if p.Name == "" || strings.TrimSpace(p.Name) != p.Name || strings.ContainsAny(p.Name, "\r\n") {
			return nil, errors.New("invalid synthetic principal name")
		}
		if _, ok := f.principals[p.Name]; ok {
			return nil, errors.New("duplicate synthetic principal")
		}
		if (p.AccountID == "") == (p.AppID == "") {
			return nil, errors.New("synthetic principal must identify exactly one account or app")
		}
		if p.AccountID != "" {
			a, ok := f.accounts[p.AccountID]
			if !ok || a.ID != p.AccountID {
				return nil, errors.New("unknown synthetic principal account")
			}
		}
		if p.AppID != "" {
			if _, ok := f.apps[p.AppID]; !ok {
				return nil, errors.New("unknown synthetic principal app")
			}
			if len(p.Permissions) != 0 {
				return nil, errors.New("app principal cannot have account permissions")
			}
		}
		for _, grant := range p.Permissions {
			switch grant {
			case SelfManage, AccountsView, BoardManage:
			default:
				return nil, fmt.Errorf("unknown synthetic permission %q", grant)
			}
		}
		p.Permissions = append([]string(nil), p.Permissions...)
		f.principals[p.Name] = p
	}
	return f, nil
}
func (f *SyntheticFixture) Authenticate(r *http.Request) (Principal, error) {
	// Test-only header deliberately cannot be confused with real authentication.
	headers := r.Header.Values("X-Synthetic-Principal")
	if len(headers) != 1 {
		return Principal{}, ErrUnauthenticated
	}
	p, ok := f.principals[headers[0]]
	if !ok {
		return Principal{}, ErrUnauthenticated
	}
	return Principal{Name: p.Name, AccountID: p.AccountID, AppID: p.AppID}, nil
}
func (f *SyntheticFixture) Require(ctx context.Context, p Principal, permission string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.AccountID == "" {
		return ErrForbidden
	}
	configured, ok := f.principals[p.Name]
	if !ok || configured.AccountID != p.AccountID {
		return ErrForbidden
	}
	for _, grant := range configured.Permissions {
		if grant == permission {
			return nil
		}
	}
	return ErrForbidden
}
func (f *SyntheticFixture) PublicRead(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !f.public {
		return ErrForbidden
	}
	return nil
}
func (f *SyntheticFixture) AuthorizeApp(ctx context.Context, p Principal, appID, account string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	app, ok := f.apps[appID]
	if !ok {
		return ErrAppNotFound
	}
	if p.AppID != appID {
		return ErrForbidden
	}
	if !app.BoardCapability {
		return ErrForbidden
	}
	if account == "" {
		return nil
	}
	for _, id := range app.ConsentingAccounts {
		if id == account {
			return nil
		}
	}
	return ErrForbidden
}
func (f *SyntheticFixture) Resolve(ctx context.Context, identifier string) (Account, error) {
	if err := ctx.Err(); err != nil {
		return Account{}, err
	}
	if id, ok := CanonicalUUID(identifier); ok {
		identifier = id
	}
	a, ok := f.accounts[identifier]
	if !ok {
		return Account{}, ErrAccountNotFound
	}
	if cached, ok := f.Cache.Get("account:"+a.ID, identifier); ok {
		return cached, nil
	}
	f.Cache.Put("account:"+a.ID, identifier, a)
	return a, nil
}
func (f *SyntheticFixture) manifest(ctx context.Context, appID, key string) (Manifest, error) {
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	a, ok := f.apps[appID]
	if !ok {
		return Manifest{}, invalid("App not found")
	}
	if !a.BoardCapability {
		return Manifest{}, invalid("Custom app must declare 'accounts.profile.board' scope to provide board widgets.")
	}
	for _, w := range a.Widgets {
		if strings.EqualFold(w.Key, key) {
			return w, nil
		}
	}
	return Manifest{}, invalid("Board widget is not configured for this app.")
}
func (f *SyntheticFixture) Lookup(ctx context.Context, appID, key string) (*Widget, error) {
	w, err := f.manifest(ctx, appID, key)
	if err != nil {
		return nil, err
	}
	return &Widget{Key: w.Key, Enabled: w.IsEnabled, AllowMultiple: w.AllowMultiple}, nil
}
func (f *SyntheticFixture) Validate(ctx context.Context, appID, key string, payload Payload) (Payload, error) {
	w, err := f.manifest(ctx, appID, key)
	if err != nil {
		return nil, err
	}
	return ValidateManifestPayload(ctx, w, payload, DocumentedEnvelope{})
}
func (f *SyntheticFixture) Manifests(ctx context.Context, appID string) ([]Manifest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a, ok := f.apps[appID]
	if !ok {
		return nil, ErrAppNotFound
	}
	raw, err := json.Marshal(a.Widgets)
	if err != nil {
		return nil, err
	}
	out := []Manifest{}
	if len(a.Widgets) > 0 {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}
func (f *SyntheticFixture) HTTPHandler(repo Repository, deleter Deleter) http.Handler {
	return NewHTTPHandler(HTTPOptions{Service: Service{Repository: repo, Catalog: f, Normalizer: DocumentedEnvelope{}, Cache: f.Cache}, Authority: f, Accounts: f, AccountCache: f.Cache, Delete: deleter})
}
