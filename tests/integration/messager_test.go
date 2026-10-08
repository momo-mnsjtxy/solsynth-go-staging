// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/ports/messager"
)

// All identities, modes, visibility decisions, JSON contents and timestamps are
// synthetic. Schema is internal/messagerpg/testdata/schema.sql, installed by the
// guarded parent CI. No production DSN is accepted by database(t).
const messagerNoneMode int32 = 77
const messagerAccount = "bc000000-0000-0000-0000-000000000001"
const messagerOther = "bc000000-0000-0000-0000-000000000002"

func messagerID(group, n int) string { return fmt.Sprintf("bd%06x-0000-0000-0000-%012x", group, n) }
func messagerFixture(t *testing.T, db *sql.DB, group int, public bool, mode int32) (string, string) {
	t.Helper()
	room, sender := messagerID(group, 1), messagerID(group, 2)
	query(t, db, `INSERT INTO chat_rooms(id,is_public,encryption_mode) VALUES($1,$2,$3)`, room, public, mode)
	query(t, db, `INSERT INTO chat_members(id,chat_room_id,account_id,joined_at) VALUES($1,$2,$3,'2026-01-01T00:00:00Z')`, sender, room, messagerAccount)
	return room, sender
}
func messagerInsert(t *testing.T, db *sql.DB, group, n int, room, sender string, sequence int64, created time.Time, typ string) {
	t.Helper()
	query(t, db, `INSERT INTO chat_messages(id,chat_room_id,sender_id,type,room_sequence,content,created_at,updated_at) VALUES($1,$2,$3,$4,$5,'# title',$6,$6)`, messagerID(group, n), room, sender, typ, sequence, created)
}
func messagerHandler(t *testing.T, db *sql.DB, account string) http.Handler {
	t.Helper()
	h, err := messager.NewSyntheticProjectionSyncHandler(messager.NewSyntheticProjectionSyncService(messagerpg.NewSyntheticProjectionRepository(db, messagerNoneMode)), account)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func messagerRequest(h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, target, strings.NewReader(body)))
	return w
}
func messagerSync(t *testing.T, h http.Handler, room, body, total string) messager.SyncResponse {
	t.Helper()
	w := messagerRequest(h, "POST", "/api/chat/"+room+"/sync", body)
	if w.Code != 200 || w.Header().Get("X-Total") != total || w.Header().Get("X-Solsynth-Projection") != messager.SyncProjectionVersion {
		t.Fatalf("sync %d total=%q body=%s", w.Code, w.Header().Get("X-Total"), w.Body.String())
	}
	var response messager.SyncResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Messages == nil {
		t.Fatal("null messages")
	}
	return response
}
func messagerSequences(response messager.SyncResponse) []int64 {
	out := make([]int64, len(response.Messages))
	for i, m := range response.Messages {
		out[i] = m.RoomSequence
	}
	return out
}
func messagerBase() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

func TestPGMessagerTimestampSequenceAndRangeRecovery(t *testing.T) {
	db := database(t)
	room, sender := messagerFixture(t, db, 1, true, messagerNoneMode)
	otherRoom, otherSender := messagerFixture(t, db, 2, true, messagerNoneMode)
	base := messagerBase()
	// Sequences deliberately disagree with chronological order. The returned
	// cursor is the final sequence-ordered row, not max(created_at).
	messagerInsert(t, db, 1, 10, room, sender, 1, base.Add(3*time.Hour), "text")
	messagerInsert(t, db, 1, 11, room, sender, 2, base, "text")
	messagerInsert(t, db, 1, 12, room, sender, 3, base.Add(time.Hour), "messages.update")
	messagerInsert(t, db, 1, 13, room, sender, 4, base.Add(2*time.Hour), "messages.delete")
	messagerInsert(t, db, 1, 14, room, sender, 5, base.Add(time.Hour), "text")
	query(t, db, `UPDATE chat_messages SET thread_id=$1 WHERE id=$2`, messagerID(1, 10), messagerID(1, 14))
	messagerInsert(t, db, 1, 15, room, sender, 6, base.Add(time.Hour), "text")
	query(t, db, `UPDATE chat_messages SET deleted_at=now() WHERE id=$1`, messagerID(1, 15))
	messagerInsert(t, db, 2, 10, otherRoom, otherSender, 3, base.Add(time.Hour), "text")
	h := messagerHandler(t, db, messagerOther) // Authenticated public nonmember is source-readable.
	response := messagerSync(t, h, room, fmt.Sprintf(`{"last_sync_timestamp":%d}`, base.UnixMilli()), "3")
	if got := messagerSequences(response); !reflect.DeepEqual(got, []int64{1, 3, 4}) || !response.CurrentTimestamp.Equal(base.Add(2*time.Hour)) {
		t.Fatal(got, response.CurrentTimestamp)
	}
	for _, body := range []string{`{"last_sync_timestamp":9223372036854775807,"missing_sequences":[4,2,2,0,-1]}`, `{"last_sync_timestamp":9223372036854775807,"missing_sequences":[4],"missing_sequence_ranges":[{"start_sequence":2,"end_sequence":2}]}`} {
		got := messagerSync(t, h, room, body, "2")
		if !reflect.DeepEqual(messagerSequences(got), []int64{2, 4}) {
			t.Fatal(got)
		}
	}
	response = messagerSync(t, h, room, `{"last_sync_timestamp":9223372036854775807,"missing_sequence_ranges":[{"start_sequence":4,"end_sequence":2},{"start_sequence":3,"end_sequence":6},{"start_sequence":0,"end_sequence":1}]}`, "3")
	if !reflect.DeepEqual(messagerSequences(response), []int64{2, 3, 4}) {
		t.Fatal(response)
	}
	response = messagerSync(t, h, room, fmt.Sprintf(`{"last_sync_timestamp":%d,"missing_sequences":[0,-5],"missing_sequence_ranges":[{"start_sequence":-5,"end_sequence":1}]}`, base.UnixMilli()), "3")
	if !reflect.DeepEqual(messagerSequences(response), []int64{1, 3, 4}) {
		t.Fatal(response)
	}
	before := time.Now().Add(-time.Second)
	empty := messagerSync(t, h, room, `{"last_sync_timestamp":253402300799999}`, "0")
	if len(empty.Messages) != 0 || empty.CurrentTimestamp.Before(before) || empty.CurrentTimestamp.After(time.Now().Add(time.Second)) {
		t.Fatal(empty)
	}
}

func TestPGMessagerMembershipAndVisibility(t *testing.T) {
	db := database(t)
	base := messagerBase()
	private, member := messagerFixture(t, db, 3, false, messagerNoneMode)
	encrypted, encryptedMember := messagerFixture(t, db, 4, true, 88)
	public, sender := messagerFixture(t, db, 5, true, messagerNoneMode)
	for _, x := range []struct {
		group        int
		room, sender string
	}{{3, private, member}, {4, encrypted, encryptedMember}, {5, public, sender}} {
		messagerInsert(t, db, x.group, 10, x.room, x.sender, 1, base, "text")
	}
	body := `{"last_sync_timestamp":0}`
	for _, room := range []string{private, encrypted} {
		if w := messagerRequest(messagerHandler(t, db, messagerOther), "POST", "/api/chat/"+room+"/sync", body); w.Code != 403 {
			t.Fatal(w)
		}
		if w := messagerRequest(messagerHandler(t, db, ""), "GET", "/api/chat/"+room+"/messages", ""); w.Code != 401 {
			t.Fatal(w)
		}
		messagerSync(t, messagerHandler(t, db, messagerAccount), room, body, "1")
	}
	// Leaving/invited/deleted membership is never treated as a grant.
	query(t, db, `INSERT INTO chat_members(id,chat_room_id,account_id) VALUES($1,$2,$3)`, messagerID(3, 3), private, messagerOther)
	for _, state := range []string{"joined_at=NULL,leave_at=NULL,deleted_at=NULL", "joined_at=now(),leave_at=now(),deleted_at=NULL", "joined_at=now(),leave_at=NULL,deleted_at=now()"} {
		query(t, db, `UPDATE chat_members SET `+state+` WHERE id=$1`, messagerID(3, 3))
		if w := messagerRequest(messagerHandler(t, db, messagerOther), "POST", "/api/chat/"+private+"/sync", body); w.Code != 403 {
			t.Fatal(state, w)
		}
	}
	query(t, db, `UPDATE chat_members SET joined_at=now(),leave_at=NULL,deleted_at=NULL WHERE id=$1`, messagerID(3, 3))
	messagerSync(t, messagerHandler(t, db, messagerOther), private, body, "1")
	if w := messagerRequest(messagerHandler(t, db, ""), "GET", "/api/chat/"+public+"/messages", ""); w.Code != 200 {
		t.Fatal(w)
	}
	query(t, db, `UPDATE chat_members SET deleted_at=now() WHERE id=$1`, sender)
	messagerSync(t, messagerHandler(t, db, messagerOther), public, body, "0")
	query(t, db, `UPDATE chat_rooms SET deleted_at=now() WHERE id=$1`, public)
	if w := messagerRequest(messagerHandler(t, db, messagerOther), "POST", "/api/chat/"+public+"/sync", body); w.Code != 404 {
		t.Fatal(w)
	}
	for _, h := range []http.Handler{messager.NewSyncHandler(messager.NewSyntheticProjectionSyncService(messagerpg.NewSyntheticProjectionRepository(db, messagerNoneMode))), func() http.Handler {
		h, _ := messager.NewSyntheticProjectionSyncHandler(messager.NewSyntheticProjectionSyncService(messagerpg.New(db)), messagerAccount)
		return h
	}()} {
		w := messagerRequest(h, "POST", "/api/chat/"+private+"/sync", body)
		if w.Code != 503 || w.Header().Get("X-Total") != "" {
			t.Fatal(w)
		}
	}
}

func TestPGMessagerHydrationAndMessageReads(t *testing.T) {
	db := database(t)
	room, sender := messagerFixture(t, db, 6, true, messagerNoneMode)
	base := messagerBase()
	otherMember := messagerID(6, 3)
	query(t, db, `INSERT INTO chat_members(id,chat_room_id,account_id,joined_at) VALUES($1,$2,$3,now())`, otherMember, room, messagerOther)
	for n := 10; n < 16; n++ {
		typ := "text"
		if n == 14 {
			typ = "messages.update"
		}
		if n == 15 {
			typ = "messages.delete"
		}
		messagerInsert(t, db, 6, n, room, sender, int64(n-9), base.Add(time.Duration(n)*time.Minute), typ)
	}
	query(t, db, `UPDATE chat_messages SET meta=$1::jsonb,attachments=$2::jsonb,reactions_count=$3::jsonb,client_message_id='client-original' WHERE id=$4`, `{"number":9007199254740993}`, `[{"id":"synthetic-file","custom":{"large":9007199254740993}}]`, `{"heart":9}`, messagerID(6, 10))
	query(t, db, `UPDATE chat_messages SET encryption_meta=$1::jsonb WHERE id=$2`, `{"ciphertext":"opaque","header":"opaque","signature":"opaque","scheme":"synthetic","epoch":9007199254740993,"future":{"x":true}}`, messagerID(6, 11))
	query(t, db, `UPDATE chat_messages SET thread_id=$1 WHERE id IN($2,$3)`, messagerID(6, 10), messagerID(6, 12), messagerID(6, 13))
	query(t, db, `UPDATE chat_messages SET deleted_at=now() WHERE id=$1`, messagerID(6, 13))
	query(t, db, `UPDATE chat_messages SET meta=$1::jsonb WHERE id IN($2,$3)`, `{"message_id":"`+messagerID(6, 10)+`"}`, messagerID(6, 14), messagerID(6, 15))
	for n := 20; n < 23; n++ {
		accountMember := otherMember
		if n == 21 {
			accountMember = sender
		}
		query(t, db, `INSERT INTO chat_reactions(id,message_id,sender_id,symbol,deleted_at) VALUES($1,$2,$3,$4,CASE WHEN $5 THEN now() ELSE NULL END)`, messagerID(6, n), messagerID(6, 10), accountMember, []string{"heart", "other", "deleted"}[n-20], n == 22)
	}
	h := messagerHandler(t, db, messagerOther)
	response := messagerSync(t, h, room, `{"last_sync_timestamp":0}`, "4")
	a, b := response.Messages[0], response.Messages[1]
	if *a.Content != "\\# title" || *b.Content != "# title" || a.ThreadRepliesCount != 1 || !a.IsThreadRoot || !a.ReactionsMade["heart"] || len(a.ReactionsMade) != 1 || b.ReactionsMade == nil || len(b.ReactionsMade) != 0 {
		t.Fatal(response)
	}
	if !strings.Contains(string(a.Meta), "9007199254740993") || !strings.Contains(string(a.Attachments), "9007199254740993") || !strings.Contains(string(b.EncryptionMeta), "9007199254740993") {
		t.Fatal(response)
	}
	if response.Messages[2].Type != "messages.update" || response.Messages[3].Type != "messages.delete" || response.Messages[2].ClientMessageID != nil {
		t.Fatal(response)
	}
	w := messagerRequest(h, "GET", "/api/chat/"+room+"/messages?take=2&offset=1", "")
	var page []messager.MessageProjection
	if w.Code != 200 || w.Header().Get("X-Total") != "4" || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page) != 2 || page[0].RoomSequence != 5 || page[1].RoomSequence != 2 {
		t.Fatal(w, page)
	}
	w = messagerRequest(h, "GET", "/api/chat/"+room+"/messages/"+messagerID(6, 12), "")
	var message messager.MessageProjection
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &message) != nil || message.ThreadID == nil || *message.ThreadID != messagerID(6, 10) {
		t.Fatal(w)
	}
	w = messagerRequest(h, "GET", "/api/chat/"+room+"/messages/"+messagerID(6, 13), "")
	if w.Code != 404 {
		t.Fatal(w)
	}
	anon := messagerRequest(messagerHandler(t, db, ""), "GET", "/api/chat/"+room+"/messages/"+messagerID(6, 10), "")
	var raw map[string]json.RawMessage
	if anon.Code != 200 || json.Unmarshal(anon.Body.Bytes(), &raw) != nil || string(raw["reactions_made"]) != "null" || raw["sender"] != nil || raw["account"] != nil {
		t.Fatal(anon)
	}
}

func TestPGMessagerLimitAndExactInt64(t *testing.T) {
	db := database(t)
	room, sender := messagerFixture(t, db, 7, true, messagerNoneMode)
	query(t, db, `INSERT INTO chat_messages(id,chat_room_id,sender_id,type,room_sequence,content,created_at,updated_at)
 SELECT ('be000007-0000-0000-0000-'||lpad(n::text,12,'0'))::uuid,$1,$2,'text',n,'row','2026-01-01T00:00:00Z'::timestamptz+make_interval(secs=>n),'2026-01-01T00:00:00Z'::timestamptz FROM generate_series(1,1005) n`, room, sender)
	messagerInsert(t, db, 7, 10, room, sender, math.MaxInt64, messagerBase(), "text")
	h := messagerHandler(t, db, messagerOther)
	response := messagerSync(t, h, room, `{"last_sync_timestamp":0}`, "1006")
	if len(response.Messages) != 1000 || response.Messages[0].RoomSequence != 1 || response.Messages[999].RoomSequence != 1000 {
		t.Fatal(len(response.Messages))
	}
	response = messagerSync(t, h, room, `{"last_sync_timestamp":0,"missing_sequences":[9223372036854775807]}`, "1")
	if len(response.Messages) != 1 || response.Messages[0].RoomSequence != math.MaxInt64 {
		t.Fatal(response)
	}
	response = messagerSync(t, h, room, `{"last_sync_timestamp":0,"missing_sequence_ranges":[{"start_sequence":9223372036854775807,"end_sequence":9223372036854775807}]}`, "1")
	if response.Messages[0].RoomSequence != math.MaxInt64 {
		t.Fatal(response)
	}
}

func TestPGMessagerReadOnlyFailuresAndIndependentPool(t *testing.T) {
	db := database(t)
	room, sender := messagerFixture(t, db, 8, true, messagerNoneMode)
	messagerInsert(t, db, 8, 10, room, sender, 1, messagerBase(), "text")
	readonly := database(t)
	readonly.SetMaxOpenConns(1)
	readonly.SetMaxIdleConns(1)
	query(t, readonly, `SET default_transaction_read_only=on`)
	response := messagerSync(t, messagerHandler(t, readonly, messagerOther), room, `{"last_sync_timestamp":0}`, "1")
	if response.Messages[0].ID != messagerID(8, 10) {
		t.Fatal(response)
	}
	_, err := readonly.Exec(`UPDATE chat_messages SET content='forbidden' WHERE id=$1`, messagerID(8, 10))
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "25006" {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = messagerpg.NewSyntheticProjectionRepository(db, messagerNoneMode).Sync(ctx, messager.SyncQuery{RoomID: room, AccountID: messagerOther}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	broken := database(t)
	broken.Close()
	w := messagerRequest(messagerHandler(t, broken, messagerOther), "POST", "/api/chat/"+room+"/sync", `{"last_sync_timestamp":0}`)
	if w.Code != 503 || strings.TrimSpace(w.Body.String()) != `{"error":"sync_unavailable"}` || w.Header().Get("X-Total") != "" {
		t.Fatal(w)
	}
	// A malformed non-array attachment cannot be reported as a successful partial row.
	query(t, db, `UPDATE chat_messages SET attachments='{}' WHERE id=$1`, messagerID(8, 10))
	w = messagerRequest(messagerHandler(t, db, messagerOther), "POST", "/api/chat/"+room+"/sync", `{"last_sync_timestamp":0}`)
	if w.Code != 503 || w.Header().Get("X-Total") != "" {
		t.Fatal(w)
	}
}

func TestPGMessagerRestartPersistence(t *testing.T) {
	db := database(t)
	room, sender := messagerID(9, 1), messagerID(9, 2)
	switch os.Getenv("PG_TEST_PHASE") {
	case "before":
		messagerFixture(t, db, 9, false, messagerNoneMode)
		messagerInsert(t, db, 9, 10, room, sender, 9007199254740993, messagerBase(), "messages.delete")
		query(t, db, `UPDATE chat_messages SET meta=$1::jsonb WHERE id=$2`, `{"message_id":"bd000009-0000-0000-0000-000000000011"}`, messagerID(9, 10))
	case "after":
	default:
		t.Fatal("missing restart phase")
	}
	response := messagerSync(t, messagerHandler(t, db, messagerAccount), room, `{"last_sync_timestamp":0,"missing_sequences":[9007199254740993]}`, "1")
	if len(response.Messages) != 1 || response.Messages[0].ID != messagerID(9, 10) || response.Messages[0].Type != "messages.delete" || response.Messages[0].RoomSequence != 9007199254740993 || !strings.Contains(string(response.Messages[0].Meta), "message_id") {
		t.Fatal(response)
	}
	if w := messagerRequest(messagerHandler(t, db, messagerOther), "POST", "/api/chat/"+room+"/sync", `{"last_sync_timestamp":0}`); w.Code != 403 {
		t.Fatal(w)
	}
}

func messagerUnreadID(t *testing.T, id string) messager.UnreadID {
	t.Helper()
	data, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	if err != nil || len(data) != 16 {
		t.Fatal(id, err)
	}
	var result messager.UnreadID
	copy(result[:], data)
	return result
}
func TestPGMessagerUnreadMatchesRetainedPolicy(t *testing.T) {
	db := database(t)
	base := messagerBase()
	readAt := base.Add(time.Hour)
	joined := base
	pureMembers := []messager.UnreadMember{}
	pureMessages := []messager.UnreadMessage{}
	for group := 10; group <= 12; group++ {
		room, self := messagerFixture(t, db, group, true, messagerNoneMode)
		sender := messagerID(group, 3)
		notify := int32((group - 9) * 11)
		symbolic := []messager.UnreadNotify{messager.UnreadNotifyAll, messager.UnreadNotifyMentions, messager.UnreadNotifyNone}[group-10]
		query(t, db, `UPDATE chat_members SET notify=$1,last_read_at=$2 WHERE id=$3`, notify, readAt, self)
		query(t, db, `INSERT INTO chat_members(id,chat_room_id,account_id,joined_at) VALUES($1,$2,$3,now())`, sender, room, messagerOther)
		pureMembers = append(pureMembers, messager.UnreadMember{ID: messagerUnreadID(t, self), AccountID: messagerUnreadID(t, messagerAccount), RoomID: messagerUnreadID(t, room), Notify: symbolic, JoinedAt: &joined, LastReadAt: &readAt})
		for n := 10; n < 18; n++ {
			typ := "text"
			created := base.Add(2 * time.Hour)
			author := sender
			mentions := []messager.UnreadID{}
			if n == 12 {
				typ = "placeholder"
			}
			if n == 13 {
				typ = "messages.update"
			}
			if n == 14 {
				created = readAt
			}
			if n == 15 {
				author = self
			}
			messagerInsert(t, db, group, n, room, author, int64(n-9), created, typ)
			if n == 11 || n == 16 {
				query(t, db, `UPDATE chat_messages SET members_mentioned=jsonb_build_array($1::text) WHERE id=$2`, messagerAccount, messagerID(group, n))
				mentions = append(mentions, messagerUnreadID(t, messagerAccount))
			}
			if n == 16 {
				query(t, db, `UPDATE chat_messages SET thread_id=$1 WHERE id=$2`, messagerID(group, 10), messagerID(group, n))
			}
			if n == 17 {
				query(t, db, `UPDATE chat_messages SET deleted_at=now() WHERE id=$1`, messagerID(group, n))
				continue
			}
			pureMessages = append(pureMessages, messager.UnreadMessage{RoomID: messagerUnreadID(t, room), SenderID: messagerUnreadID(t, author), Type: typ, CreatedAt: created, MembersMentioned: mentions})
		}
	}
	repo, err := messagerpg.NewSyntheticProjectionRepositoryWithUnread(db, messagerNoneMode, messagerpg.SyntheticUnreadPolicy{All: 11, Mentions: 22, None: 33})
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.Unread(context.Background(), messagerAccount)
	if err != nil {
		t.Fatal(err)
	}
	want, err := messager.CountUnreadMessageForUser(messagerUnreadID(t, messagerAccount), nil, pureMembers, pureMessages)
	if err != nil {
		t.Fatal(err)
	}
	for group := 10; group <= 12; group++ {
		room := messagerID(group, 1)
		if got[room] != want[messagerUnreadID(t, room)] || !got[room].HasUnread {
			t.Fatalf("room %s got=%+v want=%+v", room, got[room], want[messagerUnreadID(t, room)])
		}
	}
	// Other test rooms use an unknown synthetic notify default, contributing no
	// notification count. This makes the real global HTTP sum exactly five.
	h, err := messager.NewSyntheticProjectionSyncHandler(messager.NewSyntheticProjectionSyncService(repo), messagerAccount)
	if err != nil {
		t.Fatal(err)
	}
	w := messagerRequest(h, "GET", "/api/chat/unread", "")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "5" {
		t.Fatal(w)
	}
	query(t, db, `UPDATE chat_members SET leave_at=now() WHERE id=$1`, messagerID(10, 2))
	got, err = repo.Unread(context.Background(), messagerAccount)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := got[messagerID(10, 1)]; exists {
		t.Fatal("left member counted")
	}
	// Independent enum opt-in is enforced even with real persisted rows.
	if _, err = messagerpg.NewSyntheticProjectionRepository(db, messagerNoneMode).Unread(context.Background(), messagerAccount); !errors.Is(err, messagerpg.ErrUnverifiedPolicy) {
		t.Fatal(err)
	}
}
