// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
)

// ResolvePublicInTransaction reuses canonical current metadata and retains the
// file lock through the caller's commit. It grants no authority and changes no
// media visibility. Callers authenticate before invoking and serve bytes only
// through the existing media.Open route, which independently checks visibility.
func (r *Repository) ResolvePublicInTransaction(ctx context.Context, tx *sql.Tx, actor, id string) (media.File, error) {
	if r == nil || r.db == nil || tx == nil {
		return media.File{}, media.ErrUnavailable
	}
	if !authority.ValidID(id) {
		return media.File{}, media.ErrNotFound
	}
	if actor != "" && !authority.ValidID(actor) {
		return media.File{}, media.ErrForbidden
	}
	return scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM owned_media_files WHERE id=$1 AND state='ready' AND is_public AND EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id=owned_media_files.account_id AND NOT a.disabled) FOR SHARE`, id))
}
