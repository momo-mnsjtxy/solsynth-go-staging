// SPDX-License-Identifier: AGPL-3.0-only
// Package verification contains explicit, first-party verification adapters.
package verification

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"solsynth.local/go-migration/ports/authority/signin"
)

const turnstileEndpoint = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
const maxProviderResponse = 16 << 10

var actionPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
var bindingPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,255}$`)
var ErrConfiguration = errors.New("invalid verification adapter configuration")

// TurnstileConfig binds a widget's secret to one exact hostname, action and
// cData value. The configured widget must emit Action and SiteBinding (cData).
// The secret belongs to that widget's sitekey; Siteverify does not return it.
// A timeout is required and bounds the entire verification, including retries.
type TurnstileConfig struct {
	Secret      string
	Hostname    string
	Action      string
	SiteBinding string
	Timeout     time.Duration
}

type Turnstile struct {
	cfg    TurnstileConfig
	client *http.Client
	now    func() time.Time
}

var _ signin.Captcha = (*Turnstile)(nil)

// NewTurnstile never performs I/O. A nil transport uses a private, TLS-verifying
// transport with ambient proxies disabled. A nonnil transport is trusted code
// injection for synthetic tests, never a request or operator endpoint override.
func NewTurnstile(cfg TurnstileConfig, transport http.RoundTripper) (*Turnstile, error) {
	if !opaque(cfg.Secret, 1, 1024) || !hostname(cfg.Hostname) || !actionPattern.MatchString(cfg.Action) || !bindingPattern.MatchString(cfg.SiteBinding) || cfg.Timeout < time.Second || cfg.Timeout > 30*time.Second {
		return nil, ErrConfiguration
	}
	if transport == nil {
		transport = &http.Transport{
			Proxy:                  nil,
			DialContext:            (&net.Dialer{Timeout: cfg.Timeout, KeepAlive: 30 * time.Second}).DialContext,
			TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout:    cfg.Timeout,
			ResponseHeaderTimeout:  cfg.Timeout,
			MaxResponseHeaderBytes: 8192,
			IdleConnTimeout:        30 * time.Second,
			MaxIdleConns:           4,
			MaxIdleConnsPerHost:    4,
		}
	}
	return &Turnstile{cfg: cfg, client: &http.Client{Transport: transport, Timeout: cfg.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, now: time.Now}, nil
}

func opaque(s string, min, max int) bool {
	if len(s) < min || len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r <= 32 || r == 127 {
			return false
		}
	}
	return true
}
func hostname(s string) bool {
	if len(s) == 0 || len(s) > 253 || s != strings.ToLower(s) {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// Verify never trusts headers or a caller's success flag. Only a validated
// response from the fixed provider, with all configured bindings, is accepted.
func (t *Turnstile) Verify(ctx context.Context, token, peer string) error {
	if t == nil || t.client == nil {
		return signin.ErrCaptcha
	}
	if !opaque(token, 1, 2048) {
		return signin.ErrInvalid
	}
	remote, err := netip.ParseAddr(peer)
	if err != nil || remote.Zone() != "" {
		return signin.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, t.cfg.Timeout)
	defer cancel()
	var key [16]byte
	if _, err = rand.Read(key[:]); err != nil {
		return signin.ErrCaptcha
	}
	key[6] = key[6]&0x0f | 0x40
	key[8] = key[8]&0x3f | 0x80
	idempotency := fmt.Sprintf("%x-%x-%x-%x-%x", key[:4], key[4:6], key[6:8], key[8:10], key[10:])
	form := url.Values{"secret": {t.cfg.Secret}, "response": {token}, "remoteip": {remote.Unmap().String()}, "idempotency_key": {idempotency}}
	// Retry only uncertain transport failures, provider 5xx and internal-error.
	// Both attempts use the same idempotency key; results are never cached.
	for attempt := 0; attempt < 2; attempt++ {
		if ctx.Err() != nil {
			return signin.ErrCaptcha
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, turnstileEndpoint, strings.NewReader(form.Encode()))
		if err != nil {
			return signin.ErrCaptcha
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		response, err := t.client.Do(req)
		if err != nil {
			if response != nil && response.Body != nil {
				response.Body.Close()
			}
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, maxProviderResponse+1))
		closeErr := response.Body.Close()
		if response.StatusCode >= 500 && response.StatusCode <= 599 {
			continue
		}
		if response.StatusCode != http.StatusOK || readErr != nil || closeErr != nil || len(body) > maxProviderResponse {
			return signin.ErrCaptcha
		}
		media, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if mediaErr != nil || media != "application/json" {
			return signin.ErrCaptcha
		}
		result, err := parseTurnstile(body)
		if err != nil {
			return signin.ErrCaptcha
		}
		if !result.Success {
			if len(result.Errors) == 1 && result.Errors[0] == "internal-error" {
				continue
			}
			for _, code := range result.Errors {
				switch code {
				case "invalid-input-response", "missing-input-response", "timeout-or-duplicate":
				default:
					return signin.ErrCaptcha
				}
			}
			if len(result.Errors) == 0 {
				return signin.ErrCaptcha
			}
			return signin.ErrInvalid
		}
		if len(result.Errors) != 0 {
			return signin.ErrCaptcha
		}
		if result.Hostname != t.cfg.Hostname || result.Action != t.cfg.Action || result.CData != t.cfg.SiteBinding {
			return signin.ErrInvalid
		}
		timestamp, err := time.Parse(time.RFC3339Nano, result.Timestamp)
		now := t.now().UTC()
		if err != nil || timestamp.After(now.Add(30*time.Second)) || !timestamp.After(now.Add(-5*time.Minute)) {
			return signin.ErrInvalid
		}
		return nil
	}
	return signin.ErrCaptcha
}

type turnstileResponse struct {
	Success   bool     `json:"success"`
	Hostname  string   `json:"hostname"`
	Action    string   `json:"action"`
	CData     string   `json:"cdata"`
	Timestamp string   `json:"challenge_ts"`
	Errors    []string `json:"error-codes"`
}

func parseTurnstile(body []byte) (turnstileResponse, error) {
	var result turnstileResponse
	// Reject duplicate keys (including case variants of known fields), trailing
	// objects and missing/wrongly-typed success. Unknown provider metadata is safe.
	d := json.NewDecoder(bytes.NewReader(body))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return result, signin.ErrCaptcha
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		name, err := d.Token()
		if err != nil {
			return result, signin.ErrCaptcha
		}
		key, ok := name.(string)
		if !ok || key != strings.ToLower(key) {
			return result, signin.ErrCaptcha
		}
		if _, exists := fields[key]; exists {
			return result, signin.ErrCaptcha
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return result, signin.ErrCaptcha
		}
		fields[key] = value
	}
	if _, err = d.Token(); err != nil {
		return result, signin.ErrCaptcha
	}
	if d.Decode(new(any)) != io.EOF {
		return result, signin.ErrCaptcha
	}
	if string(fields["success"]) != "true" && string(fields["success"]) != "false" {
		return result, signin.ErrCaptcha
	}
	if err = json.Unmarshal(body, &result); err != nil {
		return result, signin.ErrCaptcha
	}
	return result, nil
}
