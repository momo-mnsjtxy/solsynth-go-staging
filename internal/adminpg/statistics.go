// SPDX-License-Identifier: AGPL-3.0-only
package adminpg

import (
	"context"
	c "solsynth.local/go-migration/ports/admin/content"
	s "solsynth.local/go-migration/ports/admin/statistics"
	"solsynth.local/go-migration/ports/wallet/accounts"
	"solsynth.local/go-migration/ports/wallet/commerce"
	"time"
)

// Statistics uses one aggregate SQL statement per service, so all counts share
// one PostgreSQL snapshot. Current global authority is locked before that read
// and rechecked before commit. It never reads private coordinates or identities.
func (r *Repository) Statistics(ctx context.Context, actor string, service s.Service) (any, error) {
	permission := service.Permission()
	if permission == "" {
		return nil, c.ErrInvalid
	}
	tx, e := r.global.BeginGlobalTransaction(ctx, actor, permission, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	now := r.now().UTC()
	day, week, month := now.Add(-24*time.Hour), now.Add(-7*24*time.Hour), now.Add(-30*24*time.Hour)
	var out any
	switch service {
	case s.Passport:
		v := s.AccountStats{CalculatedAt: now}
		e = tx.QueryRowContext(ctx, `SELECT count(*),count(*)FILTER(WHERE l.last_seen_at>=$2 AND l.last_seen_at<=$1),count(*)FILTER(WHERE l.last_seen_at>=$3 AND l.last_seen_at<=$1),count(*)FILTER(WHERE l.last_seen_at>=$4 AND l.last_seen_at<=$1),count(*)FILTER(WHERE a.created_at>=$2 AND a.created_at<=$1),count(*)FILTER(WHERE a.created_at>=$3 AND a.created_at<=$1),count(*)FILTER(WHERE a.created_at>=$4 AND a.created_at<=$1)FROM owned_authority_accounts a JOIN owned_accounts_profiles p ON p.account_id=a.id LEFT JOIN owned_presence_last_seen l ON l.account_id=a.id WHERE NOT a.disabled`, now, day, week, month).Scan(&v.TotalProfiledAccounts, &v.ActiveUsersLastDay, &v.ActiveUsersLastWeek, &v.ActiveUsersLastMonth, &v.RegisteredUsersLastDay, &v.RegisteredUsersLastWeek, &v.RegisteredUsersLastMonth)
		out = v
	case s.Sphere:
		v := s.PostStats{CalculatedAt: now}
		e = tx.QueryRowContext(ctx, `WITH active AS(SELECT p.* FROM owned_text_posts p JOIN owned_publishers u ON u.id=p.publisher_id WHERE p.deleted_at IS NULL AND u.deleted_at IS NULL)SELECT count(*),count(*)FILTER(WHERE published_at IS NOT NULL),count(*)FILTER(WHERE drafted_at IS NOT NULL),count(*)FILTER(WHERE created_at>=$2 AND created_at<=$1),count(*)FILTER(WHERE created_at>=$3 AND created_at<=$1),count(*)FILTER(WHERE created_at>=$4 AND created_at<=$1),(SELECT count(*)FROM owned_publishers WHERE deleted_at IS NULL),(SELECT count(*)FROM owned_post_reactions r JOIN active p ON p.id=r.post_id),(SELECT count(*)FROM owned_post_bookmarks b JOIN active p ON p.id=b.post_id)FROM active`, now, day, week, month).Scan(&v.TotalPosts, &v.PublishedPosts, &v.DraftPosts, &v.PostsLastDay, &v.PostsLastWeek, &v.PostsLastMonth, &v.TotalPublishers, &v.TotalReactions, &v.TotalBookmarks)
		out = v
	case s.Wallet:
		v := s.WalletStats{CalculatedAt: now}
		e = tx.QueryRowContext(ctx, `SELECT (SELECT count(*)FROM wallet_v1.wallets WHERE deleted_at IS NULL),count(*),count(*)FILTER(WHERE status=$5),count(*)FILTER(WHERE status=$6),count(*)FILTER(WHERE created_at>=$2 AND created_at<=$1),count(*)FILTER(WHERE created_at>=$3 AND created_at<=$1),count(*)FILTER(WHERE created_at>=$4 AND created_at<=$1),(SELECT count(*)FROM commerce_v1.orders),(SELECT count(*)FROM commerce_v1.orders WHERE status=$7),(SELECT count(*)FROM commerce_v1.subscriptions)FROM wallet_v1.transactions WHERE deleted_at IS NULL`, now, day, week, month, accounts.Confirmed, accounts.Pending, commerce.Paid).Scan(&v.TotalWallets, &v.TotalTransactions, &v.ConfirmedTransactions, &v.PendingTransactions, &v.TransactionsLastDay, &v.TransactionsLastWeek, &v.TransactionsLastMonth, &v.TotalOrders, &v.PaidOrders, &v.TotalSubscriptions)
		out = v
	}
	if e != nil {
		return nil, e
	}
	if e = r.global.RequireGlobalInTransaction(ctx, tx, actor, permission); e != nil {
		return nil, e
	}
	return out, tx.Commit()
}
