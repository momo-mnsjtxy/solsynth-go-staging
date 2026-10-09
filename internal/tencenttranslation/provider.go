// SPDX-License-Identifier: AGPL-3.0-only
package tencenttranslation

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"solsynth.local/go-migration/ports/sphere/reader"
	"solsynth.local/go-migration/ports/sphere/translation"
)

const host = "tmt.tencentcloudapi.com"
const contentType = "application/json; charset=utf-8"

type Config struct {
	SecretID      string
	SecretKey     []byte
	Region        string
	ProjectID     int64
	Timeout       time.Duration
	MaxConcurrent int
	Now           func() time.Time
}
type resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}
type Provider struct {
	config   Config
	resolver resolver
	exchange func(context.Context, *http.Request, netip.Addr) (*http.Response, error)
	slots    chan struct{}
	mu       sync.Mutex
	next     time.Time
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9]{8,128}$`)
var regionPattern = regexp.MustCompile(`^[a-z]{2}-[a-z0-9-]{2,32}$`)

// New is the explicit outbound opt-in. It reads no environment variables,
// creates no credentials and performs no network I/O during construction.
func New(c Config) (*Provider, error) {
	if !idPattern.MatchString(c.SecretID) || len(c.SecretKey) < 16 || len(c.SecretKey) > 256 || !regionPattern.MatchString(c.Region) || c.ProjectID < 0 {
		return nil, translation.ErrInvalid
	}
	if c.Timeout == 0 {
		c.Timeout = 5 * time.Second
	}
	if c.MaxConcurrent == 0 {
		c.MaxConcurrent = 2
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Timeout < time.Second || c.Timeout > 10*time.Second || c.MaxConcurrent < 1 || c.MaxConcurrent > 8 {
		return nil, translation.ErrInvalid
	}
	c.SecretKey = append([]byte{}, c.SecretKey...)
	p := &Provider{config: c, resolver: net.DefaultResolver, slots: make(chan struct{}, c.MaxConcurrent)}
	p.exchange = p.send
	return p, nil
}
func (p *Provider) Namespace() string {
	if p == nil {
		return ""
	}
	return "tencent-tmt:20180321:" + p.config.Region + ":" + strconv.FormatInt(p.config.ProjectID, 10)
}
func digest(data []byte) string { s := sha256.Sum256(data); return hex.EncodeToString(s[:]) }
func mac(key []byte, text string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(text))
	return h.Sum(nil)
}
func authorization(host, secretID string, secretKey, body []byte, now time.Time) string {
	date := now.UTC().Format("2006-01-02")
	scope := date + "/tmt/tc3_request"
	canonical := "POST\n/\n\ncontent-type:" + contentType + "\nhost:" + host + "\n\ncontent-type;host\n" + digest(body)
	toSign := "TC3-HMAC-SHA256\n" + strconv.FormatInt(now.Unix(), 10) + "\n" + scope + "\n" + digest([]byte(canonical))
	seed := append([]byte("TC3"), secretKey...)
	defer clear(seed)
	dateKey := mac(seed, date)
	defer clear(dateKey)
	serviceKey := mac(dateKey, "tmt")
	defer clear(serviceKey)
	signingKey := mac(serviceKey, "tc3_request")
	defer clear(signingKey)
	return "TC3-HMAC-SHA256 Credential=" + secretID + "/" + scope + ", SignedHeaders=content-type;host, Signature=" + hex.EncodeToString(mac(signingKey, toSign))
}
func (p *Provider) Translate(ctx context.Context, input translation.Request) (string, error) {
	if p == nil {
		return "", translation.ErrUnavailable
	}
	req, e := input.Normalize()
	if e != nil {
		return "", e
	}
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		return "", translation.ErrBusy
	}
	p.mu.Lock()
	now := p.config.Now()
	if now.Before(p.next) {
		p.mu.Unlock()
		return "", translation.ErrBusy
	}
	p.next = now.Add(200 * time.Millisecond)
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()
	addresses, e := p.resolver.LookupNetIP(ctx, "ip", host)
	if e != nil || len(addresses) == 0 {
		return "", translation.ErrProvider
	}
	if len(addresses) > 16 {
		return "", translation.ErrProvider
	}
	for _, address := range addresses {
		if !reader.PublicAddress(address) {
			return "", translation.ErrProvider
		}
	}
	body, e := json.Marshal(struct {
		SourceText string
		Source     string
		Target     string
		ProjectId  int64
	}{req.Text, req.Source, req.Target, p.config.ProjectID})
	if e != nil {
		return "", translation.ErrInvalid
	}
	request, e := http.NewRequestWithContext(ctx, "POST", "https://"+host+"/", bytes.NewReader(body))
	if e != nil {
		return "", translation.ErrProvider
	}
	request.Host = host
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("User-Agent", "SolsynthOwnedTranslation/1.0")
	now = p.config.Now().UTC()
	request.Header.Set("Authorization", authorization(host, p.config.SecretID, p.config.SecretKey, body, now))
	request.Header.Set("X-TC-Timestamp", strconv.FormatInt(now.Unix(), 10))
	request.Header.Set("X-TC-Action", "TextTranslate")
	request.Header.Set("X-TC-Version", "2018-03-21")
	request.Header.Set("X-TC-Region", p.config.Region)
	response, e := p.exchange(ctx, request, addresses[0].Unmap())
	if e != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		return "", translation.ErrProvider
	}
	if response == nil || response.Body == nil {
		return "", translation.ErrProvider
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.ContentLength > 65536 || response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity" {
		return "", translation.ErrProvider
	}
	ct, _, e := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if e != nil || ct != "application/json" {
		return "", translation.ErrProvider
	}
	data, e := io.ReadAll(io.LimitReader(response.Body, 65537))
	if e != nil || len(data) > 65536 || !utf8.Valid(data) || ctx.Err() != nil {
		return "", translation.ErrProvider
	}
	var out struct {
		Response *struct {
			TargetText string
			Source     string
			Target     string
			Error      *struct{ Code, Message string }
		}
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if e = d.Decode(&out); e != nil {
		return "", translation.ErrProvider
	}
	if _, e = d.Token(); e != io.EOF {
		return "", translation.ErrProvider
	}
	if out.Response == nil || out.Response.Error != nil || strings.TrimSpace(out.Response.TargetText) == "" || len(out.Response.TargetText) > 32000 || !strings.EqualFold(out.Response.Target, req.Target) {
		return "", translation.ErrProvider
	}
	return out.Response.TargetText, nil
}
func (p *Provider) send(ctx context.Context, request *http.Request, address netip.Addr) (*http.Response, error) {
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}, DisableKeepAlives: true, DisableCompression: true, MaxResponseHeaderBytes: 16384, ResponseHeaderTimeout: p.config.Timeout, DialContext: func(ctx context.Context, network, destination string) (net.Conn, error) {
		if destination != net.JoinHostPort(host, "443") {
			return nil, errors.New("translation destination changed")
		}
		return (&net.Dialer{Timeout: p.config.Timeout}).DialContext(ctx, network, net.JoinHostPort(address.String(), "443"))
	}}
	response, e := transport.RoundTrip(request)
	if e != nil {
		transport.CloseIdleConnections()
		return nil, e
	}
	response.Body = &responseBody{ReadCloser: response.Body, transport: transport}
	return response, nil
}

type responseBody struct {
	io.ReadCloser
	transport *http.Transport
}

func (b *responseBody) Close() error {
	e := b.ReadCloser.Close()
	b.transport.CloseIdleConnections()
	return e
}
