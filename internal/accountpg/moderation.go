// SPDX-License-Identifier: AGPL-3.0-only
package accountpg

// PublisherModerationPredicate is a fixed SQL fragment for canonical aliases.
// A publisher editor's management read exception also requires current realm
// membership; neither global administrator status nor a public realm suffices.
func PublisherModerationPredicate(alias, actor, realmMember string) string {
	return `(` + alias + `.shadowbanned_at IS NULL OR (` + realmMember + ` AND EXISTS(SELECT 1 FROM owned_publisher_members moderation_member JOIN owned_authority_accounts moderation_actor ON moderation_actor.id=moderation_member.account_id WHERE moderation_member.publisher_id=` + alias + `.id AND moderation_member.account_id=NULLIF(` + actor + `::text,'')::uuid AND moderation_member.joined_at IS NOT NULL AND moderation_member.revoked_at IS NULL AND moderation_member.role>=50 AND NOT moderation_actor.disabled)))`
}
