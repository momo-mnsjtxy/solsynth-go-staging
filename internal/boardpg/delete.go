// SPDX-License-Identifier: AGPL-3.0-only
package boardpg

import (
	"context"
	"solsynth.local/go-migration/ports/passport/board"
)

// Delete is available only under the repository's explicit synthetic policy.
// The legacy Remove/SaveChanges hooks are unverified. This candidate physically
// deletes one account-owned active row and does not purge cache, matching the
// owned controller's observable lack of a purge call. It does not claim audit
// hook parity or provide a production delete adapter.
func (r *Repository) Delete(ctx context.Context, account, id string) error {
	if !validID(id) {
		return ErrInvalidID
	}
	return r.WithinAccount(ctx, account, func(repo board.Repository) error {
		tx := repo.(*transactionRepository)
		result, err := tx.tx.ExecContext(ctx, `DELETE FROM account_board_items WHERE account_id=$1::uuid AND id=$2::uuid AND deleted_at IS NULL`, account, id)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return board.ErrNotFound
		}
		return nil
	})
}
