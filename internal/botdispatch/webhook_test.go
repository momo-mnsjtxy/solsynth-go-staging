// SPDX-License-Identifier: AGPL-3.0-only
package botdispatch

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"solsynth.local/go-migration/ports/develop/bots"
	"strings"
	"testing"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestWebhookBoundSignedAcceptance(t *testing.T) {
	const bot = "00000000-0000-4000-8000-000000000001"
	event := bots.MessageDelivery{EventID: "00000000-0000-4000-8000-000000000002", BotID: bot, Content: "/ping synthetic"}
	key := bytes.Repeat([]byte{7}, 32)
	calls := 0
	status := 202
	client := &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		calls++
		body, e := io.ReadAll(r.Body)
		if e != nil {
			t.Fatal(e)
		}
		mac := hmac.New(sha256.New, bytes.Repeat([]byte{7}, 32))
		mac.Write(body)
		want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if r.Header.Get("X-Bot-Signature") != want || r.Header.Get("Idempotency-Key") != event.EventID || r.URL.String() != "https://synthetic.invalid/hook" {
			t.Fatal("signature/binding")
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("synthetic")), Header: http.Header{"Location": {"https://other.invalid"}}, Request: r}, nil
	})}
	w, e := New(Config{Name: "test", BotID: bot, Endpoint: "https://synthetic.invalid/hook", SigningKey: key, Client: client})
	if e != nil {
		t.Fatal(e)
	}
	key[0] = 4
	rotated, e := New(Config{Name: "test", BotID: bot, Endpoint: "https://synthetic.invalid/hook", SigningKey: key, Client: client})
	if e != nil || rotated.BindingID() == w.BindingID() {
		t.Fatal("adapter rotation retained binding", e)
	}
	if e = w.Deliver(context.Background(), event); e != nil {
		t.Fatal(e)
	}
	status = 302
	if e = w.Deliver(context.Background(), event); !errors.Is(e, ErrDelivery) || calls != 2 {
		t.Fatal("redirect followed or accepted", calls, e)
	}
	event.BotID = event.EventID
	if e = w.Deliver(context.Background(), event); !errors.Is(e, ErrDelivery) || calls != 2 {
		t.Fatal("cross-bot provider invocation")
	}
	if client.CheckRedirect != nil {
		t.Fatal("mutated host client")
	}
}
func TestWebhookRejectsUnconfiguredAndNetworkFailure(t *testing.T) {
	for _, endpoint := range []string{"http://synthetic.invalid", "https://user:secret@synthetic.invalid", "https://synthetic.invalid?secret=x", "https://synthetic.invalid/#fragment", "/hook"} {
		if _, e := New(Config{Name: "test", BotID: "00000000-0000-4000-8000-000000000001", Endpoint: endpoint, SigningKey: bytes.Repeat([]byte{7}, 32)}); e == nil {
			t.Fatal(endpoint)
		}
	}
	w, e := New(Config{Name: "test", BotID: "00000000-0000-4000-8000-000000000001", Endpoint: "https://synthetic.invalid", SigningKey: bytes.Repeat([]byte{7}, 32), Client: &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("synthetic private provider failure")
	})}})
	if e != nil {
		t.Fatal(e)
	}
	if e = w.Deliver(context.Background(), bots.MessageDelivery{BotID: w.BotID(), EventID: "00000000-0000-4000-8000-000000000002"}); e != ErrDelivery {
		t.Fatal("provider data leaked", e)
	}
}

func TestWebhookFrozenConfigurationAndMetadata(t *testing.T) {
	const bot = "00000000-0000-4000-8000-000000000001"
	calls := 0
	client := &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		calls++
		body, e := io.ReadAll(r.Body)
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(string(body), "private-token") || !strings.Contains(string(body), `"meta":null`) {
			t.Fatal("metadata escaped", string(body))
		}
		return &http.Response{StatusCode: 202, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header), Request: r}, nil
	})}
	config := Config{Name: "frozen", BotID: bot, Endpoint: "https://synthetic.invalid/hook", SigningKey: bytes.Repeat([]byte{8}, 32), Client: client}
	w, e := New(config)
	if e != nil {
		t.Fatal(e)
	}
	client.Transport = transport(func(*http.Request) (*http.Response, error) { t.Fatal("mutated client used"); return nil, ErrDelivery })
	value := bots.MessageDelivery{EventID: "00000000-0000-4000-8000-000000000002", BotID: bot, Meta: []byte(`{"token":"private-token"}`)}
	if e = w.Deliver(context.Background(), value); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e = w.Deliver(ctx, value); !errors.Is(e, ErrDelivery) || calls != 1 {
		t.Fatal("cancelled transport invoked", calls, e)
	}
	config.Endpoint = "https://other.invalid/hook"
	other, e := New(config)
	if e != nil || w.BindingID() == other.BindingID() {
		t.Fatal("destination change reused binding", e)
	}
	standard := &http.Transport{}
	clone, e := New(Config{Name: "clone", BotID: bot, Endpoint: "https://synthetic.invalid", SigningKey: bytes.Repeat([]byte{9}, 32), Client: &http.Client{Transport: standard}})
	if e != nil || clone.client.Transport == standard {
		t.Fatal("standard transport not snapshotted", e)
	}
}
