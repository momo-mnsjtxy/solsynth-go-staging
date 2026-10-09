// SPDX-License-Identifier: AGPL-3.0-only
// Source-informed independent transactional wallet implementation; see PROVENANCE.md.
package accounts

import (
	"context"
	"errors"
	"regexp"
	"time"
)

var (
	ErrUnauthorized = errors.New("authentication required")
	ErrForbidden    = errors.New("wallet access denied")
	ErrUnavailable  = errors.New("required wallet adapter unavailable")
	ErrNotFound     = errors.New("wallet or transaction not found")
	ErrInvalid      = errors.New("invalid wallet request")
	ErrAmount       = errors.New("invalid or out-of-range decimal amount")
	ErrInsufficient = errors.New("insufficient available funds")
	ErrQuota        = errors.New("account wallet quota exceeded")
	ErrConflict     = errors.New("wallet request conflicts with existing state")
	ErrIdempotency  = errors.New("idempotency key was reused with a different request")
	ErrPIN          = errors.New("payment verification required")
)

const (
	PermissionTransactions = "wallets.transactions.manage"
	PermissionPublicID     = "wallets.public.id.manage"
)

type Principal struct {
	AccountID string
	SessionID string
}
type RealmRole string

const (
	Viewer RealmRole = "viewer"
	Editor RealmRole = "editor"
)

// Access is a mandatory trusted, fresh authority adapter, not request-supplied
// claims. RealmIDs returns only realms with viewer access. It must not silently
// convert an unavailable authority into an empty result.
type Access interface {
	Permission(context.Context, Principal, string) error
	Realm(context.Context, Principal, string, RealmRole) error
	RealmIDs(context.Context, Principal) ([]string, error)
}

// PaymentVerifier must consult current factor policy and validate PIN/reauth.
// A missing PIN is valid ONLY if this trusted adapter establishes it is not
// required. The legacy NO_PIN_PROVEDED sentinel is not an authorization token.
type PaymentVerifier interface {
	VerifyPayment(context.Context, Principal, string) error
}

// Canonical composition carries an opaque bounded proof into the money TX.
type PaymentAuthorizer interface {
	PreparePayment(context.Context, Principal, string, string, string) (context.Context, error)
}
type Owner struct {
	AccountID string
	RealmID   string
}

func (o Owner) Valid() bool {
	return (ValidID(o.AccountID) && o.RealmID == "") || (o.AccountID == "" && ValidID(o.RealmID))
}
func (o Owner) Key() string {
	if o.AccountID != "" {
		return "account:" + o.AccountID
	}
	return "realm:" + o.RealmID
}

var uuidSyntax = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func ValidID(v string) bool {
	return uuidSyntax.MatchString(v) && v != "00000000-0000-0000-0000-000000000000"
}

type Wallet struct {
	ID        string     `json:"id"`
	AccountID *string    `json:"account_id"`
	RealmID   *string    `json:"realm_id"`
	Name      string     `json:"name"`
	IsPrimary bool       `json:"is_primary"`
	PublicID  *string    `json:"public_id"`
	Pockets   []Pocket   `json:"pockets"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at"`
}

func (w Wallet) Owner() Owner {
	o := Owner{}
	if w.AccountID != nil {
		o.AccountID = *w.AccountID
	}
	if w.RealmID != nil {
		o.RealmID = *w.RealmID
	}
	return o
}

type Pocket struct {
	ID              string     `json:"id"`
	WalletID        string     `json:"wallet_id"`
	Currency        string     `json:"currency"`
	Amount          Amount     `json:"amount"`
	HeldAmount      Amount     `json:"held_amount"`
	AvailableAmount Amount     `json:"available_amount"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	DeletedAt       *time.Time `json:"deleted_at"`
}

// Status numbers are evidenced by the licensed authored Solian UI switch.
type Status int

const (
	Pending Status = iota
	Frozen
	Confirmed
	Refunded
	Cancelled
)

type Transaction struct {
	ID                     string     `json:"id"`
	Type                   int        `json:"type"` // 0 = Transfer, evidenced by authored Solian UI
	PayerWalletID          string     `json:"payer_wallet_id"`
	PayeeWalletID          string     `json:"payee_wallet_id"`
	Currency               string     `json:"currency"`
	Amount                 Amount     `json:"amount"`
	Remarks                *string    `json:"remarks"`
	Status                 Status     `json:"status"`
	IsFrozen               bool       `json:"is_frozen"`
	RequireConfirmation    bool       `json:"require_confirmation"`
	FrozenAt               *time.Time `json:"frozen_at"`
	ExpiresAt              *time.Time `json:"expires_at"`
	ConfirmedAt            *time.Time `json:"confirmed_at"`
	ConfirmationReceivedAt *time.Time `json:"confirmation_received_at"` // independent freeze policy
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
	DeletedAt              *time.Time `json:"deleted_at"`
}
type CreateRequest struct {
	Name    string  `json:"name"`
	RealmID *string `json:"realm_id"`
}
type TransferRequest struct {
	Amount              Amount  `json:"amount"`
	Currency            string  `json:"currency"`
	Remark              *string `json:"remark"`
	PayerWalletID       *string `json:"payer_wallet_id"`
	PayeeWalletID       *string `json:"payee_wallet_id"`
	PayeeAccountID      *string `json:"payee_account_id"`
	PayeePublicID       *string `json:"payee_public_id"`
	PINCode             *string `json:"pin_code"`
	Freeze              bool    `json:"freeze"`
	RequireConfirmation bool    `json:"require_confirmation"`
	TransferRequestID   *string `json:"transfer_request_id"`
}
type TransferCommand struct {
	TransferRequestID                                                            string
	ActorID, IdempotencyKey, Fingerprint, PayerWalletID, PayeeWalletID, Currency string
	PayerOwner                                                                   Owner
	Amount                                                                       Amount
	Remark                                                                       *string
	Freeze, RequireConfirmation                                                  bool
}
type TransactionQuery struct {
	WalletID, Direction string
	Type                *int
	Offset, Take        int
	PendingOnly         bool
}
type TransactionPage struct {
	Items []Transaction
	Total int64
}
type Transition string

const (
	Confirm Transition = "confirm"
	Reject  Transition = "reject"
)

// Store owns atomicity and account/pocket lock protocols. Ownership is immutable.
// Mutators recheck expected ownership and active-row state inside transactions.
type Store interface {
	GetWallet(context.Context, string) (Wallet, error)
	DefaultWallet(context.Context, string) (Wallet, error)
	WalletByPublicID(context.Context, string) (Wallet, error)
	ListWallets(context.Context, string, []string) ([]Wallet, error)
	CreateWallet(context.Context, Owner, string) (Wallet, error)
	SetDefault(context.Context, string, string) (Wallet, error)
	SetPublicID(context.Context, string, Owner, bool) (Wallet, error)
	Transfer(context.Context, TransferCommand) (Transaction, error)
	GetTransaction(context.Context, string) (Transaction, error)
	Transactions(context.Context, TransactionQuery) (TransactionPage, error)
	Transition(context.Context, string, Owner, Transition) (Transaction, error)
}

// PINStatusReader is optional only for reading status. Payment verification
// remains mandatory even when the status reports no configured PIN.
type PINStatus struct {
	HasPIN             bool `json:"has_pin"`
	ValidationRequired bool `json:"validation_required"`
}
type PINStatusReader interface {
	Status(context.Context, Principal) (PINStatus, error)
}
