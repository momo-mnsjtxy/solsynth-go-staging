// SPDX-License-Identifier: AGPL-3.0-only
package merchants

import (
	"context"
	"encoding/json"
	"net/http"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"solsynth.local/go-migration/ports/wallet/commerce"
	"time"
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (f *Failure) Error() string { return f.Code }

var (
	ErrInvalid         = &Failure{"MERCHANT_INVALID_REQUEST", "Invalid merchant request.", 400}
	ErrNotFound        = &Failure{"MERCHANT_NOT_FOUND", "Merchant resource not found.", 404}
	ErrForbidden       = &Failure{"MERCHANT_FORBIDDEN", "Merchant permission denied.", 403}
	ErrUnauthenticated = &Failure{"UNAUTHORIZED", "Authentication is required.", 401}
	ErrConflict        = &Failure{"MERCHANT_CONFLICT", "Configuration conflicts with existing state.", 409}
	ErrUnavailable     = &Failure{"MERCHANT_UNAVAILABLE", "Merchant dependency unavailable.", 503}
	ErrUnsupported     = &Failure{"MERCHANT_FEATURE_UNSUPPORTED", "Requested provider feature is unavailable.", 501}
)

type Binding struct {
	AppID           string  `json:"app_id"`
	AppSlug         string  `json:"app_slug"`
	PublisherID     string  `json:"publisher_id"`
	PaymentWalletID *string `json:"payment_wallet_id"`
}
type Profile struct {
	PublisherID string    `json:"publisher_id"`
	Name        string    `json:"name"`
	Bindings    []Binding `json:"bindings"`
}
type WalletPatch struct {
	WalletID *string `json:"wallet_id"`
}

func (v *WalletPatch) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) != nil || len(raw) != 1 {
		return ErrInvalid
	}
	value, ok := raw["wallet_id"]
	if !ok {
		return ErrInvalid
	}
	if json.Unmarshal(value, &v.WalletID) != nil {
		return ErrInvalid
	}
	return nil
}

type Definition struct {
	commerce.Definition
	PaymentWalletID string `json:"payee_wallet_id"`
	Enabled         bool   `json:"enabled"`
}
type PaymentPolicy struct {
	AllowInternalWallet        bool     `json:"allow_internal_wallet"`
	AllowExternal              bool     `json:"allow_external"`
	AllowInternalWalletRenewal bool     `json:"allow_internal_wallet_renewal"`
	AllowedMethods             []string `json:"allowed_methods"`
}
type GiftPolicy struct {
	AllowPurchase bool `json:"allow_purchase"`
}
type DefinitionInput struct {
	Identifier                 string              `json:"identifier"`
	GroupIdentifier            string              `json:"group_identifier"`
	DisplayName                string              `json:"display_name"`
	Currency                   string              `json:"currency"`
	BasePrice                  json.Number         `json:"base_price"`
	CycleDurationDays          int                 `json:"cycle_duration_days"`
	FreeTrialDays              int                 `json:"free_trial_days"`
	PayeeWalletID              string              `json:"payee_wallet_id"`
	AppIdentifier              *string             `json:"app_identifier"`
	Enabled                    *bool               `json:"enabled"`
	AllowInternalWalletRenewal *bool               `json:"allow_internal_wallet_renewal"`
	AllowGift                  *bool               `json:"allow_gift"`
	AllowedPaymentMethods      []string            `json:"allowed_payment_methods"`
	ProviderMappings           map[string][]string `json:"provider_mappings"`
	PaymentPolicy              *PaymentPolicy      `json:"payment_policy"`
	GiftPolicy                 *GiftPolicy         `json:"gift_policy"`
}
type Query struct {
	Take, Offset                       int
	From, To                           *time.Time
	Status                             *int
	Currency, AppID, ProductIdentifier string
}
type Order struct {
	ID                string                     `json:"id"`
	Status            commerce.OrderStatus       `json:"status"`
	Currency          string                     `json:"currency"`
	Amount            wallet.Amount              `json:"amount"`
	AppIdentifier     string                     `json:"app_identifier"`
	ProductIdentifier *string                    `json:"product_identifier"`
	Remarks           *string                    `json:"remarks"`
	Meta              map[string]json.RawMessage `json:"meta"`
	PayeeWalletID     string                     `json:"payee_wallet_id"`
	TransactionID     *string                    `json:"transaction_id"`
	Items             []commerce.OrderItem       `json:"items"`
	CreatedAt         time.Time                  `json:"created_at"`
	UpdatedAt         time.Time                  `json:"updated_at"`
	ExpiredAt         time.Time                  `json:"expired_at"`
}
type Total struct {
	Count  int64       `json:"count"`
	Amount json.Number `json:"total"`
}
type Daily struct {
	Date     string               `json:"date"`
	Currency string               `json:"currency"`
	Status   commerce.OrderStatus `json:"status"`
	Total
}
type CurrencyTotals struct {
	Total
	ByStatus map[string]Total `json:"by_status"`
}
type Report struct {
	Basis      string                    `json:"basis"`
	From       time.Time                 `json:"from"`
	To         time.Time                 `json:"to"`
	Count      int64                     `json:"total_count"`
	ByCurrency map[string]CurrencyTotals `json:"by_currency"`
	Daily      []Daily                   `json:"daily"`
}
type AdminQuery struct {
	Take, Offset                              int
	WalletID, AccountID, Currency, Identifier string
	AppIdentifier, ProductIdentifier          string
	Status, Type                              *int
}
type AdminPage[T any] struct {
	Items []T   `json:"items"`
	Total int64 `json:"total"`
}
type InboundOrder struct {
	ID             string    `json:"id"`
	Provider       string    `json:"provider"`
	ExternalID     string    `json:"external_id"`
	SubscriptionID string    `json:"subscription_id"`
	CreatedAt      time.Time `json:"created_at"`
}
type BalanceAdjustment struct {
	WalletID         string        `json:"wallet_id"`
	AccountID        string        `json:"account_id"`
	TreasuryWalletID string        `json:"treasury_wallet_id"`
	Currency         string        `json:"currency"`
	Amount           wallet.Amount `json:"amount"`
	Remark           *string       `json:"remark"`
}
type MaintenanceFailure struct {
	SubscriptionID string     `json:"subscription_id"`
	Reason         string     `json:"reason"`
	DocumentSHA256 string     `json:"document_sha256"`
	FirstSeenAt    time.Time  `json:"first_seen_at"`
	LastSeenAt     time.Time  `json:"last_seen_at"`
	ResolvedAt     *time.Time `json:"resolved_at"`
	Observations   int64      `json:"observations"`
}
type Settlement struct {
	ID          string        `json:"id"`
	PublisherID string        `json:"publisher_id"`
	AppID       string        `json:"app_id"`
	OrderID     string        `json:"order_id"`
	WalletID    string        `json:"wallet_id"`
	Currency    string        `json:"currency"`
	Amount      wallet.Amount `json:"amount"`
	Status      int           `json:"status"`
	CreatedAt   time.Time     `json:"created_at"`
	SettledAt   *time.Time    `json:"settled_at"`
}
type SettlementTotals map[string]Total
type SettlementOverview struct {
	TotalPending int64            `json:"total_pending"`
	TotalSettled int64            `json:"total_settled"`
	TotalAllTime int64            `json:"total_all_time"`
	Pending      SettlementTotals `json:"pending"`
	Settled      SettlementTotals `json:"settled"`
	ThisMonth    SettlementTotals `json:"this_month"`
}
type WalletProductApply struct {
	TreasuryWalletID string `json:"treasury_wallet_id"`
}

type AdminRepository interface {
	AdminTransactions(context.Context, string, AdminQuery) (AdminPage[wallet.Transaction], error)
	AdminTransaction(context.Context, string, string) (wallet.Transaction, error)
	AdminOrders(context.Context, string, AdminQuery) (AdminPage[Order], error)
	AdminOrder(context.Context, string, string) (Order, error)
	RefundOrder(context.Context, string, string, string) (wallet.Transaction, error)
	AdminInboundOrders(context.Context, string, AdminQuery) (AdminPage[InboundOrder], error)
	AdminInboundOrder(context.Context, string, string) (InboundOrder, error)
	AdjustBalance(context.Context, string, BalanceAdjustment, string) (wallet.Transaction, error)
	AdminSubscriptions(context.Context, string, AdminQuery) (AdminPage[commerce.Subscription], error)
	Billing(context.Context, string, AdminQuery) (AdminPage[InboundOrder], error)
	MaintenanceFailures(context.Context, string, string, int) ([]MaintenanceFailure, error)
	RetryMaintenance(context.Context, string, string) error
	RunMaintenance(context.Context, string, int) (int, error)
	WalletProductSummary(context.Context, string) (json.RawMessage, error)
	ApplyWalletProduct(context.Context, string, string, WalletProductApply, string) (wallet.Transaction, error)
}

type SettlementRepository interface {
	Settlements(context.Context, string, string, Query) ([]Settlement, int64, error)
	PendingSettlements(context.Context, string, string) (SettlementTotals, error)
	Settle(context.Context, string, string) ([]wallet.Transaction, error)
	SettlementOverview(context.Context, string, string) (SettlementOverview, error)
	SettlementReport(context.Context, string, string, Query) (Report, error)
}
type Repository interface {
	Profile(context.Context, string, string) (Profile, error)
	Binding(context.Context, string, string, string) (Binding, error)
	BindWallet(context.Context, string, string, string, WalletPatch) (Binding, error)
	Catalog(context.Context, string, Query) ([]Definition, int64, error)
	Definition(context.Context, string, string) (Definition, error)
	UpsertDefinition(context.Context, string, DefinitionInput) (Definition, bool, error)
	DeleteDefinition(context.Context, string, string) error
	Orders(context.Context, string, string, Query) ([]Order, int64, error)
	Report(context.Context, string, string, Query) (Report, error)
}
type Auth interface {
	Authenticate(*http.Request) (string, error)
	Authorize(*http.Request, string) error
}
