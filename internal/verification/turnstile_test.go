// SPDX-License-Identifier: AGPL-3.0-only
package verification

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"solsynth.local/go-migration/ports/authority/signin"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func captchaConfig() TurnstileConfig {
	return TurnstileConfig{Secret: "synthetic-only-secret", Hostname: "identity.example.invalid", Action: "identity", SiteBinding: "owned-site", Timeout: time.Second}
}
func providerResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

const validProvider = `{"success":true,"hostname":"identity.example.invalid","action":"identity","cdata":"owned-site","challenge_ts":"2026-01-02T03:04:00Z","error-codes":[]}`

func testCaptcha(t *testing.T, transport http.RoundTripper) *Turnstile {
	t.Helper()
	v, e := NewTurnstile(captchaConfig(), transport)
	if e != nil {
		t.Fatal(e)
	}
	v.now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
	return v
}

func TestTurnstileFixedEndpointAndBindings(t *testing.T) {
	calls := 0
	v := testCaptcha(t, roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != turnstileEndpoint || r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Fatal("provider target changed")
		}
		if r.Header.Get("Authorization") != "" || r.URL.RawQuery != "" {
			t.Fatal("secret in headers or query")
		}
		if e := r.ParseForm(); e != nil {
			t.Fatal(e)
		}
		if len(r.PostForm) != 4 || r.PostForm.Get("secret") != captchaConfig().Secret || r.PostForm.Get("response") != "synthetic-only-token" || r.PostForm.Get("remoteip") != "192.0.2.1" {
			t.Fatal("provider form mismatch")
		}
		if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(r.PostForm.Get("idempotency_key")) {
			t.Fatal("missing UUIDv4 idempotency")
		}
		if _, ok := r.Context().Deadline(); !ok {
			t.Fatal("unbounded provider call")
		}
		return providerResponse(200, validProvider), nil
	}))
	if e := v.Verify(context.Background(), "synthetic-only-token", "::ffff:192.0.2.1"); e != nil || calls != 1 {
		t.Fatal("verification failed", e, calls)
	}
}

func TestTurnstileRejectsInvalidAndMalformedClaims(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		want       error
	}{
		{"rejected", `{"success":false,"error-codes":["invalid-input-response"]}`, 200, signin.ErrInvalid},
		{"duplicate", `{"success":false,"error-codes":["timeout-or-duplicate"]}`, 200, signin.ErrInvalid},
		{"unknown_error", `{"success":false,"error-codes":["new-error"]}`, 200, signin.ErrCaptcha},
		{"secret_error", `{"success":false,"error-codes":["invalid-input-secret"]}`, 200, signin.ErrCaptcha},
		{"rejection_without_reason", `{"success":false}`, 200, signin.ErrCaptcha},
		{"missing_success", `{}`, 200, signin.ErrCaptcha},
		{"null_success", `{"success":null}`, 200, signin.ErrCaptcha},
		{"string_success", `{"success":"true"}`, 200, signin.ErrCaptcha},
		{"array", `[]`, 200, signin.ErrCaptcha},
		{"duplicate_success", strings.Replace(validProvider, `"success":true`, `"success":false,"success":true`, 1), 200, signin.ErrCaptcha},
		{"case_variant", strings.Replace(validProvider, `"success":true`, `"Success":true`, 1), 200, signin.ErrCaptcha},
		{"trailing_object", validProvider + `{}`, 200, signin.ErrCaptcha},
		{"trailing_garbage", validProvider + `!`, 200, signin.ErrCaptcha},
		{"wrong_hostname", strings.Replace(validProvider, "identity.example.invalid", "other.example.invalid", 1), 200, signin.ErrInvalid},
		{"wrong_action", strings.Replace(validProvider, `"action":"identity"`, `"action":"login"`, 1), 200, signin.ErrInvalid},
		{"wrong_site", strings.Replace(validProvider, "owned-site", "other-site", 1), 200, signin.ErrInvalid},
		{"missing_site", strings.Replace(validProvider, `"cdata":"owned-site",`, "", 1), 200, signin.ErrInvalid},
		{"old_timestamp", strings.Replace(validProvider, "03:04:00", "02:59:05", 1), 200, signin.ErrInvalid},
		{"future_timestamp", strings.Replace(validProvider, "03:04:00", "03:04:36", 1), 200, signin.ErrInvalid},
		{"bad_timestamp", strings.Replace(validProvider, "2026-01-02T03:04:00Z", "invalid", 1), 200, signin.ErrInvalid},
		{"success_with_error", strings.Replace(validProvider, `"error-codes":[]`, `"error-codes":["invalid-input-response"]`, 1), 200, signin.ErrCaptcha},
		{"wrong_type", strings.Replace(validProvider, `"error-codes":[]`, `"error-codes":true`, 1), 200, signin.ErrCaptcha},
		{"redirect", validProvider, 302, signin.ErrCaptcha},
		{"forbidden", validProvider, 403, signin.ErrCaptcha},
		{"oversized", validProvider + strings.Repeat(" ", maxProviderResponse), 200, signin.ErrCaptcha},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			v := testCaptcha(t, roundTrip(func(*http.Request) (*http.Response, error) { calls++; return providerResponse(tt.status, tt.body), nil }))
			if e := v.Verify(context.Background(), "synthetic-only-token", "192.0.2.1"); !errors.Is(e, tt.want) || calls != 1 {
				t.Fatal("unexpected decision", e, calls)
			}
		})
	}
}

func TestTurnstileRetriesWithOneIdempotencyKey(t *testing.T) {
	for _, failure := range []string{"network", "server", "internal"} {
		t.Run(failure, func(t *testing.T) {
			calls := 0
			var first url.Values
			v := testCaptcha(t, roundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				_ = r.ParseForm()
				if calls == 1 {
					first = r.PostForm
					switch failure {
					case "network":
						return nil, errors.New("synthetic provider failure containing secret")
					case "server":
						return providerResponse(503, "unavailable"), nil
					default:
						return providerResponse(200, `{"success":false,"error-codes":["internal-error"]}`), nil
					}
				}
				if r.PostForm.Encode() != first.Encode() {
					t.Fatal("retry changed idempotency or proof")
				}
				return providerResponse(200, validProvider), nil
			}))
			if e := v.Verify(context.Background(), "synthetic-only-token", "192.0.2.1"); e != nil || calls != 2 {
				t.Fatal(e, calls)
			}
		})
	}
	calls := 0
	v := testCaptcha(t, roundTrip(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("synthetic failure") }))
	if e := v.Verify(context.Background(), "synthetic-only-token", "192.0.2.1"); e != signin.ErrCaptcha || calls != 2 {
		t.Fatal("unbounded retry", e, calls)
	}
}

func TestTurnstileInputCancellationAndTimeout(t *testing.T) {
	calls := 0
	v := testCaptcha(t, roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))
	for _, input := range [][2]string{{"", "192.0.2.1"}, {strings.Repeat("a", 2049), "192.0.2.1"}, {"synthetic\ntoken", "192.0.2.1"}, {"synthetic", "unknown"}, {"synthetic", "192.0.2.1:443"}, {"synthetic", "fe80::1%eth0"}} {
		if e := v.Verify(context.Background(), input[0], input[1]); e != signin.ErrInvalid {
			t.Fatal(e)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := v.Verify(ctx, "synthetic", "192.0.2.1"); e != signin.ErrCaptcha || calls != 0 {
		t.Fatal("canceled request reached transport", e, calls)
	}
	start := time.Now()
	if e := v.Verify(context.Background(), "synthetic", "192.0.2.1"); e != signin.ErrCaptcha || calls != 1 || time.Since(start) > 2*time.Second {
		t.Fatal("timeout not bounded", e, calls)
	}
}

func TestTurnstileConfigurationAndTransport(t *testing.T) {
	for _, mutate := range []func(*TurnstileConfig){
		func(c *TurnstileConfig) { c.Secret = "" }, func(c *TurnstileConfig) { c.Secret = "secret\n" },
		func(c *TurnstileConfig) { c.Hostname = "*.example.invalid" }, func(c *TurnstileConfig) { c.Hostname = "https://example.invalid" },
		func(c *TurnstileConfig) { c.Action = "" }, func(c *TurnstileConfig) { c.SiteBinding = "" },
		func(c *TurnstileConfig) { c.Action = strings.Repeat("a", 33) }, func(c *TurnstileConfig) { c.SiteBinding = strings.Repeat("a", 256) },
		func(c *TurnstileConfig) { c.Timeout = 0 }, func(c *TurnstileConfig) { c.Timeout = time.Minute },
	} {
		c := captchaConfig()
		mutate(&c)
		if _, e := NewTurnstile(c, nil); e != ErrConfiguration {
			t.Fatal("invalid configuration accepted")
		}
	}
	v, e := NewTurnstile(captchaConfig(), nil)
	if e != nil {
		t.Fatal(e)
	}
	tr := v.client.Transport.(*http.Transport)
	if tr.Proxy != nil || tr.TLSClientConfig.InsecureSkipVerify || tr.TLSClientConfig.MinVersion == 0 || tr.ResponseHeaderTimeout == 0 {
		t.Fatal("unsafe default transport")
	}
	if v.client.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("redirects enabled")
	}
	v = testCaptcha(t, roundTrip(func(*http.Request) (*http.Response, error) {
		r := providerResponse(200, validProvider)
		r.Header.Set("Content-Type", "text/html")
		return r, nil
	}))
	if e = v.Verify(context.Background(), "synthetic", "192.0.2.1"); e != signin.ErrCaptcha {
		t.Fatal("accepted non-JSON provider")
	}
}
