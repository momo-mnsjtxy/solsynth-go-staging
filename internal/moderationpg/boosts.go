// SPDX-License-Identifier: AGPL-3.0-only
package moderationpg

import "solsynth.local/go-migration/internal/socialpg"

// The administrator role-assignment gate uses the same live paid entitlement.
func (r *Repository) WithRealmBoosts(p socialpg.BoostProvider) *Repository {
	r.social.WithBoosts(p)
	return r
}
