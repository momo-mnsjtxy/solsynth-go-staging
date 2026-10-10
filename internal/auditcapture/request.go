// SPDX-License-Identifier: AGPL-3.0-only
package auditcapture

import (
	"context"
	"database/sql"
	"net"
	"net/http"
	"net/netip"
	"solsynth.local/go-migration/ports/authority"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type key struct{}
type request struct{ actor, session, peer, agent string }

// Bind attaches transport facts and the verifier's original principal. These
// values annotate a canonical audit; they never act as an authentication proof.
// Forwarded headers, request bodies, query strings and tokens are not retained.
func Bind(ctx context.Context, r *http.Request, p authority.Principal) context.Context {
	if r == nil || !authority.ValidID(p.Account.ID) || !authority.ValidID(p.Session.ID) {
		return ctx
	}
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		host = r.RemoteAddr
	}
	peer := ""
	if ip, e := netip.ParseAddr(host); e == nil && ip.Zone() == "" {
		peer = ip.Unmap().String()
	}
	agent := strings.ToValidUTF8(r.UserAgent(), "")
	agent = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, agent)
	if utf8.RuneCountInString(agent) > 512 {
		agent = string([]rune(agent)[:512])
	}
	return context.WithValue(ctx, key{}, request{p.Account.ID, p.Session.ID, peer, agent})
}

// Record joins the original domain audit transaction. Callers supply only the
// returned canonical audit ID; no HTTP field can select actor, source or event.
func Record(ctx context.Context, tx *sql.Tx, actor, source string, id int64) error {
	v, ok := ctx.Value(key{}).(request)
	if !ok || v.actor != actor {
		return nil
	}
	if tx == nil || id < 1 {
		return authority.ErrInvalid
	}
	column := "actor_id"
	switch source {
	case "owned_accounts_audit", "owned_social_audit", "owned_progression_audit":
	case "owned_authority_contact_audit", "owned_authority_factor_audit":
		column = "account_id"
	default:
		return authority.ErrInvalid
	}
	var owner string
	if e := tx.QueryRowContext(ctx, `SELECT `+column+`::text FROM `+source+` WHERE id=$1::bigint`, id).Scan(&owner); e != nil {
		return e
	}
	if owner != actor {
		return authority.ErrForbidden
	}
	// The session is a historical request fact, so deliberate credential changes
	// may invalidate it during this same transaction. Ownership must still match.
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_account_action_context(source,source_id,account_id,session_id,ip_address,user_agent,created_at)VALUES($1::text,$2::text,$3::uuid,$4::uuid,NULLIF($5::text,'')::inet,NULLIF($6::text,''),clock_timestamp())`, source, strconv.FormatInt(id, 10), actor, v.session, v.peer, v.agent)
	return e
}

// Account appends a canonical account-domain audit for new owned producers.
// Its action and target are chosen by the implementation, never a request body.
func Account(ctx context.Context, tx *sql.Tx, actor, action, target string) error {
	if tx == nil || !authority.ValidID(actor) || !authority.ValidID(target) {
		return authority.ErrInvalid
	}
	var id int64
	if e := tx.QueryRowContext(ctx, `INSERT INTO owned_accounts_audit(actor_id,action,target_id,occurred_at)VALUES($1::uuid,$2::text,$3::uuid,clock_timestamp())RETURNING id`, actor, action, target).Scan(&id); e != nil {
		return e
	}
	return Record(ctx, tx, actor, "owned_accounts_audit", id)
}
