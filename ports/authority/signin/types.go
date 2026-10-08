// SPDX-License-Identifier: AGPL-3.0-only
// Independently authored first-party identity enrollment and factor workflows.
package signin

import (
	"context"
	"solsynth.local/go-migration/ports/authority"
	"time"
)

var (
	ErrInput       = &authority.Failure{Code: "BAD_REQUEST", Message: "Invalid sign-in request.", Status: 400}
	ErrInvalid     = &authority.Failure{Code: "INVALID_CREDENTIALS", Message: "Invalid or expired credentials.", Status: 401}
	ErrRate        = &authority.Failure{Code: "RATE_LIMITED", Message: "Too many attempts. Try again later.", Status: 429}
	ErrConflict    = &authority.Failure{Code: "ACCOUNT_OR_FACTOR_EXISTS", Message: "The account or factor already exists.", Status: 409}
	ErrDelivery    = &authority.Failure{Code: "VERIFICATION_UNAVAILABLE", Message: "Verification delivery is not configured or is unavailable.", Status: 503}
	ErrCaptcha     = &authority.Failure{Code: "CAPTCHA_UNAVAILABLE", Message: "CAPTCHA verification is not configured or is unavailable.", Status: 503}
	ErrUnverified  = &authority.Failure{Code: "EMAIL_UNVERIFIED", Message: "Verify your email before signing in.", Status: 403}
	ErrRecent      = &authority.Failure{Code: "RECENT_LOGIN_REQUIRED", Message: "Sign in again before changing authentication factors.", Status: 403}
	ErrUnsupported = &authority.Failure{Code: "FACTOR_UNSUPPORTED", Message: "This authentication factor is unavailable.", Status: 501}
)

type Credentials struct {
	AccountID, Email string
	VerifiedAt       *time.Time
}
type Factor struct {
	ID              string            `json:"id"`
	Type            int               `json:"type"`
	Trustworthy     int               `json:"trustworthy"`
	CreatedAt       time.Time         `json:"created_at"`
	EnabledAt       *time.Time        `json:"enabled_at"`
	CreatedResponse map[string]string `json:"createdResponse,omitempty"`
	Secret          []byte            `json:"-"`
	LastCounter     int64             `json:"-"`
	PendingUntil    *time.Time        `json:"-"`
}
type Challenge struct {
	ID               string    `json:"id"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	ExpiredAt        time.Time `json:"expired_at"`
	StepRemain       int       `json:"step_remain"`
	StepTotal        int       `json:"step_total"`
	FailedAttempts   int       `json:"failed_attempts"`
	BlacklistFactors []string  `json:"blacklist_factors"`
	Audiences        []string  `json:"audiences"`
	Scopes           []string  `json:"scopes"`
	Type             int       `json:"type"`
	DeviceID         string    `json:"device_id"`
	DeviceName       string    `json:"device_name"`
	Platform         int       `json:"platform"`
	IPAddress        string    `json:"ip_address"`
	UserAgent        string    `json:"user_agent"`
	Hash             []byte    `json:"-"`
	AccountID        string    `json:"account_id"`
	Version          int64     `json:"-"`
	Purpose          string    `json:"-"`
	PasswordOK       bool      `json:"-"`
	SecondOK         bool      `json:"-"`
	RequireSecond    bool      `json:"-"`
	Completed        bool      `json:"-"`
}
type RegisterRequest struct {
	Name             string `json:"name"`
	Nick             string `json:"nick"`
	Email            string `json:"email"`
	Password         string `json:"password"`
	Language         string `json:"language"`
	CaptchaToken     string `json:"captcha_token"`
	AffiliationSpell string `json:"affiliation_spell"`
}
type StartRequest struct {
	Account    string `json:"account"`
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`
	Platform   int    `json:"platform"`
	UserAgent  string `json:"-"`
}
type Delivery interface {
	Send(context.Context, string, string, string) error
}
type Captcha interface {
	// Return ErrInvalid for a rejected proof; other errors mean unavailable.
	Verify(context.Context, string, string) error
}

// Delivery is injected by the deployer. Send receives destination, purpose,
// one-use token; implementations must not log tokens or report fake success.
type Config struct {
	SealKey  []byte
	Issuer   string
	Delivery Delivery
	Captcha  Captcha
	Now      func() time.Time
	// InitialMemberPermissions is trusted operator policy, never request data.
	// It defaults to no grants and accepts only concrete permission keys.
	InitialMemberPermissions []string
}
type Store interface {
	Register(context.Context, authority.Account, Credentials, Factor, Challenge, []string) error
	Lookup(context.Context, string) (string, error)
	FindChallenge(context.Context, []byte) (string, error)
	Within(context.Context, string, func(Transaction) error) error
	Attempt(context.Context, []byte, int, time.Duration, time.Time) error
	Bind(context.Context, authority.Principal) context.Context
}
type Transaction interface {
	Authority() authority.Transaction
	Authenticate() (authority.Principal, error)
	Credentials() (Credentials, error)
	SaveCredentials(Credentials) error
	Factors() ([]Factor, error)
	SaveFactor(Factor) error
	DeleteFactor(string) error
	Challenge([]byte) (Challenge, error)
	SaveChallenge(Challenge) error
	CancelChallenges(string) error
}
type Service struct {
	store     Store
	authority *authority.Service
	verifier  authority.Authenticator
	cfg       Config
	dummy     []byte
}
