// SPDX-License-Identifier: AGPL-3.0-only
package taxonomypg

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/taxonomy"
)

type TransactionGuard interface {
	Authenticate(context.Context, *sql.DB, *sql.Tx, string) (authority.Principal, error)
	Authorize(context.Context, *sql.DB, *sql.Tx, string, ...string) (authority.Principal, error)
}

func (r *Repository) authorize(ctx context.Context, tx *policyTx, actor, permission string) error {
	if r.authorityGuard == nil {
		return taxonomy.ErrPolicyUnverified
	}
	var err error
	if permission == "" {
		_, err = r.authorityGuard.Authenticate(ctx, r.db, tx.Tx, actor)
	} else {
		_, err = r.authorityGuard.Authorize(ctx, r.db, tx.Tx, actor, permission)
	}
	var failure *authority.Failure
	if errors.As(err, &failure) {
		if failure.Status == 401 {
			return taxonomy.ErrUnauthenticated
		}
		if failure.Status == 403 || failure.Status == 423 {
			return taxonomy.ErrForbidden
		}
	}
	return err
}

// These grants match taxonomy.Service.Create and Service.Change, including
// their authentication-only publisher-owned operations. Metadata is never a
// privilege claim: actor equality and each admin grant are checked by the guard.
func mutationPermission(k taxonomy.Kind, admin bool, op taxonomy.Mutation) (string, error) {
	if k == taxonomy.Category {
		if admin && (op == "create" || op == taxonomy.Update || op == taxonomy.Delete) {
			return "post.categories.manage", nil
		}
		return "", taxonomy.ErrForbidden
	}
	if k != taxonomy.Tag {
		return "", taxonomy.ErrInvalid
	}
	if !admin {
		switch op {
		case "create", taxonomy.Update, taxonomy.Release, taxonomy.Protect:
			return "", nil
		case taxonomy.Claim:
			return "posts.tags.claim", nil
		default:
			return "", taxonomy.ErrForbidden
		}
	}
	switch op {
	case "create":
		return "posts.tags.create", nil
	case taxonomy.Update:
		return "posts.tags.update", nil
	case taxonomy.Assign, taxonomy.Unassign:
		return "posts.tags.assign", nil
	case taxonomy.Protect:
		return "posts.tags.protect", nil
	case taxonomy.Event:
		return "posts.tags.event", nil
	case taxonomy.Delete:
		return "posts.tags.delete", nil
	default:
		return "", taxonomy.ErrInvalid
	}
}

func (r *Repository) authorizeRecord(ctx context.Context, tx *policyTx, row taxonomy.Record, op taxonomy.Mutation) error {
	permission, err := mutationPermission(row.Kind, row.AuditAdmin, op)
	if err != nil {
		return err
	}
	if !authority.ValidID(row.AuditActorID) {
		return taxonomy.ErrInvalid
	}
	return r.authorize(ctx, tx, row.AuditActorID, permission)
}

func sameOwner(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

func lockPublishers(ctx context.Context, tx *policyTx, publishers ...*string) error {
	owners := map[string]bool{}
	for _, owner := range publishers {
		if owner != nil {
			owners[*owner] = true
		}
	}
	ids := make([]string, 0, len(owners))
	for id := range owners {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		var locked string
		if err := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, id).Scan(&locked); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return taxonomy.ErrForbidden
			}
			return err
		}
	}
	return nil
}

// Revalidate publisher membership and protection quota inside the mutation
// transaction, rather than relying on the service callback's separate reads.
// The caller already locked all publishers before the taxonomy table lock.
func (r *Repository) publisherPolicy(ctx context.Context, tx *policyTx, before, after taxonomy.Record, op taxonomy.Mutation) error {
	if after.Kind != taxonomy.Tag {
		return nil
	}
	if !after.AuditAdmin {
		owner, role := before.OwnerPublisherID, 100
		if op == "create" || op == taxonomy.Claim {
			owner, role = after.OwnerPublisherID, 0
		}
		if owner == nil {
			return taxonomy.ErrForbidden
		}
		var allowed bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publisher_members m JOIN owned_publishers p ON p.id=m.publisher_id WHERE m.publisher_id=$1 AND m.account_id=$2 AND m.role>=$3 AND m.joined_at IS NOT NULL AND m.revoked_at IS NULL AND p.deleted_at IS NULL AND (p.account_id IS NULL OR EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id=p.account_id AND NOT a.disabled)))`, *owner, after.AuditActorID, role).Scan(&allowed)
		if err != nil {
			return err
		}
		if !allowed {
			return taxonomy.ErrForbidden
		}
	}
	if op == taxonomy.Protect && after.IsProtected && !before.IsProtected {
		if after.OwnerPublisherID == nil {
			return taxonomy.ErrForbidden
		}
		var total, used int64
		// Lock first, then use a fresh clock so an entitlement expiring while
		// waiting is not extended by the locking statement's timestamp.
		if err := tx.QueryRowContext(ctx, `SELECT protected_tag_total FROM owned_publisher_entitlements WHERE publisher_id=$1 FOR SHARE`, *after.OwnerPublisherID).Scan(&total); err != nil {
			return taxonomy.ErrForbidden
		}
		if err := tx.QueryRowContext(ctx, `SELECT protected_tag_total FROM owned_publisher_entitlements WHERE publisher_id=$1 AND starts_at<=statement_timestamp() AND (expires_at IS NULL OR expires_at>statement_timestamp())`, *after.OwnerPublisherID).Scan(&total); err != nil {
			return taxonomy.ErrForbidden
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_taxonomy.post_tags WHERE owner_publisher_id=$1 AND is_protected AND deleted_at IS NULL`, *after.OwnerPublisherID).Scan(&used); err != nil {
			return err
		}
		if used >= total {
			return taxonomy.ErrForbidden
		}
	}
	return nil
}
