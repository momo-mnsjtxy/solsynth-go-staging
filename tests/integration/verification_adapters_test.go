// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/signinpg"
	"solsynth.local/go-migration/internal/verification"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
)

func verificationDatabase(t *testing.T) *sql.DB {
	t.Helper()
	// database checks the marked disposable cluster before any provider listener.
	db := database(t)
	if os.Getenv("CI") != "true" {
		t.Fatal("verification provider sockets are restricted to synthetic CI")
	}
	return db
}

type verificationSMTPFixture struct {
	address  string
	roots    *x509.CertPool
	messages chan []byte
	reject   atomic.Bool
}

func verificationSMTPServer(t *testing.T, mode string) *verificationSMTPFixture {
	t.Helper()
	if os.Getenv("CI") != "true" || os.Getenv("PG_TEST_REQUIRED") != "1" {
		t.Fatal("synthetic CI guard required before SMTP listener")
	}
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"smtp.example.invalid"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, e := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	leaf, e := x509.ParseCertificate(der)
	if e != nil {
		t.Fatal(e)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	cfg := &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	fixture := &verificationSMTPFixture{address: listener.Addr().String(), roots: roots, messages: make(chan []byte, 16)}
	var wg sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			connection, e := listener.Accept()
			if e != nil {
				return
			}
			wg.Add(1)
			go func() { defer wg.Done(); verificationSMTPConversation(connection, cfg, mode, fixture) }()
		}
	}()
	t.Cleanup(func() { listener.Close(); <-done; wg.Wait() })
	return fixture
}
func verificationSMTPConversation(conn net.Conn, cfg *tls.Config, mode string, fixture *verificationSMTPFixture) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	encrypted := false
	if mode == "implicit" {
		secure := tls.Server(conn, cfg)
		if secure.Handshake() != nil {
			return
		}
		conn = secure
		encrypted = true
	}
	reader := textproto.NewReader(bufio.NewReader(conn))
	writer := textproto.NewWriter(bufio.NewWriter(conn))
	_ = writer.PrintfLine("220 smtp.example.invalid synthetic only")
	authenticated, recipient := false, false
	for {
		line, e := reader.ReadLine()
		if e != nil {
			return
		}
		switch {
		case strings.HasPrefix(line, "EHLO "):
			_ = writer.PrintfLine("250-smtp.example.invalid")
			if !encrypted {
				_ = writer.PrintfLine("250-STARTTLS")
			}
			_ = writer.PrintfLine("250 AUTH PLAIN")
		case line == "STARTTLS":
			if encrypted {
				return
			}
			_ = writer.PrintfLine("220 upgrade")
			secure := tls.Server(conn, cfg)
			if secure.Handshake() != nil {
				return
			}
			conn = secure
			encrypted = true
			reader = textproto.NewReader(bufio.NewReader(conn))
			writer = textproto.NewWriter(bufio.NewWriter(conn))
		case strings.HasPrefix(line, "AUTH PLAIN "):
			raw, e := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "AUTH PLAIN "))
			if !encrypted || e != nil || string(raw) != "\x00synthetic-user\x00synthetic-password" {
				_ = writer.PrintfLine("535 rejected")
				return
			}
			authenticated = true
			_ = writer.PrintfLine("235 accepted")
		case line == "MAIL FROM:<sender@example.invalid>":
			if !encrypted || !authenticated {
				return
			}
			_ = writer.PrintfLine("250 accepted")
		case strings.HasPrefix(line, "RCPT TO:<") && strings.HasSuffix(line, "@example.invalid>"):
			if fixture.reject.Load() {
				_ = writer.PrintfLine("550 synthetic rejected recipient")
				continue
			}
			recipient = true
			_ = writer.PrintfLine("250 accepted")
		case line == "DATA":
			if !encrypted || !authenticated || !recipient {
				return
			}
			_ = writer.PrintfLine("354 body")
			body, e := io.ReadAll(io.LimitReader(reader.DotReader(), (16<<10)+1))
			if e != nil || len(body) > 16<<10 {
				return
			}
			fixture.messages <- body
			_ = writer.PrintfLine("250 queued")
		case line == "QUIT":
			_ = writer.PrintfLine("221 done")
			return
		default:
			_ = writer.PrintfLine("500 unsupported")
		}
	}
}
func (f *verificationSMTPFixture) config(mode string) verification.SMTPConfig {
	return verification.SMTPConfig{Address: f.address, ServerName: "smtp.example.invalid", TLSMode: mode, Auth: "plain", Username: "synthetic-user", Password: "synthetic-password", From: "sender@example.invalid", Name: "Synthetic verification", VerifyURL: "https://identity.example.invalid/auth/verify", ResetURL: "https://identity.example.invalid/auth/reset", Timeout: 2 * time.Second, RootCAs: f.roots}
}
func verificationDeliveredCode(t *testing.T, f *verificationSMTPFixture, path string) string {
	t.Helper()
	var raw []byte
	select {
	case raw = <-f.messages:
	case <-time.After(3 * time.Second):
		t.Fatal("synthetic relay did not accept a message")
	}
	message, e := mail.ReadMessage(bytes.NewReader(raw))
	if e != nil {
		t.Fatal(e)
	}
	body, e := io.ReadAll(quotedprintable.NewReader(message.Body))
	if e != nil {
		t.Fatal(e)
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "https://") {
			continue
		}
		u, e := url.Parse(line)
		if e != nil {
			t.Fatal(e)
		}
		if u.Host != "identity.example.invalid" || u.Path != path || len(u.Query()) != 1 {
			t.Fatal("incorrect completion link")
		}
		code := u.Query().Get("code")
		decoded, e := base64.RawURLEncoding.DecodeString(code)
		if e != nil || len(decoded) != 32 {
			t.Fatal("bad one-use code")
		}
		return code
	}
	t.Fatal("missing completion link")
	return ""
}

type verificationHTTPTransport struct {
	target    *url.URL
	transport http.RoundTripper
}

func (t verificationHTTPTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.String() != "https://challenges.cloudflare.com/turnstile/v0/siteverify" {
		return nil, errors.New("unexpected provider target")
	}
	// The sole synthetic transport rewrites the fixed provider to the exact
	// loopback TLS server created below, using only its pinned fixture roots.
	req := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = t.target.Scheme
	u.Host = t.target.Host
	req.URL = &u
	req.Host = t.target.Host
	return t.transport.RoundTrip(req)
}
func verificationCaptchaServer(t *testing.T) *verification.Turnstile {
	t.Helper()
	if os.Getenv("CI") != "true" || os.Getenv("PG_TEST_REQUIRED") != "1" {
		t.Fatal("synthetic CI guard required before HTTP listener")
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != "POST" || r.URL.Path != "/turnstile/v0/siteverify" || r.ParseForm() != nil || r.PostForm.Get("secret") != "synthetic-provider-secret" || r.PostForm.Get("idempotency_key") == "" {
			w.WriteHeader(400)
			return
		}
		switch r.PostForm.Get("response") {
		case "synthetic-accepted":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "hostname": "identity.example.invalid", "action": "identity", "cdata": "owned-site", "challenge_ts": time.Now().UTC().Format(time.RFC3339Nano)})
		case "synthetic-wrong-site":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "hostname": "identity.example.invalid", "action": "identity", "cdata": "other-site", "challenge_ts": time.Now().UTC().Format(time.RFC3339Nano)})
		case "synthetic-unavailable":
			w.WriteHeader(503)
		default:
			_, _ = io.WriteString(w, `{"success":false,"error-codes":["invalid-input-response"]}`)
		}
	}))
	t.Cleanup(server.Close)
	target, e := url.Parse(server.URL)
	if e != nil {
		t.Fatal(e)
	}
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.Proxy = nil
	v, e := verification.NewTurnstile(verification.TurnstileConfig{Secret: "synthetic-provider-secret", Hostname: "identity.example.invalid", Action: "identity", SiteBinding: "owned-site", Timeout: 2 * time.Second}, verificationHTTPTransport{target, transport})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(transport.CloseIdleConnections)
	return v
}
func verificationSignin(t *testing.T, db *sql.DB, captcha signin.Captcha, delivery signin.Delivery) *signin.Service {
	t.Helper()
	if _, e := db.Exec(authoritypg.Schema + signinpg.Schema); e != nil {
		t.Fatal(e)
	}
	now := &atomic.Int64{}
	now.Store(time.Now().Unix())
	keys := authorityKeys(t)
	cfg := authorityConfig(t, keys, now)
	canonical := authoritypg.New(db)
	issuer := newPGAuthority(t, db, keys, now)
	verifier, e := authority.NewVerifier(canonical, cfg)
	if e != nil {
		t.Fatal(e)
	}
	store := signinpg.New(db, canonical, authoritypg.NewTransactionGuard(canonical, verifier))
	seal := sha256.Sum256([]byte("synthetic-verification-adapter-only"))
	service, e := signin.New(store, issuer, verifier, signin.Config{SealKey: seal[:], Issuer: "Synthetic verification", Captcha: captcha, Delivery: delivery})
	if e != nil {
		t.Fatal(e)
	}
	return service
}

func TestPGVerificationAdaptersHTTPFlow(t *testing.T) {
	db := verificationDatabase(t)
	relay := verificationSMTPServer(t, "implicit")
	delivery, e := verification.NewSMTP(relay.config("implicit"))
	if e != nil {
		t.Fatal(e)
	}
	service := verificationSignin(t, db, verificationCaptchaServer(t), delivery)
	peer := verificationPeer(t)
	baseHandler := service.Handler()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = net.JoinHostPort(peer, "54321")
		baseHandler.ServeHTTP(w, r)
	})
	name := "adapter_" + strings.ReplaceAll(authorityUUID(t), "-", "")[:20]
	email := name + "@example.invalid"
	request := func(captcha string) string {
		return fmt.Sprintf(`{"name":%q,"email":%q,"password":"synthetic password only 1234","captcha_token":%q}`, name, email, captcha)
	}
	for _, captcha := range []string{"synthetic-rejected", "synthetic-wrong-site"} {
		backendCall(t, handler, "POST", "/stargate/accounts", "", request(captcha), 401)
	}
	backendCall(t, handler, "POST", "/stargate/accounts", "", request("synthetic-unavailable"), 503)
	var count int
	if e = db.QueryRow(`SELECT count(*) FROM owned_authority_accounts WHERE name=$1`, name).Scan(&count); e != nil || count != 0 {
		t.Fatal("failed CAPTCHA created account", e)
	}
	backendCall(t, handler, "POST", "/stargate/accounts", "", request("synthetic-accepted"), 202)
	code := verificationDeliveredCode(t, relay, "/auth/verify")
	backendCall(t, handler, "GET", "/stargate/accounts/verify?code="+url.QueryEscape(code), "", "", 405)
	backendCall(t, handler, "POST", "/stargate/accounts/verify", "", fmt.Sprintf(`{"code":%q}`, code), 204)
	backendCall(t, handler, "POST", "/stargate/accounts/verify", "", fmt.Sprintf(`{"code":%q}`, code), 401)
	backendCall(t, handler, "POST", "/passport/accounts/recovery/password", "", fmt.Sprintf(`{"account":%q,"captcha_token":"synthetic-accepted"}`, name), 202)
	reset := verificationDeliveredCode(t, relay, "/auth/reset")
	backendCall(t, handler, "POST", "/stargate/accounts/verify", "", fmt.Sprintf(`{"code":%q}`, reset), 401)
	body := fmt.Sprintf(`{"code":%q,"password":"synthetic replacement password 1234"}`, reset)
	backendCall(t, handler, "PATCH", "/passport/accounts/recovery/password", "", body, 204)
	backendCall(t, handler, "PATCH", "/passport/accounts/recovery/password", "", body, 401)
}

func TestPGVerificationSMTPRequiredTLSAndFailure(t *testing.T) {
	_ = verificationDatabase(t)
	for _, mode := range []string{"implicit", "starttls"} {
		t.Run(mode, func(t *testing.T) {
			relay := verificationSMTPServer(t, mode)
			cfg := relay.config(mode)
			delivery, e := verification.NewSMTP(cfg)
			if e != nil {
				t.Fatal(e)
			}
			token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32))
			if e = delivery.Send(context.Background(), "recipient@example.invalid", "verify_email", token); e != nil {
				t.Fatal(e)
			}
			if got := verificationDeliveredCode(t, relay, "/auth/verify"); got != token {
				t.Fatal("token changed in transit")
			}
			cfg.ServerName = "wrong.example.invalid"
			bad, e := verification.NewSMTP(cfg)
			if e != nil {
				t.Fatal(e)
			}
			if e = bad.Send(context.Background(), "recipient@example.invalid", "verify_email", token); e != signin.ErrDelivery {
				t.Fatal("unverified server certificate accepted", e)
			}
			cfg = relay.config(mode)
			cfg.RootCAs = x509.NewCertPool()
			bad, e = verification.NewSMTP(cfg)
			if e != nil {
				t.Fatal(e)
			}
			if e = bad.Send(context.Background(), "recipient@example.invalid", "verify_email", token); e != signin.ErrDelivery {
				t.Fatal("untrusted certificate accepted", e)
			}
			relay.reject.Store(true)
			if e = delivery.Send(context.Background(), "recipient@example.invalid", "verify_email", token); e != signin.ErrDelivery {
				t.Fatal("rejected mail claimed success", e)
			}
			select {
			case <-relay.messages:
				t.Fatal("failed delivery produced a message")
			default:
			}
		})
	}
}

func TestPGVerificationDeliveryFailureRemainsRecoverable(t *testing.T) {
	db := verificationDatabase(t)
	relay := verificationSMTPServer(t, "starttls")
	relay.reject.Store(true)
	delivery, e := verification.NewSMTP(relay.config("starttls"))
	if e != nil {
		t.Fatal(e)
	}
	service := verificationSignin(t, db, verificationCaptchaServer(t), delivery)
	name := "delivery_" + strings.ReplaceAll(authorityUUID(t), "-", "")[:20]
	email := name + "@example.invalid"
	peer := verificationPeer(t)
	_, e = service.Register(context.Background(), signin.RegisterRequest{Name: name, Email: email, Password: "synthetic password only 1234", CaptchaToken: "synthetic-accepted"}, peer)
	if e != signin.ErrDelivery {
		t.Fatal("rejected relay claimed success", e)
	}
	if _, e = service.Start(context.Background(), signin.StartRequest{Account: name}, peer); e != signin.ErrUnverified {
		t.Fatal("failed delivery created usable account", e)
	}
	relay.reject.Store(false)
	if e = service.ResendVerification(context.Background(), name, "synthetic-accepted", peer); e != nil {
		t.Fatal(e)
	}
	code := verificationDeliveredCode(t, relay, "/auth/verify")
	if e = service.VerifyEmail(context.Background(), code); e != nil {
		t.Fatal(e)
	}
	if e = service.VerifyEmail(context.Background(), code); e != signin.ErrInvalid {
		t.Fatal("mail token reused", e)
	}
}

func verificationPeer(t *testing.T) string {
	t.Helper()
	ip := make(net.IP, 16)
	if _, e := rand.Read(ip); e != nil {
		t.Fatal(e)
	}
	copy(ip[:4], []byte{0x20, 0x01, 0x0d, 0xb8})
	return ip.String()
}
