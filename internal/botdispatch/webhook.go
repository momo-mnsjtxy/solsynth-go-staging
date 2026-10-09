// SPDX-License-Identifier: AGPL-3.0-only
// Package botdispatch provides an explicitly configured webhook transport for
// the owned bot delivery projection. Construction never performs network I/O.
package botdispatch

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/bots"
)

var ErrDelivery = errors.New("bot webhook did not accept delivery")

type Config struct {
	Name, BotID, Endpoint string
	SigningKey            []byte
	Client                *http.Client
}
type Webhook struct {
	name, bot, endpoint, binding string
	key                          []byte
	client                       *http.Client
}

func New(c Config) (*Webhook, error) {
	u, e := url.Parse(c.Endpoint)
	if e != nil || !bots.ValidAdapterName(c.Name) || !authority.ValidID(c.BotID) || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.ForceQuery || u.Opaque != "" || strings.ContainsAny(c.Endpoint, "\r\n") || len(c.SigningKey) < 32 || len(c.SigningKey) > 128 {
		return nil, bots.ErrInvalid
	}
	client := &http.Client{Timeout: 10 * time.Second}
	if c.Client != nil {
		clone := *c.Client
		client = &clone
	}
	// Snapshot standard transports as well as the client. Arbitrary explicitly
	// supplied RoundTrippers are host-owned dependencies and must be immutable.
	if client.Transport == nil {
		base, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return nil, bots.ErrInvalid
		}
		client.Transport = base.Clone()
	} else if base, ok := client.Transport.(*http.Transport); ok {
		client.Transport = base.Clone()
	}
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if client.Timeout <= 0 || client.Timeout > 30*time.Second {
		client.Timeout = 10 * time.Second
	}
	fingerprint := sha256.Sum256(append([]byte(c.Name+"\x00"+c.BotID+"\x00"+u.String()+"\x00"), c.SigningKey...))
	return &Webhook{binding: "v" + hex.EncodeToString(fingerprint[:])[:63], name: c.Name, bot: c.BotID, endpoint: u.String(), key: append([]byte{}, c.SigningKey...), client: client}, nil
}
func (w *Webhook) Name() string {
	if w == nil {
		return ""
	}
	return w.name
}
func (w *Webhook) BotID() string {
	if w == nil {
		return ""
	}
	return w.bot
}
func (w *Webhook) Deliver(ctx context.Context, v bots.MessageDelivery) error {
	if w == nil || v.BotID != w.bot || !authority.ValidID(v.EventID) {
		return ErrDelivery
	}
	if ctx.Err() != nil {
		return ErrDelivery
	}
	// The owned text projection never conveys arbitrary client metadata.
	v.Meta = json.RawMessage(`null`)
	body, e := json.Marshal(v)
	if e != nil || len(body) > 131072 {
		return ErrDelivery
	}
	request, e := http.NewRequestWithContext(ctx, http.MethodPost, w.endpoint, bytes.NewReader(body))
	if e != nil {
		return ErrDelivery
	}
	mac := hmac.New(sha256.New, w.key)
	_, _ = mac.Write(body)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Bot-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	request.Header.Set("Idempotency-Key", v.EventID)
	request.Header.Set("X-Solsynth-Projection", "owned-bot-message-v1")
	response, e := w.client.Do(request)
	if e != nil {
		return ErrDelivery
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if ctx.Err() != nil {
		return ErrDelivery
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ErrDelivery
	}
	return nil
}

func (w *Webhook) BindingID() string {
	if w == nil {
		return ""
	}
	return w.binding
}
