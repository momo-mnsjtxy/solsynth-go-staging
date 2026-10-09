// SPDX-License-Identifier: AGPL-3.0-only
package pushprovider

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"solsynth.local/go-migration/ports/notifications"
	"strings"
	"sync"
	"time"
)

var projectID = regexp.MustCompile(`^[a-z][a-z0-9-]{4,62}[a-z0-9]$`)

type FCMConfig struct{ ProjectID, ServiceAccountEmail, PrivateKeyPEM string }
type FCM struct {
	cfg     FCMConfig
	key     *rsa.PrivateKey
	client  *http.Client
	mu      sync.Mutex
	token   string
	expires time.Time
	now     func() time.Time
}

func NewFCM(c FCMConfig, client *http.Client) (*FCM, error) {
	if !projectID.MatchString(c.ProjectID) || len(c.ServiceAccountEmail) > 320 || !strings.HasSuffix(c.ServiceAccountEmail, ".iam.gserviceaccount.com") || strings.ContainsAny(c.ServiceAccountEmail, " \x00\r\n") {
		return nil, notifications.ErrInvalid
	}
	block, rest := pem.Decode([]byte(c.PrivateKeyPEM))
	if block == nil || len(strings.TrimSpace(string(rest))) != 0 || block.Type != "PRIVATE KEY" {
		return nil, notifications.ErrInvalid
	}
	parsed, e := x509.ParsePKCS8PrivateKey(block.Bytes)
	if e != nil {
		return nil, notifications.ErrInvalid
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok || key.N.BitLen() < 2048 || key.N.BitLen() > 8192 || key.Validate() != nil {
		return nil, notifications.ErrInvalid
	}
	if client == nil {
		client = &http.Client{}
	}
	bounded := *client
	bounded.Timeout = 10 * time.Second
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.PrivateKeyPEM = ""
	return &FCM{cfg: c, key: key, client: &bounded, now: time.Now}, nil
}
func parseResponse(resp *http.Response, out any) error {
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return notifications.ErrDeliveryUnconfigured
	}
	raw, e := io.ReadAll(io.LimitReader(resp.Body, 64<<10+1))
	if e != nil || len(raw) > 64<<10 {
		return notifications.ErrDeliveryUnconfigured
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	if decoder.Decode(out) != nil {
		return notifications.ErrDeliveryUnconfigured
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return notifications.ErrDeliveryUnconfigured
	}
	return nil
}
func (f *FCM) access(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now().UTC()
	if f.token != "" && now.Add(time.Minute).Before(f.expires) {
		return f.token, nil
	}
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{"iss": f.cfg.ServiceAccountEmail, "scope": "https://www.googleapis.com/auth/firebase.messaging", "aud": "https://oauth2.googleapis.com/token", "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, e := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if e != nil {
		return "", notifications.ErrDeliveryUnconfigured
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)}}
	req, e := http.NewRequestWithContext(ctx, "POST", "https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	if e != nil {
		return "", notifications.ErrDeliveryUnconfigured
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, e := f.client.Do(req)
	if e != nil {
		return "", notifications.ErrDeliveryUnconfigured
	}
	var token struct {
		Access  string `json:"access_token"`
		Type    string `json:"token_type"`
		Seconds int    `json:"expires_in"`
	}
	if e = parseResponse(resp, &token); e != nil {
		return "", e
	}
	if token.Access == "" || len(token.Access) > 16384 || strings.ContainsAny(token.Access, "\x00\r\n") || !strings.EqualFold(token.Type, "Bearer") || token.Seconds < 60 || token.Seconds > 3600 {
		return "", notifications.ErrDeliveryUnconfigured
	}
	f.token = token.Access
	f.expires = f.now().UTC().Add(time.Duration(token.Seconds) * time.Second)
	return f.token, nil
}
func (f *FCM) SendDevice(ctx context.Context, token string, data map[string]string) error {
	if f == nil || len(token) < 16 || len(token) > 4096 || strings.ContainsAny(token, " \x00\r\n") || len(data) < 1 || len(data) > 8 {
		return notifications.ErrInvalid
	}
	for k, v := range data {
		if len(k) > 128 || len(v) > 2048 || strings.ContainsRune(k+v, 0) {
			return notifications.ErrInvalid
		}
	}
	access, e := f.access(ctx)
	if e != nil {
		return e
	}
	raw, e := json.Marshal(map[string]any{"message": map[string]any{"token": token, "data": data}})
	if e != nil || len(raw) > 8192 {
		return notifications.ErrInvalid
	}
	req, e := http.NewRequestWithContext(ctx, "POST", "https://fcm.googleapis.com/v1/projects/"+f.cfg.ProjectID+"/messages:send", strings.NewReader(string(raw)))
	if e != nil {
		return notifications.ErrDeliveryUnconfigured
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+access)
	resp, e := f.client.Do(req)
	if e != nil {
		return notifications.ErrDeliveryUnconfigured
	}
	var accepted struct {
		Name string `json:"name"`
	}
	if e = parseResponse(resp, &accepted); e != nil {
		return e
	}
	if !strings.HasPrefix(accepted.Name, "projects/"+f.cfg.ProjectID+"/messages/") || len(accepted.Name) > 2048 {
		return notifications.ErrDeliveryUnconfigured
	}
	return nil
}
