// SPDX-License-Identifier: AGPL-3.0-only
package accountpg

// ActivePublisherOwnerPredicate accepts only a compile-time publisher alias.
// Organization publishers use the canonical realm owner. Time-dependent account
// punishments must be checked in the current statement, including after waits.
func ActivePublisherOwnerPredicate(alias string) string {
	return `EXISTS(SELECT 1 FROM owned_authority_accounts subscription_owner WHERE subscription_owner.id=COALESCE(` + alias + `.account_id,(SELECT subscription_realm.account_id FROM owned_realms subscription_realm WHERE subscription_realm.id=` + alias + `.realm_id AND subscription_realm.deleted_at IS NULL)) AND NOT subscription_owner.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments subscription_punishment WHERE subscription_punishment.account_id=subscription_owner.id AND subscription_punishment.kind='disable_account' AND(subscription_punishment.starts_at IS NULL OR subscription_punishment.starts_at<=clock_timestamp()) AND(subscription_punishment.expires_at IS NULL OR subscription_punishment.expires_at>clock_timestamp())))`
}
