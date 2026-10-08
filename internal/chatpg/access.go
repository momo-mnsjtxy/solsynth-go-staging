// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
)

//go:embed relations_schema.sql
var RelationsSchema string
var _ rooms.AccessPolicy = (*Repository)(nil)
var expression = regexp.MustCompile(`^(\$[1-9][0-9]*|[a-z_][a-z0-9_]*(\.[a-z_][a-z0-9_]*)?)$`)

func (r *Repository) ReadSQL(room, actor string) string {
	if !expression.MatchString(room) || !expression.MatchString(actor) {
		return "FALSE"
	}
	base := fmt.Sprintf(`EXISTS(SELECT 1 FROM chat_rooms access_base WHERE access_base.id=(%s)::uuid AND NOT access_base.owned_relation_guard AND access_base.deleted_at IS NULL)`, room)
	if r == nil || r.social == nil {
		return base
	}
	viewer := fmt.Sprintf(`NULLIF((%s)::text,'')::uuid`, actor)
	return `(` + base + ` OR EXISTS(SELECT 1 FROM owned_chat_direct_pairs pair JOIN chat_rooms linked ON linked.id=pair.room_id JOIN owned_authority_accounts low_account ON low_account.id=pair.account_low JOIN owned_authority_accounts high_account ON high_account.id=pair.account_high WHERE pair.room_id=(` + room + `)::uuid AND linked.owned_type=1 AND linked.deleted_at IS NULL AND ` + viewer + ` IN(pair.account_low,pair.account_high) AND NOT low_account.disabled AND NOT high_account.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id IN(pair.account_low,pair.account_high) AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=statement_timestamp())AND(p.expires_at IS NULL OR p.expires_at>statement_timestamp())) AND NOT EXISTS(SELECT 1 FROM owned_social_effective_relationships relation WHERE relation.status=-100 AND((relation.account_id=pair.account_low AND relation.related_id=pair.account_high)OR(relation.account_id=pair.account_high AND relation.related_id=pair.account_low))) AND (SELECT count(*) FROM chat_members m WHERE m.chat_room_id=pair.room_id AND m.account_id IN(pair.account_low,pair.account_high) AND m.joined_at IS NOT NULL AND m.leave_at IS NULL AND m.deleted_at IS NULL AND NOT m.owned_banned)=2)
 OR EXISTS(SELECT 1 FROM owned_chat_realm_links link JOIN owned_realms realm ON realm.id=link.realm_id JOIN chat_rooms linked ON linked.id=link.room_id JOIN owned_authority_accounts owner ON owner.id=realm.account_id WHERE link.room_id=(` + room + `)::uuid AND linked.owned_type=0 AND linked.deleted_at IS NULL AND realm.deleted_at IS NULL AND NOT owner.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=owner.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=statement_timestamp())AND(p.expires_at IS NULL OR p.expires_at>statement_timestamp())) AND((realm.is_public AND linked.is_public)OR EXISTS(SELECT 1 FROM owned_realm_members member JOIN owned_authority_accounts account ON account.id=member.account_id WHERE member.realm_id=realm.id AND member.account_id=` + viewer + ` AND member.joined_at IS NOT NULL AND member.leave_at IS NULL AND NOT account.disabled))))`
}
func (r *Repository) SendSQL(room, actor string) string {
	if !expression.MatchString(room) || !expression.MatchString(actor) {
		return "FALSE"
	}
	viewer := fmt.Sprintf(`NULLIF((%s)::text,'')::uuid`, actor)
	permission := "FALSE"
	if provider, ok := r.social.(social.PermissionProvider); ok {
		permission = provider.RealmPermissionSQL("permission_link.realm_id", actor, social.CanChat)
	}
	realmGate := ` AND NOT EXISTS(SELECT 1 FROM owned_chat_realm_links permission_link WHERE permission_link.room_id=(` + room + `)::uuid AND NOT (` + permission + `))`
	return `(` + r.ReadSQL(room, actor) + realmGate + ` AND EXISTS(SELECT 1 FROM chat_members sending JOIN chat_rooms linked ON linked.id=sending.chat_room_id WHERE sending.chat_room_id=(` + room + `)::uuid AND sending.account_id=` + viewer + ` AND sending.joined_at IS NOT NULL AND sending.leave_at IS NULL AND sending.deleted_at IS NULL AND NOT sending.owned_banned AND(sending.owned_timeout_until IS NULL OR sending.owned_timeout_until<=clock_timestamp())AND(linked.owned_type<>1 OR sending.owned_confirmed_at IS NOT NULL)) AND NOT EXISTS(SELECT 1 FROM owned_chat_realm_links link WHERE link.room_id=(` + room + `)::uuid AND NOT EXISTS(SELECT 1 FROM owned_realm_members member WHERE member.realm_id=link.realm_id AND member.account_id=` + viewer + ` AND member.joined_at IS NOT NULL AND member.leave_at IS NULL)))`
}
func (r *Repository) CallSQL(room, actor string) string {
	if !expression.MatchString(room) || !expression.MatchString(actor) {
		return "FALSE"
	}
	return `(` + r.SendSQL(room, actor) + ` AND NOT EXISTS(SELECT 1 FROM chat_rooms linked JOIN chat_members member ON member.chat_room_id=linked.id WHERE linked.id=(` + room + `)::uuid AND linked.owned_type=1 AND member.owned_confirmed_at IS NULL))`
}
func (r *Repository) CanReadInTransaction(ctx context.Context, tx *sql.Tx, actor, rid string) (bool, error) {
	if tx == nil {
		return false, rooms.ErrUnavailable
	}
	var yes bool
	if r == nil || r.social == nil {
		e := tx.QueryRowContext(ctx, `SELECT NOT owned_relation_guard FROM chat_rooms WHERE id=$1 AND deleted_at IS NULL`, rid).Scan(&yes)
		return yes, e
	}
	e := tx.QueryRowContext(ctx, `SELECT `+r.ReadSQL("$1", "$2"), rid, actor).Scan(&yes)
	return yes, e
}
func (r *Repository) readable(ctx context.Context, tx *sql.Tx, v rooms.Room, actor string) error {
	if v.EncryptionMode != r.policy.NoneEncryptionMode {
		v.IsPublic = false
	}
	if e := readable(ctx, tx, v, actor); e != nil {
		return e
	}
	yes, e := r.CanReadInTransaction(ctx, tx, actor, v.ID)
	if e != nil {
		return e
	}
	if !yes {
		return rooms.ErrForbidden
	}
	return nil
}
func (r *Repository) RelationsReady(ctx context.Context) error {
	if r == nil || r.db == nil || r.social == nil || r.guard == nil {
		return rooms.ErrUnavailable
	}
	if e := r.MLSReady(ctx); e != nil {
		return e
	}
	if r.associations != nil {
		if e := r.associations.Ready(ctx); e != nil {
			return e
		}
	}
	_, e := r.db.ExecContext(ctx, `SELECT account_low,account_high,room_id FROM owned_chat_direct_pairs LIMIT 0; SELECT room_id,realm_id FROM owned_chat_realm_links LIMIT 0;SELECT action,occurred_at FROM owned_chat_relations_audit LIMIT 0;SELECT owned_relation_guard,owned_type FROM chat_rooms LIMIT 0;SELECT owned_confirmed_at,owned_timeout_until FROM chat_members LIMIT 0`)
	return e
}
func canonicalAccount(ctx context.Context, tx *sql.Tx, id string) (*accounts.Account, error) {
	var a accounts.Account
	var profileID sql.NullString
	var bio, pronouns, location, zone sql.NullString
	var links []byte
	e := tx.QueryRowContext(ctx, `SELECT a.id::text,a.name,a.nick,a.language,a.region,a.created_at,p.id::text,p.bio,p.pronouns,p.location,p.time_zone,p.links FROM owned_authority_accounts a LEFT JOIN owned_accounts_profiles p ON p.account_id=a.id WHERE a.id=$1 AND NOT a.disabled`, id).Scan(&a.ID, &a.Name, &a.Nick, &a.Language, &a.Region, &a.CreatedAt, &profileID, &bio, &pronouns, &location, &zone, &links)
	if e != nil {
		return nil, e
	}
	if profileID.Valid {
		a.Profile = &accounts.Profile{ID: profileID.String, Bio: bio.String, Pronouns: pronouns.String, Location: location.String, TimeZone: zone.String}
		if e = json.Unmarshal(links, &a.Profile.Links); e != nil {
			return nil, e
		}
	}
	return &a, nil
}
func (r *Repository) decorateRoom(ctx context.Context, tx *sql.Tx, v *rooms.Room, actor string) error {
	if r.mls && v.EncryptionMode == rooms.MLSEncryptionMode {
		if e := tx.QueryRowContext(ctx, `SELECT id,epoch,state_version FROM owned_mls_groups WHERE room_id=$1`, v.ID).Scan(&v.MLSGroupID, &v.MLSEpoch, &v.MLSStateVersion); e != nil {
			return e
		}
	}
	if !v.RelationGuard {
		return nil
	}
	if r.social == nil {
		return rooms.ErrUnavailable
	}
	if v.Type == 0 {
		var id string
		e := tx.QueryRowContext(ctx, `SELECT realm_id::text FROM owned_chat_realm_links WHERE room_id=$1`, v.ID).Scan(&id)
		if e != nil {
			return rooms.ErrUnavailable
		}
		v.RealmID = &id
		realm, e := r.social.RealmInTransaction(ctx, tx, id)
		if e != nil {
			if errors.Is(e, social.ErrNotFound) || errors.Is(e, social.ErrForbidden) {
				return nil
			}
			return e
		}
		v.Realm = &realm
		return nil
	}
	if v.Type != 1 {
		return rooms.ErrUnsupported
	}
	if actor == "" {
		return nil
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+memberColumns+` FROM chat_members WHERE chat_room_id=$1 AND deleted_at IS NULL ORDER BY account_id`, v.ID)
	if e != nil {
		return e
	}
	members := []rooms.Member{}
	for rows.Next() {
		m, e := scanMember(rows)
		if e != nil {
			rows.Close()
			return e
		}
		if m.AccountID != actor {
			m.LastReadAt = nil
		}
		members = append(members, m)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for i := range members {
		a, e := canonicalAccount(ctx, tx, members[i].AccountID)
		if e != nil {
			if errors.Is(e, sql.ErrNoRows) {
				continue
			}
			return e
		}
		members[i].Account = a
	}
	v.Members = members
	v.DirectMembers = []rooms.Member{}
	for _, m := range members {
		if m.AccountID != actor {
			v.DirectMembers = append(v.DirectMembers, m)
		}
	}
	return nil
}
