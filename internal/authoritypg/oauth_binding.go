// SPDX-License-Identifier: AGPL-3.0-only
package authoritypg

import (
	"database/sql"
	"encoding/json"
	"solsynth.local/go-migration/ports/authority"
	"time"
)

// CheckSessionBinding uses the original canonical SQL snapshot. Write
// transactions already own the user's account row; secondary rows use NOWAIT
// so opposing app-owner/user operations never wait in an inverted lock order.
// Read-only verifiers take no locks and see one coherent repeatable-read view.
func (t *transaction) CheckSessionBinding(se authority.Session, now time.Time) error {
	if se.Type != 1 || se.Category != authority.OAuthSessionCategory || se.AccountID != t.account.ID || !authority.ValidID(se.ClientID) || !authority.ValidID(se.ParentID) {
		return authority.ErrInvalid
	}
	var readOnly string
	if e := t.tx.QueryRowContext(t.ctx, `SHOW transaction_read_only`).Scan(&readOnly); e != nil {
		return authority.ErrUnavailable
	}
	if readOnly != "on" {
		var owner sql.NullString
		var pub, project string
		if e := t.tx.QueryRowContext(t.ctx, `SELECT pub.account_id::text,pub.id::text,p.id::text FROM owned_custom_apps a JOIN owned_dev_projects p ON p.id=a.project_id JOIN owned_developers d ON d.id=p.developer_id JOIN owned_publishers pub ON pub.id=d.publisher_id WHERE a.id=$1`, se.ClientID).Scan(&owner, &pub, &project); e != nil {
			return authority.ErrInvalid
		}
		locks := []struct{ table, id string }{}
		if owner.Valid && owner.String != se.AccountID {
			locks = append(locks, struct{ table, id string }{"owned_authority_accounts", owner.String})
		}
		locks = append(locks, struct{ table, id string }{"owned_publishers", pub}, struct{ table, id string }{"owned_dev_projects", project}, struct{ table, id string }{"owned_custom_apps", se.ClientID})
		for _, v := range locks {
			var id string
			if e := t.tx.QueryRowContext(t.ctx, `SELECT id::text FROM `+v.table+` WHERE id=$1 FOR SHARE NOWAIT`, v.id).Scan(&id); e != nil {
				return authority.ErrUnavailable
			}
		}
		var id string
		if e := t.tx.QueryRowContext(t.ctx, `SELECT account_id::text FROM owned_app_consents WHERE account_id=$1 AND app_id=$2 FOR SHARE NOWAIT`, se.AccountID, se.ClientID).Scan(&id); e != nil {
			return bindingLockError(e)
		}
		if e := t.tx.QueryRowContext(t.ctx, `SELECT session_id::text FROM owned_oauth_grants WHERE session_id=$1 FOR SHARE NOWAIT`, se.ID).Scan(&id); e != nil {
			return bindingLockError(e)
		}
	}
	var scopes []byte
	var valid bool
	e := t.tx.QueryRowContext(t.ctx, `SELECT g.scopes,
 g.account_id=$2 AND g.app_id=$3 AND g.parent_id=$4 AND g.revoked_at IS NULL AND g.expires_at>$5 AND a.oauth_epoch=g.app_epoch AND a.status=1 AND a.deleted_at IS NULL AND p.deleted_at IS NULL AND pub.deleted_at IS NULL AND pub.account_id IS NOT DISTINCT FROM g.owner_id
 AND c.version=g.consent_version AND c.revoked_at IS NULL AND c.expires_at>$5 AND c.scopes @> g.scopes
 AND parent.account_id=$2 AND parent.type=0 AND parent.revoked_at IS NULL AND parent.expires_at>$5 AND parent.account_version=$6
 AND (g.owner_id IS NULL OR (NOT owner.disabled AND owner.version=g.owner_version AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments punish WHERE punish.account_id=owner.id AND punish.kind='disable_account' AND(punish.starts_at IS NULL OR punish.starts_at<=$5)AND(punish.expires_at IS NULL OR punish.expires_at>$5))))
 AND (g.secret_id IS NULL OR EXISTS(SELECT 1 FROM owned_app_secrets secret WHERE secret.id=g.secret_id AND secret.app_id=g.app_id AND secret.revoked_at IS NULL AND secret.expires_at>$5 AND secret.secret_hash=g.secret_hash AND secret.scopes @> '["oauth.client.authenticate"]'::jsonb))
 FROM owned_oauth_grants g JOIN owned_custom_apps a ON a.id=g.app_id JOIN owned_dev_projects p ON p.id=a.project_id JOIN owned_developers d ON d.id=p.developer_id JOIN owned_publishers pub ON pub.id=d.publisher_id LEFT JOIN owned_authority_accounts owner ON owner.id=pub.account_id JOIN owned_app_consents c ON c.account_id=g.account_id AND c.app_id=g.app_id JOIN owned_authority_sessions parent ON parent.id=g.parent_id WHERE g.session_id=$1`, se.ID, se.AccountID, se.ClientID, se.ParentID, now, t.account.Version).Scan(&scopes, &valid)
	if e != nil || !valid {
		return authority.ErrInvalid
	}
	var declared []string
	if json.Unmarshal(scopes, &declared) != nil || len(declared) != len(se.Scopes) {
		return authority.ErrInvalid
	}
	seen := map[string]bool{}
	for _, s := range declared {
		if !authority.OAuthScope(s) || seen[s] {
			return authority.ErrInvalid
		}
		seen[s] = true
	}
	for _, s := range se.Scopes {
		if !seen[s] {
			return authority.ErrInvalid
		}
	}
	if !seen["openid"] {
		return authority.ErrInvalid
	}
	return nil
}
