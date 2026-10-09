-- SPDX-License-Identifier: AGPL-3.0-only
SELECT EXISTS(
 SELECT 1 FROM owned_livestreams s
 JOIN owned_publishers u ON u.id=s.publisher_id
 LEFT JOIN owned_realms realm ON realm.id=u.realm_id
 JOIN owned_authority_accounts owner ON owner.id=COALESCE(u.account_id,realm.account_id)
 WHERE s.id=$2 AND s.publisher_id=$3 AND s.deleted_at IS NULL
 AND s.status IN(0,1) AND s.operation_state IN('ready','starting','active')
 AND NOT owner.disabled AND NOT EXISTS(
  SELECT 1 FROM owned_authority_punishments punishment
  WHERE punishment.account_id=owner.id AND punishment.kind='disable_account'
  AND(punishment.starts_at IS NULL OR punishment.starts_at<=clock_timestamp())
  AND(punishment.expires_at IS NULL OR punishment.expires_at>clock_timestamp()))
 AND NOT EXISTS(SELECT 1 FROM owned_social_effective_relationships blocked
  WHERE blocked.status=-100 AND((blocked.account_id=owner.id AND blocked.related_id=NULLIF($1::text,'')::uuid)
  OR(blocked.related_id=owner.id AND blocked.account_id=NULLIF($1::text,'')::uuid)))
 AND(s.visibility<>2 OR $4::boolean))
