// SPDX-License-Identifier: AGPL-3.0-only
// Independently authored authority; see docs/owned-authority-architecture.md.
package authority

import (
	"context"
	"errors"
	"time"
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (e *Failure) Error() string { return e.Code }

var (
	ErrInvalid         = &Failure{"UNAUTHORIZED", "Invalid authentication credentials.", 401}
	ErrExpired         = &Failure{"TOKEN_EXPIRED", "The access token has expired.", 401}
	ErrRevoked         = &Failure{"SESSION_REVOKED", "The session has been revoked.", 401}
	ErrStale           = &Failure{"TOKEN_STALE", "The token has been superseded.", 401}
	ErrDisabled        = &Failure{"ACCOUNT_DISABLED", "The account is disabled.", 423}
	ErrForbidden       = &Failure{"FORBIDDEN", "Permission denied.", 403}
	ErrUnavailable     = &Failure{"AUTHORITY_UNAVAILABLE", "Authentication authority is unavailable.", 503}
	ErrRefresh         = &Failure{"INVALID_REFRESH_TOKEN", "Invalid or expired refresh token.", 401}
	ErrNotFound        = errors.New("authority: record not found")
	ErrSessionNotFound = &Failure{"SESSION_NOT_FOUND", "Session not found.", 404}
)

type Account struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Nick      string    `json:"nick"`
	Language  string    `json:"language"`
	Region    string    `json:"region"`
	Superuser bool      `json:"is_superuser"`
	CreatedAt time.Time `json:"created_at"`
	Version   int64     `json:"-"`
	Disabled  bool      `json:"-"`
}
type Session struct {
	ID             string     `json:"id"`
	AccountID      string     `json:"-"`
	ParentID       string     `json:"-"`
	Label          string     `json:"label"`
	Type           int        `json:"type"`
	ClientID       string     `json:"-"`
	CreatedAt      time.Time  `json:"created_at"`
	LastGrantedAt  time.Time  `json:"last_granted_at"`
	IsCurrent      bool       `json:"is_current"`
	Category       string     `json:"category"`
	Trusted        bool       `json:"trusted"`
	Epoch          int64      `json:"-"`
	AccountVersion int64      `json:"-"`
	ExpiresAt      time.Time  `json:"-"`
	RevokedAt      *time.Time `json:"-"`
	Scopes         []string   `json:"-"`
	RefreshHash    []byte     `json:"-"`
	PreviousHash   []byte     `json:"-"`
	PreviousUntil  time.Time  `json:"-"`
	SealedPair     []byte     `json:"-"`
}
type Rule struct {
	Pattern   string
	Deny      bool
	StartsAt  *time.Time
	ExpiresAt *time.Time
}
type Punishment struct {
	Kind      string
	Pattern   string
	StartsAt  *time.Time
	ExpiresAt *time.Time
}
type LoginCode struct {
	AccountVersion int64
	Hash           []byte
	AccountID      string
	ExpiresAt      time.Time
	Used           bool
	Label          string
	Scopes         []string
}

// All writes for an account, including its grants/groups/punishments, must take
// the same account row lock. An implementation commits iff the callback succeeds.
type Store interface {
	WithinAccount(context.Context, string, func(Transaction) error) error
	FindRefresh(context.Context, []byte) (accountID, sessionID string, err error)
	FindCode(context.Context, []byte) (accountID string, err error)
}

// ReadStore provides one consistent authority snapshot without write privileges.
type ReadStore interface {
	ReadAccount(context.Context, string, func(Transaction) error) error
}
type Transaction interface {
	Account() Account
	SaveAccount(Account) error
	Session(string) (Session, error)
	Sessions() ([]Session, error)
	SaveSession(Session) error
	Rules() ([]Rule, error)
	Punishments() ([]Punishment, error)
	Code([]byte) (LoginCode, error)
	SaveCode(LoginCode) error
}
type Principal struct {
	proof   *verifiedCredential
	Account Account
	Session Session
}

// Both methods verify signatures AND fresh database state. Constructing a
// Principal never constitutes proof to this interface.
type Authenticator interface {
	Authenticate(context.Context, string) (Principal, error)
}
type Authorizer interface {
	Authorize(context.Context, string, ...string) (Principal, error)
}
type Pair struct {
	Token            string `json:"token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshExpiresIn int64  `json:"refresh_expires_in"`
}
type SessionOptions struct {
	Label    string
	ClientID string
	Type     int
	Scopes   []string
}
