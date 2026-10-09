// SPDX-License-Identifier: AGPL-3.0-only
package verification

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"solsynth.local/go-migration/ports/authority/signin"
)

// SMTPConfig requires explicit connection, encryption and authentication policy.
// TLSMode is "implicit" (TLS before SMTP) or "starttls" (mandatory upgrade).
// Auth is "plain" over verified TLS or explicit "none" for a trusted relay.
// RootCAs, if supplied, are a trusted operator/test CA pool, copied at creation.
// VerifyURL and ResetURL are distinct HTTPS pages at the same trusted origin;
// they must consume the code using the owned JSON completion APIs, not by GET.
type SMTPConfig struct {
	Address    string
	ServerName string
	TLSMode    string
	Auth       string
	Username   string
	Password   string
	From       string
	Name       string
	VerifyURL  string
	ResetURL   string
	Timeout    time.Duration
	RootCAs    *x509.CertPool
}

type SMTP struct {
	cfg                 SMTPConfig
	verifyURL, resetURL *url.URL
	connect             func(context.Context) (smtpSession, func(), error)
	now                 func() time.Time
}

type smtpSession interface {
	StartTLS(*tls.Config) error
	Auth(smtp.Auth) error
	Mail(string) error
	Rcpt(string) error
	Data() (io.WriteCloser, error)
	Quit() error
	Close() error
}

var _ signin.Delivery = (*SMTP)(nil)

func NewSMTP(cfg SMTPConfig) (*SMTP, error) {
	host, port, err := net.SplitHostPort(cfg.Address)
	number, portErr := strconv.ParseUint(port, 10, 16)
	if err != nil || portErr != nil || number == 0 || (!hostname(host) && net.ParseIP(host) == nil) || !hostname(cfg.ServerName) || (cfg.TLSMode != "implicit" && cfg.TLSMode != "starttls") || cfg.Timeout < time.Second || cfg.Timeout > time.Minute || !mailbox(cfg.From) || !displayName(cfg.Name) {
		return nil, ErrConfiguration
	}
	switch cfg.Auth {
	case "plain":
		if !credential(cfg.Username, 320) || !credential(cfg.Password, 4096) {
			return nil, ErrConfiguration
		}
	case "none":
		if cfg.Username != "" || cfg.Password != "" {
			return nil, ErrConfiguration
		}
	default:
		return nil, ErrConfiguration
	}
	verify, err := completionURL(cfg.VerifyURL)
	if err != nil {
		return nil, err
	}
	reset, err := completionURL(cfg.ResetURL)
	if err != nil || reset.Host != verify.Host || reset.String() == verify.String() {
		return nil, ErrConfiguration
	}
	if cfg.RootCAs != nil {
		cfg.RootCAs = cfg.RootCAs.Clone()
	}
	s := &SMTP{cfg: cfg, verifyURL: verify, resetURL: reset, now: time.Now}
	s.connect = s.dial
	return s, nil
}

func credential(s string, max int) bool {
	if len(s) == 0 || len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func displayName(s string) bool {
	if len(s) == 0 || len(s) > 100 || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func mailbox(s string) bool {
	if len(s) == 0 || len(s) > 254 || strings.ContainsAny(s, "<>()[]\\\" ,;:\r\n\t") {
		return false
	}
	for _, r := range s {
		if r < 33 || r > 126 {
			return false
		}
	}
	parsed, err := mail.ParseAddress(s)
	if err != nil || parsed.Name != "" || parsed.Address != s || strings.Count(s, "@") != 1 {
		return false
	}
	parts := strings.SplitN(s, "@", 2)
	return len(parts[0]) <= 64 && hostname(parts[1])
}
func completionURL(raw string) (*url.URL, error) {
	if len(raw) > 2048 || !opaque(raw, 1, 2048) {
		return nil, ErrConfiguration
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || !hostname(u.Hostname()) || u.Path == "" || u.Path == "/" || u.RawPath != "" || strings.Contains(u.Path, "\\") {
		return nil, ErrConfiguration
	}
	for _, r := range u.Path {
		if unicode.IsControl(r) {
			return nil, ErrConfiguration
		}
	}
	if u.Port() == "" && u.Host != u.Hostname() {
		return nil, ErrConfiguration
	}
	if port := u.Port(); port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return nil, ErrConfiguration
		}
	}
	return u, nil
}
func (s *SMTP) tlsConfig() *tls.Config {
	return &tls.Config{ServerName: s.cfg.ServerName, RootCAs: s.cfg.RootCAs, MinVersion: tls.VersionTLS12}
}

func (s *SMTP) dial(ctx context.Context) (smtpSession, func(), error) {
	dialer := &net.Dialer{Timeout: s.cfg.Timeout}
	var conn net.Conn
	var err error
	if s.cfg.TLSMode == "implicit" {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: s.tlsConfig()}).DialContext(ctx, "tcp", s.cfg.Address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", s.cfg.Address)
	}
	if err != nil {
		return nil, nil, signin.ErrDelivery
	}
	// net/smtp has no context API. Deadline and cancellation cover the greeting,
	// TLS upgrade, authentication, envelope, DATA acceptance and cleanup.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	cleanup := func() { stop(); _ = conn.Close() }
	deadline, ok := ctx.Deadline()
	if !ok || conn.SetDeadline(deadline) != nil {
		cleanup()
		return nil, nil, signin.ErrDelivery
	}
	client, err := smtp.NewClient(conn, s.cfg.ServerName)
	if err != nil {
		cleanup()
		return nil, nil, signin.ErrDelivery
	}
	return client, cleanup, nil
}

// Send returns nil only after the relay accepts DATA. SMTP has no idempotency
// primitive; uncertain writes are returned as failures and are never retried.
// All errors are fixed sentinels; recipient, password and code never enter logs.
func (s *SMTP) Send(ctx context.Context, recipient, purpose, token string) error {
	if s == nil || s.connect == nil {
		return signin.ErrDelivery
	}
	message, err := s.message(recipient, purpose, token)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	if ctx.Err() != nil {
		return signin.ErrDelivery
	}
	client, cleanup, err := s.connect(ctx)
	if err != nil {
		return signin.ErrDelivery
	}
	defer cleanup()
	defer client.Close()
	if s.cfg.TLSMode == "starttls" {
		if err = client.StartTLS(s.tlsConfig()); err != nil {
			return signin.ErrDelivery
		}
	}
	if s.cfg.Auth == "plain" {
		if err = client.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.ServerName)); err != nil {
			return signin.ErrDelivery
		}
	}
	if err = client.Mail(s.cfg.From); err != nil {
		return signin.ErrDelivery
	}
	if err = client.Rcpt(recipient); err != nil {
		return signin.ErrDelivery
	}
	data, err := client.Data()
	if err != nil {
		return signin.ErrDelivery
	}
	n, writeErr := data.Write(message)
	if writeErr != nil || n != len(message) {
		// Do not close DATA after a partial write: Close would finalize a truncated
		// message. Closing the underlying session aborts without a success claim.
		return signin.ErrDelivery
	}
	if err = data.Close(); err != nil {
		return signin.ErrDelivery
	}
	// A failed QUIT cannot undo a successful DATA acknowledgement.
	_ = client.Quit()
	return nil
}

func (s *SMTP) message(recipient, purpose, token string) ([]byte, error) {
	if !mailbox(recipient) || len(token) != 43 {
		return nil, signin.ErrInput
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != token {
		return nil, signin.ErrInput
	}
	var target url.URL
	var subject, text string
	switch purpose {
	case "verify_email":
		target = *s.verifyURL
		subject = "Verify your email"
		text = "Confirm your email address using the following one-use link:"
	case "reset_password":
		target = *s.resetURL
		subject = "Reset your password"
		text = "Choose a new password using the following one-use link:"
	default:
		return nil, signin.ErrInput
	}
	target.RawQuery = url.Values{"code": {token}}.Encode()
	body := s.cfg.Name + "\n\n" + text + "\n\n" + target.String() + "\n\nThis link expires in 30 minutes and can be used once. If you did not request this, you can ignore this message.\n"
	var out bytes.Buffer
	id := sha256.Sum256([]byte(purpose + "\x00" + recipient + "\x00" + token))
	domain := strings.SplitN(s.cfg.From, "@", 2)[1]
	fmt.Fprintf(&out, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%x@%s>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n",
		(&mail.Address{Name: s.cfg.Name, Address: s.cfg.From}).String(), recipient, mime.QEncoding.Encode("utf-8", subject), s.now().UTC().Format(time.RFC1123Z), id[:], domain)
	qp := quotedprintable.NewWriter(&out)
	if _, err = io.WriteString(qp, body); err != nil {
		return nil, signin.ErrDelivery
	}
	if err = qp.Close(); err != nil || out.Len() > 16<<10 {
		return nil, signin.ErrDelivery
	}
	return out.Bytes(), nil
}
