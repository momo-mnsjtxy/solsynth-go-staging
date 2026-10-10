// SPDX-License-Identifier: AGPL-3.0-only
// Independent commerce state and adapters; source boundaries in PROVENANCE.md.
package commerce

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"time"
)

var (
	ErrInvalid     = errors.New("invalid commerce request")
	ErrNotFound    = errors.New("commerce resource not found")
	ErrConflict    = errors.New("commerce state conflict")
	ErrUnavailable = errors.New("commerce adapter unavailable")
	ErrForbidden   = errors.New("commerce access denied")
	ErrIdempotency = errors.New("idempotency key reused with different input")
	ErrStock       = errors.New("insufficient product stock")
)

type OrderStatus int

const (
	Unpaid OrderStatus = iota
	Paid
	Cancelled
	Finished
	Expired
)

// Active=1 and gift statuses are evidenced by authored Solian callers. Other
// subscription states are an explicit independent policy, not a Shared SDK claim.
type SubscriptionStatus int

const (
	SubscriptionUnpaid SubscriptionStatus = iota
	SubscriptionActive
	SubscriptionCancelled
	SubscriptionExpired
)

type GiftStatus int

const (
	GiftCreated GiftStatus = iota
	GiftSent
	GiftRedeemed
	GiftCancelled
	GiftExpired
)

type OrderItem struct {
	ProductIdentifier string      `json:"product_identifier"`
	Quantity          int         `json:"quantity"`
	UnitPrice         json.Number `json:"unit_price"`
	Currency          string      `json:"currency"`
}
type Order struct {
	Coupon            *CouponQuote               `json:"coupon,omitempty"`
	ID                string                     `json:"id"`
	Status            OrderStatus                `json:"status"`
	Currency          string                     `json:"currency"`
	Amount            wallet.Amount              `json:"amount"`
	AppIdentifier     string                     `json:"app_identifier"`
	ProductIdentifier *string                    `json:"product_identifier"`
	Remarks           *string                    `json:"remarks"`
	Meta              map[string]json.RawMessage `json:"meta"`
	PayeeWalletID     string                     `json:"payee_wallet_id"`
	PayerWalletID     *string                    `json:"payer_wallet_id"`
	TransactionID     *string                    `json:"transaction_id"`
	Items             []OrderItem                `json:"items"`
	CreatedAt         time.Time                  `json:"created_at"`
	UpdatedAt         time.Time                  `json:"updated_at"`
	ExpiredAt         time.Time                  `json:"expired_at"`
	App               *OrderApp                  `json:"app"`
	Developer         *OrderDeveloper            `json:"developer"`
	IsSystemApp       bool                       `json:"is_system_app"`
}
type Credentials struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}
type ItemRequest struct {
	ProductIdentifier string `json:"product_identifier"`
	Quantity          int    `json:"quantity"`
}
type CreateOrder struct {
	Credentials
	Currency          string                     `json:"currency"`
	Amount            wallet.Amount              `json:"amount"`
	Remarks           *string                    `json:"remarks"`
	ProductIdentifier *string                    `json:"product_identifier"`
	Meta              map[string]json.RawMessage `json:"meta"`
	DurationHours     int                        `json:"duration_hours"`
	PayeeWalletID     *string                    `json:"payee_wallet_id"`
	Items             []ItemRequest              `json:"items"`
}
type PayOrder struct {
	PayerWalletID *string `json:"payer_wallet_id"`
	PINCode       *string `json:"pin_code"`
}
type UpdateStatus struct {
	Credentials
	Status OrderStatus `json:"status"`
}
type PageQuery struct {
	Offset, Take                     int
	Status                           *int
	AppIdentifier, ProductIdentifier string
}
type PaymentDetails struct {
	Currency      string  `json:"currency"`
	OrderID       string  `json:"order_id,omitempty"`
	PayerWalletID *string `json:"payer_wallet_id,omitempty"`
}
type Definition struct {
	ProductID                  string              `json:"product_id,omitempty"`
	ProductIdentifier          string              `json:"product_identifier,omitempty"`
	UnitPrice                  json.Number         `json:"unit_price,omitempty"`
	ProductTerms               string              `json:"-"`
	RequiredScopes             []string            `json:"required_scopes,omitempty"`
	AppIdentifier              *string             `json:"app_identifier,omitempty"`
	Identifier                 string              `json:"identifier"`
	GroupIdentifier            string              `json:"group_identifier"`
	DisplayName                string              `json:"display_name"`
	Currency                   string              `json:"currency"`
	BasePrice                  wallet.Amount       `json:"base_price"`
	PayeeWalletID              string              `json:"-"`
	CycleDurationDays          int                 `json:"cycle_duration_days"`
	FreeTrialDays              int                 `json:"free_trial_days,omitempty"`
	AllowInternalWalletRenewal bool                `json:"allow_internal_wallet_renewal"`
	AllowGift                  bool                `json:"allow_gift"`
	AllowedPaymentMethods      []string            `json:"allowed_payment_methods"`
	ProviderMappings           map[string][]string `json:"provider_mappings"`
}
type CreateSubscription struct {
	Identifier        string         `json:"identifier"`
	PaymentMethod     string         `json:"payment_method"`
	PaymentDetails    PaymentDetails `json:"payment_details"`
	Coupon            *string        `json:"coupon"`
	CycleDurationDays *int           `json:"cycle_duration_days"`
	IsFreeTrial       bool           `json:"is_free_trial"`
	IsAutoRenewal     *bool          `json:"is_auto_renewal"`
}
type Subscription struct {
	Coupon            *CouponQuote       `json:"coupon,omitempty"`
	AppIdentifier     *string            `json:"app_identifier,omitempty"`
	ProductTerms      string             `json:"product_terms,omitempty"`
	ConsentVersion    int64              `json:"consent_version,omitempty"`
	CycleDurationDays int                `json:"cycle_duration_days"`
	IsAutoRenewal     bool               `json:"is_auto_renewal"`
	ID                string             `json:"id"`
	AccountID         string             `json:"account_id"`
	Identifier        string             `json:"identifier"`
	GroupIdentifier   string             `json:"group_identifier"`
	DisplayName       string             `json:"display_name"`
	Status            SubscriptionStatus `json:"status"`
	IsActive          bool               `json:"is_active"`
	IsFreeTrial       bool               `json:"is_free_trial"`
	IsTesting         bool               `json:"is_testing"`
	PaymentMethod     string             `json:"payment_method"`
	PaymentDetails    PaymentDetails     `json:"payment_details"`
	BasePrice         wallet.Amount      `json:"base_price"`
	FinalPrice        wallet.Amount      `json:"final_price"`
	BegunAt           time.Time          `json:"begun_at"`
	EndedAt           time.Time          `json:"ended_at"`
	RenewalAt         *time.Time         `json:"renewal_at"`
	CreatedAt         time.Time          `json:"created_at"`
	UpdatedAt         time.Time          `json:"updated_at"`
}
type PurchaseGift struct {
	SubscriptionIdentifier   string         `json:"subscription_identifier"`
	RecipientID              *string        `json:"recipient_id"`
	PaymentMethod            string         `json:"payment_method"`
	PaymentDetails           PaymentDetails `json:"payment_details"`
	Message                  *string        `json:"message"`
	Coupon                   *string        `json:"coupon"`
	GiftDurationDays         *int           `json:"gift_duration_days"`
	SubscriptionDurationDays *int           `json:"subscription_duration_days"`
}
type Gift struct {
	Coupon                 *CouponQuote   `json:"coupon,omitempty"`
	ID                     string         `json:"id"`
	GifterID               string         `json:"gifter_id"`
	RecipientID            *string        `json:"recipient_id"`
	RedeemerID             *string        `json:"redeemer_id"`
	SubscriptionID         *string        `json:"subscription_id"`
	GiftCode               string         `json:"gift_code"`
	SubscriptionIdentifier string         `json:"subscription_identifier"`
	Message                *string        `json:"message"`
	Status                 GiftStatus     `json:"status"`
	IsOpenGift             bool           `json:"is_open_gift"`
	BasePrice              wallet.Amount  `json:"base_price"`
	FinalPrice             wallet.Amount  `json:"final_price"`
	PaymentMethod          string         `json:"payment_method"`
	PaymentDetails         PaymentDetails `json:"payment_details"`
	ExpiresAt              time.Time      `json:"expires_at"`
	RedeemedAt             *time.Time     `json:"redeemed_at"`
	CreatedAt              time.Time      `json:"created_at"`
	UpdatedAt              time.Time      `json:"updated_at"`
}
type Redemption struct {
	Gift         Gift         `json:"gift"`
	Subscription Subscription `json:"subscription"`
}
type GiftCheck struct {
	GiftCode               string  `json:"gift_code"`
	SubscriptionIdentifier string  `json:"subscription_identifier"`
	CanRedeem              bool    `json:"can_redeem"`
	Error                  string  `json:"error"`
	Message                *string `json:"message"`
}
type Metrics struct {
	AppIdentifier          string                   `json:"app_identifier"`
	TotalOrders            int                      `json:"total_orders"`
	PaidOrders             int                      `json:"paid_orders"`
	UnpaidOrders           int                      `json:"unpaid_orders"`
	FinishedOrders         int                      `json:"finished_orders"`
	CancelledOrders        int                      `json:"cancelled_orders"`
	ExpiredOrders          int                      `json:"expired_orders"`
	TotalIncomingAmount    wallet.Amount            `json:"total_incoming_amount"`
	PaidIncomingAmount     wallet.Amount            `json:"paid_incoming_amount"`
	ProductIncomingAmounts map[string]wallet.Amount `json:"product_incoming_amounts"`
	ProductOrderCounts     map[string]int           `json:"product_order_counts"`
}

type AppPayout struct {
	Credentials
	PayeeAccountID string        `json:"payee_account_id"`
	Currency       string        `json:"currency"`
	Amount         wallet.Amount `json:"amount"`
	Remarks        *string       `json:"remarks"`
}

type PayoutStore interface {
	CreateAppPayout(context.Context, AppPayout, string) (wallet.Transaction, error)
}

// All writes require mandatory transaction authority in the production Store.
type Store interface {
	CreateOrder(context.Context, CreateOrder, string) (Order, error)
	GetOrder(context.Context, string, string, bool) (Order, error)
	ListOrders(context.Context, string, PageQuery) ([]Order, int, error)
	PayOrder(context.Context, string, string, *string) (Order, error)
	UpdateOrder(context.Context, string, UpdateStatus) (Order, error)
	Metrics(context.Context, Credentials, *time.Time, *time.Time) (Metrics, error)
	Catalog(context.Context) ([]Definition, error)
	CreateSubscription(context.Context, string, CreateSubscription, string, bool) (Subscription, error)
	Subscriptions(context.Context, string, PageQuery) ([]Subscription, int, error)
	Subscription(context.Context, string, string) (Subscription, error)
	CancelSubscription(context.Context, string, string) (Subscription, error)
	SubscriptionOrder(context.Context, string, string) (Order, error)
	PurchaseGift(context.Context, string, PurchaseGift, string) (Gift, error)
	Gifts(context.Context, string, string, PageQuery) ([]Gift, int, error)
	Gift(context.Context, string, string) (Gift, error)
	GiftOrder(context.Context, string, string) (Order, error)
	GiftAction(context.Context, string, string, bool) (Gift, error)
	CheckGift(context.Context, string, string) (GiftCheck, error)
	RedeemGift(context.Context, string, string) (Redemption, error)
}
type Identity interface {
	Authenticate(*http.Request) (wallet.Principal, error)
}

// Providers are explicitly injected. No provider network implementation, saved
// credentials, signature bypass or permissive default is included.
type Provider interface {
	Checkout(context.Context, string, string, json.RawMessage) (any, error)
	Restore(context.Context, string, json.RawMessage) (VerifiedPurchase, error)
	VerifyWebhook(context.Context, http.Header, []byte) (VerifiedPurchase, error)
}
type VerifiedPurchase struct {
	Provider, ExternalID, AccountID, Identifier string
	BegunAt                                     time.Time
	DurationDays                                int
	Testing                                     bool
}
type ProviderStore interface {
	ApplyProvider(context.Context, VerifiedPurchase) (Subscription, error)
}
type Service struct {
	Store     Store
	Identity  Identity
	Payments  wallet.PaymentVerifier
	Providers map[string]Provider
}

type GroupCatalog struct {
	GroupIdentifier string       `json:"group_identifier"`
	DisplayName     string       `json:"display_name"`
	MaxPerkLevel    int          `json:"max_perk_level"`
	Items           []Definition `json:"items"`
}
type GroupItem struct {
	Subscription Subscription `json:"subscription"`
	Definition   *Definition  `json:"definition"`
}
type GroupState struct {
	GroupIdentifier string       `json:"group_identifier"`
	Catalog         GroupCatalog `json:"catalog"`
	Current         *GroupItem   `json:"current"`
	Next            *GroupItem   `json:"next"`
	Subscriptions   []GroupItem  `json:"subscriptions"`
}
type GroupStore interface {
	SubscriptionGroup(context.Context, string, string) (GroupState, error)
}

type PendingActivations struct {
	TotalCount       int            `json:"total_count"`
	NextActivationAt *time.Time     `json:"next_activation_at"`
	Subscriptions    []Subscription `json:"subscriptions"`
}
type ActivateSubscription struct {
	SubscriptionID string `json:"subscription_id"`
}
type ActivationStore interface {
	PendingActivations(context.Context, string, PageQuery) (PendingActivations, error)
	SwitchSubscriptionActivation(context.Context, string, string, string) (GroupState, error)
}

type OrderApp struct {
	ID          string          `json:"id"`
	Slug        string          `json:"slug"`
	Name        string          `json:"name"`
	Description *string         `json:"description"`
	Status      int             `json:"status"`
	Picture     json.RawMessage `json:"picture"`
	Background  json.RawMessage `json:"background"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}
type OrderDeveloper struct {
	ID            string  `json:"id"`
	PublisherID   string  `json:"publisher_id"`
	PublisherName *string `json:"publisher_name"`
}
