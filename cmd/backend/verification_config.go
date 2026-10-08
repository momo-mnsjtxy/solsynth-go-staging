// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"errors"
	"flag"
	"os"
	"solsynth.local/go-migration/internal/verification"
	"solsynth.local/go-migration/ports/authority/signin"
	"time"
)

type verificationOptions struct {
	captcha, secretEnv, hostname, action, binding, delivery                                *string
	address, serverName, tlsMode, auth, user, passwordEnv, from, name, verifyURL, resetURL *string
}

func verificationFlags(f *flag.FlagSet) verificationOptions {
	return verificationOptions{
		captcha:   f.String("captcha-provider", "none", "explicit CAPTCHA provider: none or turnstile"),
		secretEnv: f.String("captcha-secret-env", "", "explicit widget secret environment variable"),
		hostname:  f.String("captcha-hostname", "", "exact expected widget hostname"),
		action:    f.String("captcha-action", "", "exact expected widget action"),
		binding:   f.String("captcha-site-binding", "", "exact expected widget cData"),
		delivery:  f.String("verification-delivery", "none", "explicit verification delivery: none or smtp"),
		address:   f.String("smtp-address", "", "explicit SMTP host:port"), serverName: f.String("smtp-server-name", "", "required TLS certificate server name"),
		tlsMode: f.String("smtp-tls-mode", "", "explicit implicit or starttls encryption"), auth: f.String("smtp-auth", "", "explicit plain or none SMTP authentication"),
		user: f.String("smtp-username", "", "explicit SMTP username"), passwordEnv: f.String("smtp-password-env", "", "explicit SMTP password environment variable"),
		from: f.String("smtp-from", "", "verification envelope sender address"), name: f.String("smtp-sender-name", "SoSYS", "verification sender display name"),
		verifyURL: f.String("verification-url", "", "existing HTTPS verification completion page"), resetURL: f.String("password-reset-url", "", "existing HTTPS reset completion page"),
	}
}
func (v verificationOptions) apply(c *signin.Config) error {
	if *v.captcha != "none" && *v.captcha != "turnstile" || *v.delivery != "none" && *v.delivery != "smtp" {
		return errors.New("unsupported explicit verification provider")
	}
	if c == nil && (*v.captcha != "none" || *v.delivery != "none") {
		return errors.New("verification providers require explicit enable-signin")
	}
	if *v.captcha == "turnstile" {
		if *v.secretEnv == "" {
			return errors.New("explicit CAPTCHA secret variable required")
		}
		adapter, e := verification.NewTurnstile(verification.TurnstileConfig{Secret: os.Getenv(*v.secretEnv), Hostname: *v.hostname, Action: *v.action, SiteBinding: *v.binding, Timeout: 5 * time.Second}, nil)
		if e != nil {
			return errors.New("invalid explicit CAPTCHA configuration")
		}
		c.Captcha = adapter
	} else if *v.secretEnv != "" || *v.hostname != "" || *v.action != "" || *v.binding != "" {
		return errors.New("unused CAPTCHA configuration requires explicit provider")
	}
	if *v.delivery == "smtp" {
		password := ""
		if *v.passwordEnv != "" {
			password = os.Getenv(*v.passwordEnv)
		}
		adapter, e := verification.NewSMTP(verification.SMTPConfig{Address: *v.address, ServerName: *v.serverName, TLSMode: *v.tlsMode, Auth: *v.auth, Username: *v.user, Password: password, From: *v.from, Name: *v.name, VerifyURL: *v.verifyURL, ResetURL: *v.resetURL, Timeout: 10 * time.Second})
		if e != nil {
			return errors.New("invalid explicit verification delivery configuration")
		}
		c.Delivery = adapter
	} else if *v.address != "" || *v.serverName != "" || *v.tlsMode != "" || *v.auth != "" || *v.user != "" || *v.passwordEnv != "" || *v.from != "" || *v.verifyURL != "" || *v.resetURL != "" {
		return errors.New("unused mail configuration requires explicit delivery provider")
	}
	return nil
}
