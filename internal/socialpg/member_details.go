// SPDX-License-Identifier: AGPL-3.0-only
package socialpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/passport/social"
)

// MemberDetails joins optional canonical label and scoped progression data to
// the caller's existing transaction. No domain side effects occur in this read.
type MemberDetails interface {
	EnrichMemberInTransaction(context.Context, *sql.Tx, *social.Member) error
}

func (r *Repository) WithMemberDetails(p MemberDetails) *Repository { r.memberDetails = p; return r }
func (r *Repository) enrichMember(ctx context.Context, tx *sql.Tx, m *social.Member) error {
	if r.memberDetails == nil {
		return nil
	}
	return r.memberDetails.EnrichMemberInTransaction(ctx, tx, m)
}
