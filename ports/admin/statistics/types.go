// SPDX-License-Identifier: AGPL-3.0-only
package statistics

import (
	"context"
	"net/http"
	"time"
)

type Service string

const (
	Passport Service = "passport"
	Sphere   Service = "sphere"
	Wallet   Service = "wallet"
)

func (s Service) Permission() string {
	switch s {
	case Passport:
		return "accounts.view"
	case Sphere:
		return "posts.moderate"
	case Wallet:
		return "wallets.transactions.manage"
	}
	return ""
}

type AccountStats struct {
	CalculatedAt             time.Time `json:"calculated_at"`
	TotalProfiledAccounts    int64     `json:"total_profiled_accounts"`
	ActiveUsersLastDay       int64     `json:"active_users_last_day"`
	ActiveUsersLastWeek      int64     `json:"active_users_last_week"`
	ActiveUsersLastMonth     int64     `json:"active_users_last_month"`
	RegisteredUsersLastDay   int64     `json:"registered_users_last_day"`
	RegisteredUsersLastWeek  int64     `json:"registered_users_last_week"`
	RegisteredUsersLastMonth int64     `json:"registered_users_last_month"`
}
type PostStats struct {
	CalculatedAt    time.Time `json:"calculated_at"`
	TotalPosts      int64     `json:"total_posts"`
	PublishedPosts  int64     `json:"published_posts"`
	DraftPosts      int64     `json:"draft_posts"`
	PostsLastDay    int64     `json:"posts_last_day"`
	PostsLastWeek   int64     `json:"posts_last_week"`
	PostsLastMonth  int64     `json:"posts_last_month"`
	TotalPublishers int64     `json:"total_publishers"`
	TotalReactions  int64     `json:"total_reactions"`
	TotalBookmarks  int64     `json:"total_bookmarks"`
}
type WalletStats struct {
	CalculatedAt          time.Time `json:"calculated_at"`
	TotalWallets          int64     `json:"total_wallets"`
	TotalTransactions     int64     `json:"total_transactions"`
	ConfirmedTransactions int64     `json:"confirmed_transactions"`
	PendingTransactions   int64     `json:"pending_transactions"`
	TransactionsLastDay   int64     `json:"transactions_last_day"`
	TransactionsLastWeek  int64     `json:"transactions_last_week"`
	TransactionsLastMonth int64     `json:"transactions_last_month"`
	TotalOrders           int64     `json:"total_orders"`
	PaidOrders            int64     `json:"paid_orders"`
	TotalSubscriptions    int64     `json:"total_subscriptions"`
}
type Repository interface {
	Statistics(context.Context, string, Service) (any, error)
}
type Auth interface {
	Authenticate(*http.Request) (string, error)
}
