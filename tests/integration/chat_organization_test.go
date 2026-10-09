// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/passport/social"
	c "solsynth.local/go-migration/ports/wallet/commerce"
)

// Explicit test-only entitlement for search visibility cases. A separate case
// exercises actual canonical paid system subscriptions and final revalidation.
type syntheticOrganizationPerk struct{}

func (syntheticOrganizationPerk) RequireChatSearch(context.Context, *sql.DB, *sql.Tx, string, time.Time) error {
	return nil
}

// Pause only after canonical hydration succeeds, so the second entitlement
// pass can encounter a newly begun paid cycle that the first pass did not lock.
type organizationReadGate struct {
	reader  rooms.OrganizationMessageReader
	entered chan struct{}
	release <-chan struct{}
}

func (g organizationReadGate) OrganizationMessages(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, ids []string) ([]messager.MessageProjection, error) {
	out, e := g.reader.OrganizationMessages(ctx, db, tx, actor, ids)
	if e != nil {
		return nil, e
	}
	close(g.entered)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-g.release:
		return out, nil
	}
}

type organizationFixture struct {
	chatRelationsFixture
	org  *chatpg.Organization
	http http.Handler
}

func newOrganizationFixture(t *testing.T) organizationFixture {
	t.Helper()
	f := organizationFixture{chatRelationsFixture: newChatRelationsFixture(t)}
	query(t, f.db, chatpg.OrganizationSchema)
	f.org = chatpg.NewOrganization(f.chats, f.read, syntheticOrganizationPerk{})
	f.http = (&rooms.Service{Repository: f.chats, Organization: f.org, Authority: f.verifier, Reader: f.read, Bind: f.guard.Bind}).Handler()
	if e := f.org.Ready(f.base); e != nil {
		t.Fatal(e)
	}
	return f
}
func orgGroup(t *testing.T, f organizationFixture, actor, name string) rooms.ChatGroup {
	t.Helper()
	g, e := f.org.CreateGroup(f.contexts[actor], actor, rooms.GroupPatch{Name: &name})
	if e != nil {
		t.Fatal(e)
	}
	return g
}
func orgSummary(t *testing.T, f organizationFixture, actor string) map[string]rooms.ChatSummary {
	t.Helper()
	v, e := f.org.Summary(f.contexts[actor], actor)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestPGChatOrganizationSummaryReadAllVisibility(t *testing.T) {
	f := newOrganizationFixture(t)
	r := threadRoom(t, f.chatRelationsFixture, false, f.other)
	hidden := threadRoom(t, f.chatRelationsFixture, false)
	m := relationSend(t, f.chatRelationsFixture, f.owner, r.ID, "Summary content")
	relationSend(t, f.chatRelationsFixture, f.owner, hidden.ID, "Never visible")
	v := orgSummary(t, f, f.other)
	if len(v) != 1 || v[r.ID].UnreadCount != 1 || !v[r.ID].HasUnread || v[r.ID].LastMessage == nil || v[r.ID].LastMessage.ID != m.ID {
		t.Fatal(v)
	}
	if _, e := f.chats.SetNotify(f.contexts[f.other], f.other, r.ID, chatPolicy.NotifyNone); e != nil {
		t.Fatal(e)
	}
	v = orgSummary(t, f, f.other)
	if v[r.ID].UnreadCount != 0 || !v[r.ID].HasUnread {
		t.Fatal("muted dot", v)
	}
	// A retained future-dated row must not make the next send disappear under
	// the read-all cursor. The sender's room sequence clock advances beyond it.
	query(t, f.db, `UPDATE chat_messages SET created_at=clock_timestamp()+interval '1 hour' WHERE id=$1`, m.ID)
	if e := f.org.ReadAll(f.contexts[f.other], f.other); e != nil {
		t.Fatal(e)
	}
	v = orgSummary(t, f, f.other)
	if v[r.ID].HasUnread {
		t.Fatal(v)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_chat_read_receipts WHERE account_id=$1`, f.other); n != 1 {
		t.Fatal(n)
	}
	relationSend(t, f.chatRelationsFixture, f.owner, r.ID, "After read all")
	if !orgSummary(t, f, f.other)[r.ID].HasUnread {
		t.Fatal("new message swallowed")
	}
	if e := f.chats.DeleteMessage(f.contexts[f.owner], f.owner, r.ID, m.ID); e != nil {
		t.Fatal(e)
	}
	for _, s := range orgSummary(t, f, f.other) {
		if s.LastMessage != nil && s.LastMessage.ID == m.ID {
			t.Fatal("deleted message revived")
		}
	}
	dm := relationDirect(t, f.chatRelationsFixture)
	relationSend(t, f.chatRelationsFixture, f.owner, dm.ID, "Private direct")
	if _, e := f.chats.ChangeDirect(f.contexts[f.other], f.other, dm.ID, "block"); e != nil {
		t.Fatal(e)
	}
	if _, ok := orgSummary(t, f, f.other)[dm.ID]; ok {
		t.Fatal("blocked direct in summary")
	}
	if e := f.org.ReadAll(f.contexts[f.other], f.other); e != nil {
		t.Fatal(e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_chat_read_receipts WHERE account_id=$1 AND room_id=$2`, f.other, dm.ID); n != 0 {
		t.Fatal("blocked direct cursor changed")
	}
}
func TestPGChatOrganizationSearchLiteralFiltersAndPrivacy(t *testing.T) {
	f := newOrganizationFixture(t)
	r := threadRoom(t, f.chatRelationsFixture, false, f.other)
	secret := threadRoom(t, f.chatRelationsFixture, false)
	first := relationSend(t, f.chatRelationsFixture, f.owner, r.ID, "Price 100%_\\literal")
	second := relationSend(t, f.chatRelationsFixture, f.other, r.ID, "PRICE 100%_\\literal second")
	relationSend(t, f.chatRelationsFixture, f.owner, r.ID, "Price 100XXliteral wildcard bait")
	relationSend(t, f.chatRelationsFixture, f.owner, secret.ID, "Price 100%_\\literal secret")
	gone := relationSend(t, f.chatRelationsFixture, f.owner, r.ID, "Price 100%_\\literal deleted")
	if e := f.chats.DeleteMessage(f.contexts[f.owner], f.owner, r.ID, gone.ID); e != nil {
		t.Fatal(e)
	}
	encrypted := relationSend(t, f.chatRelationsFixture, f.owner, r.ID, "Price 100%_\\literal ciphertext")
	query(t, f.db, `UPDATE chat_messages SET encryption_meta='{"algorithm":"synthetic"}' WHERE id=$1`, encrypted.ID)
	q := rooms.SearchRequest{Query: "100%_\\literal", Take: 1}
	page, e := f.org.Search(f.contexts[f.other], f.other, q)
	if e != nil || page.Total != 2 || len(page.Rooms) != 1 || len(page.Rooms[0].Messages) != 1 || page.Rooms[0].Messages[0].ID != second.ID {
		t.Fatal(page, e)
	}
	q.Offset = 1
	page, e = f.org.Search(f.contexts[f.other], f.other, q)
	if e != nil || len(page.Rooms) != 1 || page.Rooms[0].Messages[0].ID != first.ID {
		t.Fatal(page, e)
	}
	q.Offset = 0
	q.Sender = f.owner
	q.After = &first.CreatedAt
	q.Before = &second.CreatedAt
	page, e = f.org.Search(f.contexts[f.other], f.other, q)
	if e != nil || page.Total != 1 || page.Rooms[0].Messages[0].ID != first.ID {
		t.Fatal(page, e)
	}
	q.RoomIDs = []string{secret.ID}
	page, e = f.org.Search(f.contexts[f.other], f.other, q)
	if e != nil || page.Total != 0 || len(page.Rooms) != 0 {
		t.Fatal("room selector leaked", page, e)
	}
	if _, e = f.chats.ChangeMember(f.contexts[f.other], f.other, r.ID, rooms.Leave, ""); e != nil {
		t.Fatal(e)
	}
	q.RoomIDs = nil
	q.After = nil
	q.Before = nil
	q.Sender = ""
	page, e = f.org.Search(f.contexts[f.other], f.other, q)
	if e != nil || page.Total != 0 || len(page.Rooms) != 0 {
		t.Fatal("former member search", page, e)
	}
	dm := relationDirect(t, f.chatRelationsFixture)
	relationSend(t, f.chatRelationsFixture, f.owner, dm.ID, "Price 100%_\\literal blocked direct")
	if _, e = f.chats.ChangeDirect(f.contexts[f.other], f.other, dm.ID, "block"); e != nil {
		t.Fatal(e)
	}
	page, e = f.org.Search(f.contexts[f.other], f.other, q)
	if e != nil || page.Total != 0 || len(page.Rooms) != 0 {
		t.Fatal("blocked direct search leaked", page, e)
	}
	if _, e = chatpg.NewOrganization(f.chats, f.read, nil).Search(f.contexts[f.owner], f.owner, q); !errors.Is(e, rooms.ErrUnavailable) {
		t.Fatal("unconfigured entitlement admitted", e)
	}
}
func TestPGChatOrganizationPinsAuthorizationExpiryAndEvents(t *testing.T) {
	f := newOrganizationFixture(t)
	r := threadRoom(t, f.chatRelationsFixture, true, f.other)
	private := threadRoom(t, f.chatRelationsFixture, false)
	msg := relationSend(t, f.chatRelationsFixture, f.owner, r.ID, "Pinned content")
	other := relationSend(t, f.chatRelationsFixture, f.owner, private.ID, "Private target")
	if _, e := f.org.Pin(f.contexts[f.other], f.other, r.ID, rooms.PinRequest{MessageID: msg.ID}); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("member pinned owner group", e)
	}
	if _, e := f.org.Pin(f.contexts[f.owner], f.owner, r.ID, rooms.PinRequest{MessageID: other.ID}); !errors.Is(e, rooms.ErrPinFailed) {
		t.Fatal("crossroom target", e)
	}
	at := time.Now().UTC().Add(-time.Minute)
	pin, e := f.org.Pin(f.contexts[f.owner], f.owner, r.ID, rooms.PinRequest{MessageID: msg.ID, ExpiresAt: &at})
	if e != nil {
		t.Fatal(e)
	}
	var eventExpiry int64
	var expiryType string
	if e = f.db.QueryRow(`SELECT (meta->>'expires_at')::bigint,jsonb_typeof(meta->'expires_at') FROM chat_messages WHERE chat_room_id=$1 AND type='messages.pinned' AND meta->>'pin_id'=$2`, r.ID, pin.ID).Scan(&eventExpiry, &expiryType); e != nil || eventExpiry != at.UnixMilli() || expiryType != "number" {
		t.Fatal("authored millisecond event shape changed", eventExpiry, expiryType, e)
	}
	if _, e = f.org.Pin(f.contexts[f.owner], f.owner, r.ID, rooms.PinRequest{MessageID: msg.ID}); !errors.Is(e, rooms.ErrPinFailed) {
		t.Fatal("expired duplicate", e)
	}
	pins, e := f.org.Pins(f.base, "", r.ID, false)
	if e != nil || len(pins) != 0 {
		t.Fatal(pins, e)
	}
	pins, e = f.org.Pins(f.base, "", r.ID, true)
	if e != nil || len(pins) != 1 || pins[0].ID != pin.ID || pins[0].PinnedBy == nil || pins[0].PinnedBy.LastReadAt != nil {
		t.Fatal(pins, e)
	}
	if e = f.org.Unpin(f.contexts[f.owner], f.owner, private.ID, pin.ID); !errors.Is(e, rooms.ErrUnpinFailed) {
		t.Fatal(e)
	}
	if e = f.org.Unpin(f.contexts[f.owner], f.owner, r.ID, pin.ID); e != nil {
		t.Fatal(e)
	}
	replacement, e := f.org.Pin(f.contexts[f.owner], f.owner, r.ID, rooms.PinRequest{MessageID: msg.ID})
	if e != nil || replacement.ID == pin.ID {
		t.Fatal(replacement, e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM chat_messages WHERE chat_room_id=$1 AND type IN('messages.pinned','messages.unpinned')`, r.ID); n != 3 {
		t.Fatal("pin event count", n)
	}
	if e = f.chats.DeleteMessage(f.contexts[f.owner], f.owner, r.ID, msg.ID); e != nil {
		t.Fatal(e)
	}
	pins, e = f.org.Pins(f.base, "", r.ID, true)
	if e != nil || len(pins) != 0 {
		t.Fatal("deleted target revealed", pins, e)
	}
	realm := relationRealm(t, f.chatRelationsFixture, false)
	linked := relationRoom(t, f.chatRelationsFixture, realm)
	rm := relationSend(t, f.chatRelationsFixture, f.other, linked.ID, "Realm pin")
	if _, e = f.org.Pin(f.contexts[f.third], f.third, linked.ID, rooms.PinRequest{MessageID: rm.ID}); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("normal realm member pinned", e)
	}
	if _, e = f.org.Pin(f.contexts[f.other], f.other, linked.ID, rooms.PinRequest{MessageID: rm.ID}); e != nil {
		t.Fatal("realm moderator denied", e)
	}
	if _, e = f.org.Pins(f.base, "", linked.ID, false); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("private realm public pins", e)
	}
}
func TestPGChatOrganizationGroupsOwnershipAssignmentAndDelete(t *testing.T) {
	f := newOrganizationFixture(t)
	r := threadRoom(t, f.chatRelationsFixture, false, f.other)
	a := orgGroup(t, f, f.owner, "Owner folder")
	b := orgGroup(t, f, f.other, "Other folder")
	if _, e := f.org.UpdateGroup(f.contexts[f.other], f.other, a.ID, rooms.GroupPatch{Name: relationText("Hijack")}); !errors.Is(e, rooms.ErrNotFound) {
		t.Fatal(e)
	}
	if e := f.org.DeleteGroup(f.contexts[f.other], f.other, a.ID); !errors.Is(e, rooms.ErrNotFound) {
		t.Fatal(e)
	}
	if e := f.org.MoveToGroup(f.contexts[f.other], f.other, r.ID, &a.ID); !errors.Is(e, rooms.ErrGroupMove) {
		t.Fatal(e)
	}
	if e := f.org.MoveToGroup(f.contexts[f.owner], f.owner, r.ID, &a.ID); e != nil {
		t.Fatal(e)
	}
	if e := f.org.MoveToGroup(f.contexts[f.other], f.other, r.ID, &b.ID); e != nil {
		t.Fatal(e)
	}
	for _, entry := range []struct{ actor, gid string }{{f.owner, a.ID}, {f.other, b.ID}} {
		v, e := f.org.GroupMember(f.contexts[entry.actor], entry.actor, r.ID)
		if e != nil || v.ChatGroupID == nil || *v.ChatGroupID != entry.gid || v.ChatGroup.AccountID != entry.actor {
			t.Fatal(v, e)
		}
	}
	// Both halves of the composite ownership boundary must also reject a
	// mismatched direct write, independently of the HTTP/repository checks.
	for _, statement := range []string{
		`UPDATE owned_chat_group_members SET group_id=$1 WHERE account_id=$2`,
		`UPDATE owned_chat_group_members SET group_id=$1,account_id=$3 WHERE account_id=$2`,
	} {
		var e error
		if statement == `UPDATE owned_chat_group_members SET group_id=$1 WHERE account_id=$2` {
			_, e = f.db.Exec(statement, b.ID, f.owner)
		} else {
			_, e = f.db.Exec(statement, b.ID, f.owner, f.other)
		}
		var pg *pgconn.PgError
		if !errors.As(e, &pg) || pg.Code != "23503" {
			t.Fatal("composite group ownership was not enforced", e)
		}
	}
	if e := f.org.DeleteGroup(f.contexts[f.owner], f.owner, a.ID); e != nil {
		t.Fatal(e)
	}
	mine, e := f.org.GroupMember(f.contexts[f.owner], f.owner, r.ID)
	if e != nil || mine.ChatGroupID != nil {
		t.Fatal(mine, e)
	}
	other, e := f.org.GroupMember(f.contexts[f.other], f.other, r.ID)
	if e != nil || other.ChatGroupID == nil || *other.ChatGroupID != b.ID {
		t.Fatal(other, e)
	}
	groups, e := f.org.Groups(f.contexts[f.owner], f.owner)
	if e != nil || len(groups) != 0 {
		t.Fatal(groups, e)
	}
	if _, e = f.chats.ChangeMember(f.contexts[f.other], f.other, r.ID, rooms.Leave, ""); e != nil {
		t.Fatal(e)
	}
	if e = f.org.MoveToGroup(f.contexts[f.other], f.other, r.ID, nil); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("departed member moved", e)
	}
}
func TestPGChatOrganizationAtomicRollbackAndDuplicateRace(t *testing.T) {
	f := newOrganizationFixture(t)
	r := threadRoom(t, f.chatRelationsFixture, false, f.other)
	m := relationSend(t, f.chatRelationsFixture, f.owner, r.ID, "Atomic pin")
	query(t, f.db, `CREATE FUNCTION synthetic_chat_pin_fail()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.chat_room_id::text=TG_ARGV[0] AND NEW.type='messages.pinned' THEN RAISE EXCEPTION 'synthetic atomic pin failure';END IF;RETURN NEW;END$$`)
	query(t, f.db, fmt.Sprintf(`CREATE TRIGGER synthetic_chat_pin_fail BEFORE INSERT ON chat_messages FOR EACH ROW EXECUTE FUNCTION synthetic_chat_pin_fail('%s')`, r.ID))
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DROP TRIGGER IF EXISTS synthetic_chat_pin_fail ON chat_messages;DROP FUNCTION IF EXISTS synthetic_chat_pin_fail()`)
	})
	if _, e := f.org.Pin(f.contexts[f.owner], f.owner, r.ID, rooms.PinRequest{MessageID: m.ID}); e == nil {
		t.Fatal("event failure committed")
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_chat_pins WHERE room_id=$1`, r.ID); n != 0 {
		t.Fatal("orphan pin", n)
	}
	if n := commerceCount(t, f.db, `SELECT owned_next_sequence FROM chat_rooms WHERE id=$1`, r.ID); n != int(m.RoomSequence) {
		t.Fatal("sequence changed on rollback", n)
	}
	query(t, f.db, `DROP TRIGGER synthetic_chat_pin_fail ON chat_messages;DROP FUNCTION synthetic_chat_pin_fail()`)
	var wg sync.WaitGroup
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := f.org.Pin(f.contexts[f.owner], f.owner, r.ID, rooms.PinRequest{MessageID: m.ID})
			done <- e
		}()
	}
	wg.Wait()
	close(done)
	good, bad := 0, 0
	for e := range done {
		if e == nil {
			good++
		} else if errors.Is(e, rooms.ErrPinFailed) {
			bad++
		} else {
			t.Fatal(e)
		}
	}
	if good != 1 || bad != 1 {
		t.Fatal(good, bad)
	}
	var livePin string
	if e := f.db.QueryRow(`SELECT id::text FROM owned_chat_pins WHERE room_id=$1 AND deleted_at IS NULL`, r.ID).Scan(&livePin); e != nil {
		t.Fatal(e)
	}
	sequence := commerceCount(t, f.db, `SELECT owned_next_sequence FROM chat_rooms WHERE id=$1`, r.ID)
	query(t, f.db, `CREATE FUNCTION synthetic_chat_unpin_fail()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.chat_room_id::text=TG_ARGV[0] AND NEW.type='messages.unpinned' THEN RAISE EXCEPTION 'synthetic atomic unpin failure';END IF;RETURN NEW;END$$`)
	query(t, f.db, fmt.Sprintf(`CREATE TRIGGER synthetic_chat_unpin_fail BEFORE INSERT ON chat_messages FOR EACH ROW EXECUTE FUNCTION synthetic_chat_unpin_fail('%s')`, r.ID))
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DROP TRIGGER IF EXISTS synthetic_chat_unpin_fail ON chat_messages;DROP FUNCTION IF EXISTS synthetic_chat_unpin_fail()`)
	})
	if e := f.org.Unpin(f.contexts[f.owner], f.owner, r.ID, livePin); e == nil {
		t.Fatal("failed unpin event committed tombstone")
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_chat_pins WHERE id=$1 AND deleted_at IS NULL`, livePin); n != 1 {
		t.Fatal("unpin failure lost live pin", n)
	}
	if n := commerceCount(t, f.db, `SELECT owned_next_sequence FROM chat_rooms WHERE id=$1`, r.ID); n != sequence {
		t.Fatal("unpin failure advanced sequence", n)
	}
	query(t, f.db, `DROP TRIGGER synthetic_chat_unpin_fail ON chat_messages;DROP FUNCTION synthetic_chat_unpin_fail()`)
	before := orgSummary(t, f, f.other)
	query(t, f.db, `CREATE FUNCTION synthetic_chat_receipt_fail()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.account_id::text=TG_ARGV[0] THEN RAISE EXCEPTION 'synthetic receipt failure';END IF;RETURN NEW;END$$`)
	query(t, f.db, fmt.Sprintf(`CREATE TRIGGER synthetic_chat_receipt_fail BEFORE INSERT ON owned_chat_read_receipts FOR EACH ROW EXECUTE FUNCTION synthetic_chat_receipt_fail('%s')`, f.other))
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DROP TRIGGER IF EXISTS synthetic_chat_receipt_fail ON owned_chat_read_receipts;DROP FUNCTION IF EXISTS synthetic_chat_receipt_fail()`)
	})
	if e := f.org.ReadAll(f.contexts[f.other], f.other); e == nil {
		t.Fatal("receipt failure committed")
	}
	after := orgSummary(t, f, f.other)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("partial read-all", before, after)
	}
}
func TestPGChatOrganizationWaitRevocationAndMembershipChange(t *testing.T) {
	for _, mode := range []string{"summary-session", "read-session", "pin-session", "group-session", "summary-member", "read-member"} {
		t.Run(mode, func(t *testing.T) {
			f := newOrganizationFixture(t)
			r := threadRoom(t, f.chatRelationsFixture, false, f.other)
			m := relationSend(t, f.chatRelationsFixture, f.owner, r.ID, "Wait authority")
			actor := f.other
			if mode == "pin-session" {
				actor = f.owner
			}
			ctx, cancel := context.WithTimeout(f.contexts[actor], 12*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			table, target := "owned_authority_accounts", actor
			if mode == "summary-member" || mode == "read-member" {
				table, target = "chat_rooms", r.ID
			}
			blocker, pid := atomicBlocker(t, locks, table, target)
			if table == "chat_rooms" {
				if _, e := blocker.ExecContext(ctx, `UPDATE chat_members SET leave_at=clock_timestamp() WHERE chat_room_id=$1 AND account_id=$2`, r.ID, actor); e != nil {
					t.Fatal(e)
				}
			} else {
				token := f.otherToken
				if actor == f.owner {
					token = f.token
				}
				proof, e := f.verifier.Authenticate(f.base, token)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = blocker.ExecContext(ctx, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp(),epoch=epoch+1 WHERE id=$1`, proof.Session.ID); e != nil {
					t.Fatal(e)
				}
			}
			done := make(chan error, 1)
			go func() {
				var e error
				switch mode {
				case "read-session", "read-member":
					e = f.org.ReadAll(ctx, actor)
				case "pin-session":
					_, e = f.org.Pin(ctx, actor, r.ID, rooms.PinRequest{MessageID: m.ID})
				case "group-session":
					_, e = f.org.CreateGroup(ctx, actor, rooms.GroupPatch{Name: relationText("Stale")})
				default:
					_, e = f.org.Summary(ctx, actor)
				}
				done <- e
			}()
			waitAtomicBlocked(t, locks, pid, table)
			if e := blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			e := <-done
			if table == "chat_rooms" {
				if !errors.Is(e, rooms.ErrConflict) {
					t.Fatal("stale membership snapshot", e)
				}
			} else if !errors.Is(e, authority.ErrRevoked) {
				t.Fatal("stale session", e)
			}
			if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_chat_pins WHERE room_id=$1`, r.ID); n != 0 {
				t.Fatal(n)
			}
			if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_chat_read_receipts WHERE account_id=$1`, actor); n != 0 {
				t.Fatal(n)
			}
		})
	}
	for _, mode := range []string{"pin-realm-role", "summary-realm-member", "read-realm-member", "search-realm-member"} {
		t.Run(mode, func(t *testing.T) {
			f := newOrganizationFixture(t)
			realm := relationRealm(t, f.chatRelationsFixture, false)
			r := relationRoom(t, f.chatRelationsFixture, realm)
			m := relationSend(t, f.chatRelationsFixture, f.other, r.ID, "Realm wait content")
			actor := f.third
			if mode == "pin-realm-role" {
				actor = f.other
			}
			ctx, cancel := context.WithTimeout(f.contexts[actor], 12*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			blocker, pid := atomicBlocker(t, locks, "owned_realms", realm.ID)
			var e error
			if mode == "pin-realm-role" {
				_, e = blocker.ExecContext(ctx, `UPDATE owned_realm_members SET role=$3 WHERE realm_id=$1 AND account_id=$2`, realm.ID, actor, social.Normal)
			} else {
				_, e = blocker.ExecContext(ctx, `UPDATE owned_realm_members SET leave_at=clock_timestamp() WHERE realm_id=$1 AND account_id=$2`, realm.ID, actor)
			}
			if e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() {
				var x error
				switch mode {
				case "pin-realm-role":
					_, x = f.org.Pin(ctx, actor, r.ID, rooms.PinRequest{MessageID: m.ID})
				case "read-realm-member":
					x = f.org.ReadAll(ctx, actor)
				case "search-realm-member":
					_, x = f.org.Search(ctx, actor, rooms.SearchRequest{Query: "Realm wait", Take: 20})
				default:
					_, x = f.org.Summary(ctx, actor)
				}
				done <- x
			}()
			waitAtomicBlocked(t, locks, pid, "owned_realms")
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			want := error(rooms.ErrConflict)
			if mode == "pin-realm-role" {
				want = rooms.ErrForbidden
			}
			if e = <-done; !errors.Is(e, want) {
				t.Fatal("stale realm authority admitted", e)
			}
			if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_chat_pins WHERE room_id=$1`, r.ID); n != 0 {
				t.Fatal("pin persisted after realm denial", n)
			}
			if n := commerceCount(t, f.db, `SELECT count(*) FROM chat_messages WHERE chat_room_id=$1 AND type='messages.pinned'`, r.ID); n != 0 {
				t.Fatal("pin event persisted after realm denial", n)
			}
			if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_chat_read_receipts WHERE account_id=$1`, actor); n != 0 {
				t.Fatal("receipt persisted after realm denial", n)
			}
		})
	}

	for _, mode := range []string{"pin-expiry-wait", "deleted-pin-target-wait", "public-realm-privacy-wait"} {
		t.Run(mode, func(t *testing.T) {
			f := newOrganizationFixture(t)
			r := threadRoom(t, f.chatRelationsFixture, true, f.other)
			var realm social.Realm
			if mode == "public-realm-privacy-wait" {
				realm = relationRealm(t, f.chatRelationsFixture, true)
				r = relationRoom(t, f.chatRelationsFixture, realm)
			}
			m := relationSend(t, f.chatRelationsFixture, f.owner, r.ID, "Pin wait content")
			q := rooms.PinRequest{MessageID: m.ID}
			if mode == "pin-expiry-wait" {
				var at time.Time
				if e := f.db.QueryRow(`SELECT clock_timestamp()+interval '2 seconds'`).Scan(&at); e != nil {
					t.Fatal(e)
				}
				q.ExpiresAt = &at
			}
			if _, e := f.org.Pin(f.contexts[f.owner], f.owner, r.ID, q); e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(f.base, 12*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			blocker, pid := atomicBlocker(t, locks, "chat_rooms", r.ID)
			if mode == "deleted-pin-target-wait" {
				if _, e := blocker.ExecContext(ctx, `UPDATE chat_messages SET deleted_at=clock_timestamp() WHERE id=$1`, m.ID); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "public-realm-privacy-wait" {
				if _, e := blocker.ExecContext(ctx, `UPDATE owned_realms SET is_public=false WHERE id=$1`, realm.ID); e != nil {
					t.Fatal(e)
				}
			}
			type result struct {
				pins []rooms.Pin
				err  error
			}
			done := make(chan result, 1)
			go func() {
				pins, e := f.org.Pins(ctx, "", r.ID, false)
				done <- result{pins, e}
			}()
			waitAtomicBlocked(t, locks, pid, "chat_rooms")
			if q.ExpiresAt != nil {
				organizationWaitExpired(t, ctx, f.db, *q.ExpiresAt)
			}
			if e := blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			v := <-done
			if mode == "public-realm-privacy-wait" {
				if !errors.Is(v.err, rooms.ErrForbidden) || len(v.pins) != 0 {
					t.Fatal("private realm pins revealed after wait", v)
				}
			} else if v.err != nil || len(v.pins) != 0 {
				t.Fatal("stale pin target revealed after wait", v)
			}
		})
	}

}
func TestPGChatOrganizationCanonicalSearchEntitlement(t *testing.T) {
	f := newCommerceFixture(t)
	query(t, f.db, chatpg.OrganizationSchema)
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	chat, e := chatpg.NewWithAuthority(f.db, chatPolicy, f.guard)
	if e != nil {
		t.Fatal(e)
	}
	chat.WithSocial(socialpg.NewWithAuthority(f.db, f.guard))
	reader, e := messagerpg.NewWithIndependentPolicy(f.db, messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30})
	if e != nil {
		t.Fatal(e)
	}
	reader.WithRoomAccess(chat)
	perk, e := f.r.ChatSearchPerks(map[string]int{f.plan: 1})
	if e != nil {
		t.Fatal(e)
	}
	org := chatpg.NewOrganization(chat, reader, perk)
	room, e := chat.Create(f.ctx, f.actor, rooms.RoomPatch{Name: relationText("Paid search")})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = chat.Send(f.ctx, f.actor, room.ID, rooms.TextRequest{Content: "Paid searchable text"}); e != nil {
		t.Fatal(e)
	}
	q := rooms.SearchRequest{Query: "searchable", Take: 20}
	if _, e = org.Search(f.ctx, f.actor, q); !errors.Is(e, rooms.ErrSearchEntitlement) {
		t.Fatal("unpaid account admitted", e)
	}
	sub, e := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: f.plan}, "chat-search", false)
	if e != nil {
		t.Fatal(e)
	}
	order, e := f.r.SubscriptionOrder(f.ctx, f.actor, sub.Identifier)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = org.Search(f.ctx, f.actor, q); !errors.Is(e, rooms.ErrSearchEntitlement) {
		t.Fatal("unpaid order admitted", e)
	}
	if _, e = f.r.PayOrder(f.ctx, f.actor, order.ID, &f.wallet.ID); e != nil {
		t.Fatal(e)
	}
	page, e := org.Search(f.ctx, f.actor, q)
	if e != nil || page.Total != 1 {
		t.Fatal(page, e)
	}
	query(t, f.db, `UPDATE commerce_v1.definitions SET enabled=false WHERE identifier=$1`, f.plan)
	if _, e = org.Search(f.ctx, f.actor, q); !errors.Is(e, rooms.ErrSearchEntitlement) {
		t.Fatal("disabled definition admitted", e)
	}
	query(t, f.db, `UPDATE commerce_v1.definitions SET enabled=true WHERE identifier=$1`, f.plan)
	t.Run("final-queued-cycle-expiry-after-definition-wait", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(f.ctx, 12*time.Second)
		defer cancel()
		plan := "synthetic-next-" + authorityUUID(t)
		query(t, f.db, `INSERT INTO commerce_v1.definitions(identifier,document,payee_wallet_id) SELECT $1::text,jsonb_set(jsonb_set(document,'{identifier}',to_jsonb($1::text)),'{group_identifier}',to_jsonb($1::text)),payee_wallet_id FROM commerce_v1.definitions WHERE identifier=$2::text`, plan, f.plan)
		next, e := f.r.CreateSubscription(ctx, f.actor, c.CreateSubscription{Identifier: plan}, "chat-search-next", false)
		if e != nil {
			t.Fatal(e)
		}
		order, e := f.r.SubscriptionOrder(ctx, f.actor, plan)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.r.PayOrder(ctx, f.actor, order.ID, &f.wallet.ID); e != nil {
			t.Fatal(e)
		}
		perk, e := f.r.ChatSearchPerks(map[string]int{f.plan: 1, plan: 1})
		if e != nil {
			t.Fatal(e)
		}
		blocker, e := f.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if e != nil {
			t.Fatal(e)
		}
		defer blocker.Rollback()
		var pid int
		if e = blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
			t.Fatal(e)
		}
		if _, e = blocker.ExecContext(ctx, `SELECT identifier FROM commerce_v1.definitions WHERE identifier=$1 FOR UPDATE`, plan); e != nil {
			t.Fatal(e)
		}
		var begun, ended time.Time
		if e = f.db.QueryRowContext(ctx, `SELECT at,at+interval '2 seconds' FROM(SELECT clock_timestamp()+interval '2 seconds' AS at) deadline`).Scan(&begun, &ended); e != nil {
			t.Fatal(e)
		}
		// Both cycles were genuinely paid through the canonical fixture. Only
		// their coherent synthetic schedule is compressed for the elapsed-time case.
		query(t, f.db, `UPDATE commerce_v1.subscriptions SET ended_at=$2::timestamptz,document=jsonb_set(document,'{ended_at}',to_jsonb($2::timestamptz)) WHERE id=$1::uuid`, sub.ID, begun)
		query(t, f.db, `UPDATE commerce_v1.subscriptions SET begun_at=$2::timestamptz,ended_at=$3::timestamptz,document=jsonb_set(jsonb_set(document,'{begun_at}',to_jsonb($2::timestamptz)),'{ended_at}',to_jsonb($3::timestamptz)) WHERE id=$1::uuid`, next.ID, begun, ended)
		entered, release := make(chan struct{}), make(chan struct{})
		guarded := chatpg.NewOrganization(chat, organizationReadGate{reader: reader, entered: entered, release: release}, perk)
		type result struct {
			page rooms.SearchPage
			err  error
		}
		done := make(chan result, 1)
		go func() {
			page, e := guarded.Search(ctx, f.actor, q)
			done <- result{page, e}
		}()
		select {
		case <-ctx.Done():
			t.Fatal("first eligible cycle did not reach hydration", ctx.Err())
		case <-entered:
		}
		organizationWaitExpired(t, ctx, f.db, begun)
		close(release)
		waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: ctx}, pid, "commerce_v1.definitions")
		organizationWaitExpired(t, ctx, f.db, ended)
		if e = blocker.Commit(); e != nil {
			t.Fatal(e)
		}
		v := <-done
		if !errors.Is(v.err, rooms.ErrSearchEntitlement) || v.page.Total != 0 || len(v.page.Rooms) != 0 {
			t.Fatal("final entitlement pass reused time from before its lock wait", v)
		}
	})
	query(t, f.db, `WITH deadline AS(SELECT clock_timestamp()+interval '1 day' AS at) UPDATE commerce_v1.subscriptions s SET ended_at=deadline.at,document=jsonb_set(s.document,'{ended_at}',to_jsonb(deadline.at)) FROM deadline WHERE s.id=$1::uuid`, sub.ID)
	t.Run("expiry-after-definition-wait", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(f.ctx, 12*time.Second)
		defer cancel()
		var expiredAt time.Time
		// Keep the canonical scalar and document projection coherent. Eligibility
		// expires on the real database clock while Search is demonstrably waiting.
		if e := f.db.QueryRowContext(ctx, `WITH deadline AS(SELECT clock_timestamp()+interval '2 seconds' AS at) UPDATE commerce_v1.subscriptions s SET ended_at=deadline.at,document=jsonb_set(s.document,'{ended_at}',to_jsonb(deadline.at)) FROM deadline WHERE s.id=$1::uuid RETURNING s.ended_at`, sub.ID).Scan(&expiredAt); e != nil {
			t.Fatal(e)
		}
		blocker, e := f.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if e != nil {
			t.Fatal(e)
		}
		defer blocker.Rollback()
		var pid int
		if e = blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
			t.Fatal(e)
		}
		if _, e = blocker.ExecContext(ctx, `SELECT identifier FROM commerce_v1.definitions WHERE identifier=$1 FOR UPDATE`, f.plan); e != nil {
			t.Fatal(e)
		}
		done := make(chan error, 1)
		go func() {
			_, e := org.Search(ctx, f.actor, q)
			done <- e
		}()
		locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
		waitAtomicBlocked(t, locks, pid, "commerce_v1.definitions")
		organizationWaitExpired(t, ctx, f.db, expiredAt)
		if e = blocker.Commit(); e != nil {
			t.Fatal(e)
		}
		if e = <-done; !errors.Is(e, rooms.ErrSearchEntitlement) {
			t.Fatal("expired paid search survived lock wait", e)
		}
	})
	// Restore coherent active time for the independent corruption rejection.
	query(t, f.db, `WITH deadline AS(SELECT clock_timestamp()+interval '1 day' AS at) UPDATE commerce_v1.subscriptions s SET ended_at=deadline.at,document=jsonb_set(s.document,'{ended_at}',to_jsonb(deadline.at)) FROM deadline WHERE s.id=$1::uuid`, sub.ID)
	query(t, f.db, `UPDATE commerce_v1.subscriptions SET document=jsonb_set(document,'{account_id}',to_jsonb($2::text)) WHERE id=$1`, sub.ID, authorityUUID(t))
	if _, e = org.Search(f.ctx, f.actor, q); !errors.Is(e, rooms.ErrConflict) {
		t.Fatal("corrupt grant admitted", e)
	}
}
func TestPGBackendChatOrganizationRoutes(t *testing.T) {
	f := newBackendFixture(t)
	room := backendDecode[rooms.Room](t, backendCall(t, f.app, "POST", "/messager/chat", f.token, `{"name":"Organized room"}`, 200))
	msg := backendDecode[map[string]any](t, backendCall(t, f.app, "POST", "/api/chat/"+room.ID+"/messages", f.token, `{"content":"Pin me"}`, 200))
	group := backendDecode[rooms.ChatGroup](t, backendCall(t, f.app, "POST", "/messager/chat/groups", f.token, `{"name":"Work"}`, 200))
	backendJSONCall(t, f.app, "PATCH", "/api/chat/rooms/"+room.ID+"/group", f.token, map[string]any{"group_id": group.ID}, 200)
	member := backendDecode[rooms.GroupMember](t, backendCall(t, f.app, "GET", "/api/chat/"+room.ID+"/members/me", f.token, "", 200))
	if member.ChatGroupID == nil || *member.ChatGroupID != group.ID {
		t.Fatal(member)
	}
	pin := backendDecode[rooms.Pin](t, backendJSONCall(t, f.app, "POST", "/api/chat/"+room.ID+"/pins", f.token, map[string]any{"message_id": msg["id"]}, 200))
	backendCall(t, f.app, "GET", "/messager/chat/summary", f.token, "", 200)
	backendCall(t, f.app, "POST", "/messager/chat/read-all", f.token, "", 200)
	backendCall(t, f.app, "GET", "/api/chat/messages/search?query=Pin", f.token, "", 503)
	backendCall(t, f.app, "DELETE", "/messager/chat/"+room.ID+"/pins/"+pin.ID, f.token, "", 200)
	backendCall(t, f.app, "DELETE", "/api/chat/groups/"+group.ID, f.token, "", 200)
	backendCall(t, f.app, "GET", "/api/chat/groups", f.otherToken, "", 200)
	backendCall(t, f.app, "GET", "/api/chat/summary", "", "", 401)
}
func organizationRestartAuthority(t *testing.T, f organizationFixture) (organizationFixture, authority.Config, string) {
	t.Helper()
	clock := &atomic.Int64{}
	clock.Store(time.Now().UTC().Unix())
	config := authorityConfig(t, authorityKeys(t), clock)
	config.AccessTTL = time.Hour
	config.SessionTTL = time.Hour
	config.Now = time.Now
	issuer, e := authority.New(authoritypg.New(f.db), config)
	if e != nil {
		t.Fatal(e)
	}
	token := pgIssue(t, issuer, f.owner).Token
	verifier, e := authority.NewVerifier(authoritypg.New(f.db), config)
	if e != nil {
		t.Fatal(e)
	}
	proof, e := verifier.Authenticate(f.base, token)
	if e != nil {
		t.Fatal(e)
	}
	f.verifier = verifier
	f.guard = authoritypg.NewTransactionGuard(authoritypg.New(f.db), verifier)
	f.contexts[f.owner] = f.guard.Bind(f.base, proof)
	f.chats, e = chatpg.NewWithAuthority(f.db, chatPolicy, f.guard)
	if e != nil {
		t.Fatal(e)
	}
	f.chats.WithSocial(socialpg.NewWithAuthority(f.db, f.guard))
	f.read.WithRoomAccess(f.chats)
	f.org = chatpg.NewOrganization(f.chats, f.read, syntheticOrganizationPerk{})
	return f, config, token
}
func TestPGChatOrganizationRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	type record struct {
		Actor, Room, Message, Pin, Group, Token string
		Authority                               restartAuthoritySnapshot
		Summary                                 map[string]rooms.ChatSummary
		Member                                  rooms.GroupMember
	}
	var saved record
	if phase == "before" {
		f, config, token := organizationRestartAuthority(t, newOrganizationFixture(t))
		// Original one-hour proof exists before any restart target room, message,
		// group, pin or read receipt. Only public verification keys are persisted.
		room := threadRoom(t, f.chatRelationsFixture, false)
		m := relationSend(t, f.chatRelationsFixture, f.owner, room.ID, "Persisted organization")
		g := orgGroup(t, f, f.owner, "Saved group")
		if e := f.org.MoveToGroup(f.contexts[f.owner], f.owner, room.ID, &g.ID); e != nil {
			t.Fatal(e)
		}
		p, e := f.org.Pin(f.contexts[f.owner], f.owner, room.ID, rooms.PinRequest{MessageID: m.ID})
		if e != nil {
			t.Fatal(e)
		}
		if e = f.org.ReadAll(f.contexts[f.owner], f.owner); e != nil {
			t.Fatal(e)
		}
		member, e := f.org.GroupMember(f.contexts[f.owner], f.owner, room.ID)
		if e != nil {
			t.Fatal(e)
		}
		saved = record{Actor: f.owner, Room: room.ID, Message: m.ID, Pin: p.ID, Group: g.ID, Token: token, Authority: saveAuthoritySnapshot(t, config), Summary: orgSummary(t, f, f.owner), Member: member}
		saveRestartFixture(t, "chat-organization.json", saved)
		return
	}
	loadRestartFixture(t, "chat-organization.json", &saved)
	config := restoreAuthoritySnapshot(t, saved.Authority)
	store := authoritypg.New(db)
	verifier, e := authority.NewVerifier(store, config)
	if e != nil {
		t.Fatal(e)
	}
	base, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	proof, e := verifier.Authenticate(base, saved.Token)
	if e != nil {
		t.Fatal("original pre-restart proof no longer valid", e)
	}
	guard := authoritypg.NewTransactionGuard(store, verifier)
	ctx := guard.Bind(base, proof)
	chat, e := chatpg.NewWithAuthority(db, chatPolicy, guard)
	if e != nil {
		t.Fatal(e)
	}
	chat.WithSocial(socialpg.NewWithAuthority(db, guard))
	reader, e := messagerpg.NewWithIndependentPolicy(db, messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30})
	if e != nil {
		t.Fatal(e)
	}
	reader.WithRoomAccess(chat)
	org := chatpg.NewOrganization(chat, reader, syntheticOrganizationPerk{})
	summary, e := org.Summary(ctx, saved.Actor)
	if e != nil {
		t.Fatal(e)
	}
	a, _ := json.Marshal(summary)
	b, _ := json.Marshal(saved.Summary)
	if string(a) != string(b) {
		t.Fatal("summary restart differs", string(a), string(b))
	}
	member, e := org.GroupMember(ctx, saved.Actor, saved.Room)
	if e != nil || member.ChatGroupID == nil || *member.ChatGroupID != saved.Group || member.LastReadAt == nil || saved.Member.LastReadAt == nil || !member.LastReadAt.Equal(*saved.Member.LastReadAt) {
		t.Fatal("group/read persistence", member, e)
	}
	pins, e := org.Pins(ctx, saved.Actor, saved.Room, false)
	if e != nil || len(pins) != 1 || pins[0].ID != saved.Pin || pins[0].MessageID != saved.Message {
		t.Fatal(pins, e)
	}
	if n := commerceCount(t, db, `SELECT count(*) FROM owned_chat_read_receipts WHERE account_id=$1 AND room_id=$2`, saved.Actor, saved.Room); n != 1 {
		t.Fatal(n)
	}
	// The saved proof's lifetime remains enforced; never refresh or reissue it.
	expired := config
	expired.Now = func() time.Time { return saved.Authority.Now.Add(2 * time.Hour) }
	expiredVerifier, e := authority.NewVerifier(store, expired)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = expiredVerifier.Authenticate(base, saved.Token); e == nil {
		t.Fatal("original proof expiry disabled after restart")
	}
}

// Observe expiration on the database clock; no frozen or advanced fixture clock
// can make a lock-wait regression pass without real elapsed time.
func organizationWaitExpired(t *testing.T, ctx context.Context, db *sql.DB, at time.Time) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var expired bool
		if e := db.QueryRowContext(ctx, `SELECT clock_timestamp()>$1::timestamptz`, at).Scan(&expired); e != nil {
			t.Fatal(e)
		}
		if expired {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
}
