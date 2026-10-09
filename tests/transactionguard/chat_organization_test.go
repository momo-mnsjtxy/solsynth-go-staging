// SPDX-License-Identifier: AGPL-3.0-only
package transactionguard_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
)

type neverOrganizationReader struct{ t *testing.T }

func (r neverOrganizationReader) OrganizationMessages(context.Context, *sql.DB, *sql.Tx, string, []string) ([]messager.MessageProjection, error) {
	r.t.Fatal("hydration preceded authority")
	return nil, nil
}

type neverSearchPerk struct{ t *testing.T }

func (p neverSearchPerk) RequireChatSearch(context.Context, *sql.DB, *sql.Tx, string, time.Time) error {
	p.t.Fatal("entitlement preceded authority")
	return nil
}
func TestChatOrganizationOriginalProofPrecedesAllDomainSQL(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name, permission string
		run              func(*chatpg.Organization) error
	}{
		{"summary", "", func(o *chatpg.Organization) error { _, e := o.Summary(ctx, actor); return e }},
		{"read all", "chat.read.all", func(o *chatpg.Organization) error { return o.ReadAll(ctx, actor) }},
		{"search", "", func(o *chatpg.Organization) error {
			_, e := o.Search(ctx, actor, rooms.SearchRequest{Query: "text", Take: 20})
			return e
		}},
		{"pins", "", func(o *chatpg.Organization) error { _, e := o.Pins(ctx, actor, target, false); return e }},
		{"pin", "chat.pins.manage", func(o *chatpg.Organization) error {
			_, e := o.Pin(ctx, actor, target, rooms.PinRequest{MessageID: target})
			return e
		}},
		{"unpin", "chat.pins.manage", func(o *chatpg.Organization) error { return o.Unpin(ctx, actor, target, target) }},
		{"groups", "", func(o *chatpg.Organization) error { _, e := o.Groups(ctx, actor); return e }},
		{"create group", "chat.groups.manage", func(o *chatpg.Organization) error {
			_, e := o.CreateGroup(ctx, actor, rooms.GroupPatch{Name: ptr("Synthetic")})
			return e
		}},
		{"update group", "", func(o *chatpg.Organization) error {
			_, e := o.UpdateGroup(ctx, actor, target, rooms.GroupPatch{Name: ptr("Synthetic")})
			return e
		}},
		{"delete group", "", func(o *chatpg.Organization) error { return o.DeleteGroup(ctx, actor, target) }},
		{"move group", "", func(o *chatpg.Organization) error { return o.MoveToGroup(ctx, actor, target, &targetGroup) }},
		{"member group", "", func(o *chatpg.Organization) error { _, e := o.GroupMember(ctx, actor, target); return e }},
	} {
		t.Run(c.name, func(t *testing.T) {
			harness(t, actor, c.permission, func(db *sql.DB, g *denyGuard) error {
				r, e := chatpg.NewWithAuthority(db, policy, g)
				if e != nil {
					return e
				}
				return c.run(chatpg.NewOrganization(r, neverOrganizationReader{t}, neverSearchPerk{t}))
			})
		})
	}
}

var targetGroup = target
