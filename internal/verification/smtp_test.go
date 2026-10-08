// SPDX-License-Identifier: AGPL-3.0-only
package verification

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"net/smtp"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"solsynth.local/go-migration/ports/authority/signin"
)

func mailConfig() SMTPConfig {
	return SMTPConfig{Address: "smtp.example.invalid:465", ServerName: "smtp.example.invalid", TLSMode: "implicit", Auth: "plain", Username: "synthetic-user", Password: "synthetic-password", From: "sender@example.invalid", Name: "Synthetic 验证", VerifyURL: "https://identity.example.invalid/auth/verify", ResetURL: "https://identity.example.invalid/auth/reset", Timeout: time.Second}
}
func mailToken() string { return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{251}, 32)) }

type fakeSMTP struct {
	events     []string
	fail       string
	body       bytes.Buffer
	dataClosed bool
	tlsConfig  *tls.Config
}

func (f *fakeSMTP) step(event string) error {
	f.events = append(f.events, event)
	if f.fail == event {
		return errors.New("synthetic secret-bearing failure")
	}
	return nil
}
func (f *fakeSMTP) StartTLS(c *tls.Config) error { f.tlsConfig = c; return f.step("tls") }
func (f *fakeSMTP) Auth(a smtp.Auth) error {
	mechanism, response, e := a.Start(&smtp.ServerInfo{Name: "smtp.example.invalid", TLS: true, Auth: []string{"PLAIN"}})
	if e != nil || mechanism != "PLAIN" || string(response) != "\x00synthetic-user\x00synthetic-password" {
		return errors.New("bad auth")
	}
	return f.step("auth")
}
func (f *fakeSMTP) Mail(from string) error {
	if from != "sender@example.invalid" {
		return errors.New("bad sender")
	}
	return f.step("mail")
}
func (f *fakeSMTP) Rcpt(to string) error {
	if to != "recipient@example.invalid" {
		return errors.New("bad recipient")
	}
	return f.step("rcpt")
}
func (f *fakeSMTP) Data() (io.WriteCloser, error) {
	if e := f.step("data"); e != nil {
		return nil, e
	}
	return fakeData{f}, nil
}
func (f *fakeSMTP) Quit() error  { return f.step("quit") }
func (f *fakeSMTP) Close() error { return f.step("close") }

type fakeData struct{ f *fakeSMTP }

func (d fakeData) Write(b []byte) (int, error) {
	if e := d.f.step("write"); e != nil {
		return 0, e
	}
	if d.f.fail == "short" {
		return len(b) - 1, nil
	}
	return d.f.body.Write(b)
}
func (d fakeData) Close() error { d.f.dataClosed = true; return d.f.step("accepted") }
func fakeMailer(t *testing.T, cfg SMTPConfig, f *fakeSMTP) *SMTP {
	t.Helper()
	s, e := NewSMTP(cfg)
	if e != nil {
		t.Fatal(e)
	}
	s.connect = func(ctx context.Context) (smtpSession, func(), error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("SMTP lacks deadline")
		}
		return f, func() { _ = f.step("cleanup") }, nil
	}
	return s
}

func TestSMTPVerifiedTLSBeforeAuthAndEnvelope(t *testing.T) {
	for _, mode := range []string{"implicit", "starttls"} {
		t.Run(mode, func(t *testing.T) {
			cfg := mailConfig()
			cfg.TLSMode = mode
			f := &fakeSMTP{}
			s := fakeMailer(t, cfg, f)
			if e := s.Send(context.Background(), "recipient@example.invalid", "verify_email", mailToken()); e != nil {
				t.Fatal(e)
			}
			want := []string{"auth", "mail", "rcpt", "data", "write", "accepted", "quit", "close", "cleanup"}
			if mode == "starttls" {
				want = append([]string{"tls"}, want...)
				if f.tlsConfig.ServerName != cfg.ServerName || f.tlsConfig.InsecureSkipVerify || f.tlsConfig.MinVersion != tls.VersionTLS12 {
					t.Fatal("unsafe TLS upgrade")
				}
			}
			if !reflect.DeepEqual(f.events, want) {
				t.Fatal(f.events)
			}
		})
	}
	cfg := mailConfig()
	cfg.Auth = "none"
	cfg.Username = ""
	cfg.Password = ""
	f := &fakeSMTP{}
	s := fakeMailer(t, cfg, f)
	if e := s.Send(context.Background(), "recipient@example.invalid", "verify_email", mailToken()); e != nil || f.events[0] != "mail" {
		t.Fatal("explicit relay auth failed", e)
	}
}

func TestSMTPFailureNeverClaimsSuccessOrRetries(t *testing.T) {
	for _, phase := range []string{"tls", "auth", "mail", "rcpt", "data", "write", "short", "accepted"} {
		t.Run(phase, func(t *testing.T) {
			cfg := mailConfig()
			cfg.TLSMode = "starttls"
			f := &fakeSMTP{fail: phase}
			s := fakeMailer(t, cfg, f)
			if e := s.Send(context.Background(), "recipient@example.invalid", "verify_email", mailToken()); e != signin.ErrDelivery {
				t.Fatal("error exposed or accepted", e)
			}
			for _, event := range f.events {
				if event == "quit" {
					t.Fatal("failed delivery reached success cleanup")
				}
			}
			if (phase == "write" || phase == "short") && f.dataClosed {
				t.Fatal("partial DATA was finalized")
			}
			if f.events[len(f.events)-1] != "cleanup" {
				t.Fatal("connection leaked")
			}
		})
	}
	f := &fakeSMTP{fail: "quit"}
	s := fakeMailer(t, mailConfig(), f)
	if e := s.Send(context.Background(), "recipient@example.invalid", "verify_email", mailToken()); e != nil || !f.dataClosed {
		t.Fatal("post-acceptance QUIT lost delivery result", e)
	}
}

func TestSMTPMessageLinksAndUTF8Encoding(t *testing.T) {
	s, e := NewSMTP(mailConfig())
	if e != nil {
		t.Fatal(e)
	}
	for _, purpose := range []string{"verify_email", "reset_password"} {
		raw, e := s.message("recipient@example.invalid", purpose, mailToken())
		if e != nil {
			t.Fatal(e)
		}
		m, e := mail.ReadMessage(bytes.NewReader(raw))
		if e != nil {
			t.Fatal(e)
		}
		from, e := mail.ParseAddress(m.Header.Get("From"))
		if e != nil || from.Name != s.cfg.Name {
			t.Fatal("display name did not round trip")
		}
		typ, params, e := mime.ParseMediaType(m.Header.Get("Content-Type"))
		if e != nil || typ != "text/plain" || params["charset"] != "UTF-8" {
			t.Fatal("not UTF-8 plain text")
		}
		if m.Header.Get("Content-Transfer-Encoding") != "quoted-printable" {
			t.Fatal("missing safe transport encoding")
		}
		body, e := io.ReadAll(quotedprintable.NewReader(m.Body))
		if e != nil {
			t.Fatal(e)
		}
		if !strings.Contains(string(body), s.cfg.Name) {
			t.Fatal("UTF-8 content lost")
		}
		want := s.cfg.VerifyURL
		if purpose == "reset_password" {
			want = s.cfg.ResetURL
		}
		found := false
		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "https://") {
				continue
			}
			link, e := url.Parse(line)
			if e != nil {
				t.Fatal(e)
			}
			code := link.Query().Get("code")
			link.RawQuery = ""
			if code != mailToken() || link.String() != want {
				t.Fatal("wrong token or destination")
			}
			found = true
		}
		if !found || len(raw) > 16<<10 || !strings.Contains(string(body), "30 minutes") {
			t.Fatal("incomplete bounded message")
		}
		if strings.Contains(string(raw), s.cfg.Password) || strings.Contains(string(raw), s.cfg.Username) {
			t.Fatal("SMTP credentials in message")
		}
	}
	a, _ := s.message("recipient@example.invalid", "verify_email", mailToken())
	b, _ := s.message("recipient@example.invalid", "verify_email", mailToken())
	ma, _ := mail.ReadMessage(bytes.NewReader(a))
	mb, _ := mail.ReadMessage(bytes.NewReader(b))
	if ma.Header.Get("Message-ID") != mb.Header.Get("Message-ID") {
		t.Fatal("same token lost stable Message-ID")
	}
}

func TestSMTPRejectsInjectionAndUnsupportedPayloadsBeforeDial(t *testing.T) {
	f := &fakeSMTP{}
	s := fakeMailer(t, mailConfig(), f)
	for _, input := range [][3]string{
		{"recipient@example.invalid\r\nBcc: other@example.invalid", "verify_email", mailToken()},
		{"Name <recipient@example.invalid>", "verify_email", mailToken()},
		{"a@example.invalid,b@example.invalid", "verify_email", mailToken()},
		{"用户@example.invalid", "verify_email", mailToken()},
		{"recipient@example.invalid", "unknown", mailToken()},
		{"recipient@example.invalid", "reset_password", "short"},
		{"recipient@example.invalid", "reset_password", strings.Repeat("a", 1<<20)},
		{"recipient@example.invalid", "verify_email", mailToken() + "\r\n"},
	} {
		if e := s.Send(context.Background(), input[0], input[1], input[2]); e != signin.ErrInput {
			t.Fatal("accepted bad payload", e)
		}
	}
	if len(f.events) != 0 {
		t.Fatal("invalid payload opened SMTP")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := s.Send(ctx, "recipient@example.invalid", "verify_email", mailToken()); e != signin.ErrDelivery || len(f.events) != 0 {
		t.Fatal("canceled send reached SMTP", e)
	}
	s.connect = func(ctx context.Context) (smtpSession, func(), error) { <-ctx.Done(); return nil, nil, ctx.Err() }
	start := time.Now()
	if e := s.Send(context.Background(), "recipient@example.invalid", "verify_email", mailToken()); e != signin.ErrDelivery || time.Since(start) > 2*time.Second {
		t.Fatal("unbounded SMTP dial")
	}
}

func TestSMTPConfigurationFailsClosed(t *testing.T) {
	for _, mutate := range []func(*SMTPConfig){
		func(c *SMTPConfig) { c.Address = "" }, func(c *SMTPConfig) { c.Address = "smtp.example.invalid:0" }, func(c *SMTPConfig) { c.Address = "smtp.example.invalid:65536" },
		func(c *SMTPConfig) { c.ServerName = "" }, func(c *SMTPConfig) { c.ServerName = "*.example.invalid" },
		func(c *SMTPConfig) { c.TLSMode = "" }, func(c *SMTPConfig) { c.TLSMode = "plain" }, func(c *SMTPConfig) { c.Auth = "" },
		func(c *SMTPConfig) { c.Password = "" }, func(c *SMTPConfig) { c.Username = "user\r\n" }, func(c *SMTPConfig) { c.Auth = "none" },
		func(c *SMTPConfig) { c.From = "sender@example.invalid\r\n" }, func(c *SMTPConfig) { c.Name = "Name\nInjected" },
		func(c *SMTPConfig) { c.Timeout = 0 }, func(c *SMTPConfig) { c.Timeout = 2 * time.Minute },
		func(c *SMTPConfig) { c.VerifyURL = "http://identity.example.invalid/verify" }, func(c *SMTPConfig) { c.VerifyURL = "https://user@identity.example.invalid/verify" },
		func(c *SMTPConfig) { c.VerifyURL = "https://identity.example.invalid/verify?next=elsewhere" }, func(c *SMTPConfig) { c.ResetURL = "https://other.example.invalid/reset" },
		func(c *SMTPConfig) { c.ResetURL = c.VerifyURL },
		func(c *SMTPConfig) { c.VerifyURL = "https://identity.example.invalid/verify%0A" },
		func(c *SMTPConfig) { c.VerifyURL = "https://identity.example.invalid:/verify" }, func(c *SMTPConfig) { c.VerifyURL = "https://identity.example.invalid/" },
	} {
		c := mailConfig()
		mutate(&c)
		if _, e := NewSMTP(c); e != ErrConfiguration {
			t.Fatal("invalid configuration accepted")
		}
	}
	var s *SMTP
	if e := s.Send(context.Background(), "recipient@example.invalid", "verify_email", mailToken()); e != signin.ErrDelivery {
		t.Fatal("nil adapter did not close")
	}
}
