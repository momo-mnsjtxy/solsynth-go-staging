// SPDX-License-Identifier: AGPL-3.0-only
// Source-informed PostgreSQL read projection; see schema-evidence.json.
package messagerpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
)

var (
	ErrUnverifiedPolicy        = messager.ErrSyncPolicyUnverified
	ErrDatabaseRequired        = errors.New("messagerpg: database required")
	ErrInvalidStoredProjection = errors.New("messagerpg: invalid stored projection")
)

// Repository performs only read-only repeatable-read transactions. It owns no
// connection and never creates schema. Active-row semantics and enum values
// remain explicit synthetic choices; production construction is always denied.
type Repository struct {
	owned              bool
	roomAccess         rooms.AccessPolicy
	unreadPolicy       *SyntheticUnreadPolicy
	db                 *sql.DB
	synthetic          bool
	noneEncryptionMode int32
	now                func() time.Time
}

func (r *Repository) WithRoomAccess(p rooms.AccessPolicy) *Repository { r.roomAccess = p; return r }

func New(db *sql.DB) *Repository { return &Repository{db: db} }
func NewSyntheticProjectionRepository(db *sql.DB, noneEncryptionMode int32) *Repository {
	return &Repository{db: db, synthetic: true, noneEncryptionMode: noneEncryptionMode, now: time.Now}
}

var _ messager.SyncReader = (*Repository)(nil)

func (r *Repository) ready(ctx context.Context) error {
	if r == nil || !r.synthetic {
		return ErrUnverifiedPolicy
	}
	return ctx.Err()
}
func (r *Repository) transaction(ctx context.Context) (*sql.Tx, error) {
	if r.db == nil {
		return nil, ErrDatabaseRequired
	}
	return r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
}

// requireRoom implements the controller's access ordering inside the same
// snapshot as selection. It does not infer membership from a supplied identity.
func (r *Repository) requireRoom(ctx context.Context, tx *sql.Tx, room, account string) error {
	var public bool
	var encryption int32
	var guarded bool
	var err error
	if r.owned {
		err = tx.QueryRowContext(ctx, `SELECT is_public,encryption_mode,owned_relation_guard FROM chat_rooms WHERE id=$1 AND deleted_at IS NULL`, room).Scan(&public, &encryption, &guarded)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT is_public, encryption_mode FROM chat_rooms WHERE id=$1 AND deleted_at IS NULL`, room).Scan(&public, &encryption)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return messager.ErrSyncRoomNotFound
	}
	if err != nil {
		return err
	}
	if guarded {
		if r.roomAccess == nil {
			return messager.ErrSyncForbidden
		}
		allowed, e := r.roomAccess.CanReadInTransaction(ctx, tx, account, room)
		if e != nil {
			return e
		}
		if !allowed {
			return messager.ErrSyncForbidden
		}
	}
	if public && encryption == r.noneEncryptionMode {
		return nil
	}
	if account == "" {
		return messager.ErrSyncUnauthorized
	}
	var active bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM chat_members WHERE chat_room_id=$1 AND account_id=$2 AND joined_at IS NOT NULL AND leave_at IS NULL AND deleted_at IS NULL)`, room, account).Scan(&active)
	if err != nil {
		return err
	}
	if !active {
		return messager.ErrSyncForbidden
	}
	return nil
}

// Candidate visibility is explicit for every participating table. The unknown
// shared ApplySoftDeleteFilters implementation has NOT been treated as verified.
const visibleMessages = `m.chat_room_id=$1 AND m.deleted_at IS NULL
 AND EXISTS (SELECT 1 FROM chat_members sender WHERE sender.id=m.sender_id AND sender.chat_room_id=m.chat_room_id AND sender.deleted_at IS NULL)`
const projectionColumns = `m.id::text,m.chat_room_id::text,m.sender_id::text,m.type,m.room_sequence,
 m.content,m.client_message_id,m.created_at,m.updated_at,m.deleted_at,m.edited_at,
 m.meta,m.members_mentioned,m.attachments,m.encryption_meta,m.reactions_count,
 m.replied_message_id::text,m.forwarded_message_id::text,m.thread_id::text`

func sequenceArray(values []int64) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, v := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatInt(v, 10))
	}
	b.WriteByte('}')
	return b.String()
}
func selection(q messager.SyncQuery) (string, []any) {
	where := visibleMessages + ` AND m.thread_id IS NULL`
	args := []any{q.RoomID}
	plan := messager.NewRecoveryPlan(q.Request.MissingSequences, q.Request.MissingSequenceRanges)
	if !plan.UsesSequences() {
		return where + ` AND m.created_at > $2`, append(args, time.UnixMilli(q.Request.LastSyncTimestamp).UTC())
	}
	ranges := plan.Ranges()
	starts, ends := make([]int64, len(ranges)), make([]int64, len(ranges))
	for i, v := range ranges {
		starts[i] = v.StartSequence
		ends[i] = v.EndSequence
	}
	where += ` AND (m.room_sequence=ANY($2::bigint[]) OR EXISTS
 (SELECT 1 FROM unnest($3::bigint[],$4::bigint[]) AS wanted(first_sequence,last_sequence)
 WHERE m.room_sequence BETWEEN wanted.first_sequence AND wanted.last_sequence))`
	return where, append(args, sequenceArray(plan.Sequences()), sequenceArray(starts), sequenceArray(ends))
}
func count(ctx context.Context, tx *sql.Tx, where string, args []any) (int32, error) {
	var total int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM chat_messages m WHERE `+where, args...).Scan(&total); err != nil {
		return 0, err
	}
	if total < 0 || total > math.MaxInt32 {
		return 0, ErrInvalidStoredProjection
	}
	return int32(total), nil
}

func (r *Repository) Sync(ctx context.Context, q messager.SyncQuery) (messager.SyncResponse, error) {
	if err := r.ready(ctx); err != nil {
		return messager.SyncResponse{}, err
	}
	if err := messager.ValidateSyncQuery(q); err != nil {
		return messager.SyncResponse{}, err
	}
	tx, err := r.transaction(ctx)
	if err != nil {
		return messager.SyncResponse{}, err
	}
	defer tx.Rollback()
	if err = r.requireRoom(ctx, tx, q.RoomID, q.AccountID); err != nil {
		return messager.SyncResponse{}, err
	}
	where, args := selection(q)
	total, err := count(ctx, tx, where, args)
	if err != nil {
		return messager.SyncResponse{}, err
	}
	messages, err := readMessages(ctx, tx, where+` ORDER BY m.room_sequence ASC,m.created_at ASC LIMIT 1000`, args, q.AccountID)
	if err != nil {
		return messager.SyncResponse{}, err
	}
	now := time.Now
	if r.now != nil {
		now = r.now
	}
	timestamp := now().UTC()
	if len(messages) > 0 {
		timestamp = messages[len(messages)-1].CreatedAt
	}
	if err = tx.Commit(); err != nil {
		return messager.SyncResponse{}, err
	}
	return messager.SyncResponse{Messages: messages, CurrentTimestamp: timestamp, TotalCount: total}, nil
}
func (r *Repository) ListMessages(ctx context.Context, q messager.MessageListQuery) (messager.MessagePage, error) {
	if err := r.ready(ctx); err != nil {
		return messager.MessagePage{}, err
	}
	if err := messager.ValidateMessageListQuery(q); err != nil {
		return messager.MessagePage{}, err
	}
	tx, err := r.transaction(ctx)
	if err != nil {
		return messager.MessagePage{}, err
	}
	defer tx.Rollback()
	if err = r.requireRoom(ctx, tx, q.RoomID, q.AccountID); err != nil {
		return messager.MessagePage{}, err
	}
	where := visibleMessages + ` AND m.thread_id IS NULL`
	total, err := count(ctx, tx, where, []any{q.RoomID})
	if err != nil {
		return messager.MessagePage{}, err
	}
	messages, err := readMessages(ctx, tx, where+` ORDER BY m.room_sequence DESC,m.created_at DESC LIMIT $2 OFFSET $3`, []any{q.RoomID, q.Take, q.Offset}, q.AccountID)
	if err != nil {
		return messager.MessagePage{}, err
	}
	if err = tx.Commit(); err != nil {
		return messager.MessagePage{}, err
	}
	return messager.MessagePage{Messages: messages, TotalCount: total}, nil
}
func (r *Repository) GetMessage(ctx context.Context, q messager.MessageQuery) (messager.MessageProjection, error) {
	if err := r.ready(ctx); err != nil {
		return messager.MessageProjection{}, err
	}
	if err := messager.ValidateMessageQuery(q); err != nil {
		return messager.MessageProjection{}, err
	}
	tx, err := r.transaction(ctx)
	if err != nil {
		return messager.MessageProjection{}, err
	}
	defer tx.Rollback()
	if err = r.requireRoom(ctx, tx, q.RoomID, q.AccountID); err != nil {
		return messager.MessageProjection{}, err
	}
	messages, err := readMessages(ctx, tx, visibleMessages+` AND m.id=$2 LIMIT 1`, []any{q.RoomID, q.MessageID}, q.AccountID)
	if err != nil {
		return messager.MessageProjection{}, err
	}
	if len(messages) == 0 {
		return messager.MessageProjection{}, messager.ErrSyncMessageNotFound
	}
	if err = tx.Commit(); err != nil {
		return messager.MessageProjection{}, err
	}
	return messages[0], nil
}
func rawJSON(data []byte, shape byte, nullable bool) (json.RawMessage, error) {
	if len(data) == 0 || string(data) == "null" {
		if nullable {
			return json.RawMessage("null"), nil
		}
		return nil, ErrInvalidStoredProjection
	}
	if !json.Valid(data) || strings.TrimSpace(string(data))[0] != shape {
		return nil, ErrInvalidStoredProjection
	}
	return append(json.RawMessage{}, data...), nil
}
func readMessages(ctx context.Context, tx *sql.Tx, where string, args []any, account string) ([]messager.MessageProjection, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+projectionColumns+` FROM chat_messages m WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	messages := make([]messager.MessageProjection, 0)
	for rows.Next() {
		var m messager.MessageProjection
		var meta, mentions, attachments, encryption, reactions []byte
		if err = rows.Scan(&m.ID, &m.ChatRoomID, &m.SenderID, &m.Type, &m.RoomSequence, &m.Content, &m.ClientMessageID, &m.CreatedAt, &m.UpdatedAt, &m.DeletedAt, &m.EditedAt, &meta, &mentions, &attachments, &encryption, &reactions, &m.RepliedMessageID, &m.ForwardedMessageID, &m.ThreadID); err != nil {
			rows.Close()
			return nil, err
		}
		for _, v := range []struct {
			data     []byte
			shape    byte
			nullable bool
			dst      *json.RawMessage
		}{{meta, '{', true, &m.Meta}, {mentions, '[', true, &m.MembersMentioned}, {attachments, '[', false, &m.Attachments}, {encryption, '{', true, &m.EncryptionMeta}, {reactions, '{', false, &m.ReactionsCount}} {
			if *v.dst, err = rawJSON(v.data, v.shape, v.nullable); err != nil {
				rows.Close()
				return nil, err
			}
		}
		m.CreatedAt = m.CreatedAt.UTC()
		m.UpdatedAt = m.UpdatedAt.UTC()
		m.Content = messager.FormatMessageContent(m.Content, string(m.EncryptionMeta) != "null")
		if account != "" {
			m.ReactionsMade = make(map[string]bool)
		}
		messages = append(messages, m)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return messages, nil
	}
	ids := make([]string, len(messages))
	index := make(map[string]int, len(messages))
	for i, m := range messages {
		ids[i] = m.ID
		index[m.ID] = i
	}
	// UUIDs come from typed PostgreSQL columns, not unchecked user input.
	array := "{" + strings.Join(ids, ",") + "}"
	if account != "" {
		rows, err = tx.QueryContext(ctx, `SELECT r.message_id::text,r.symbol FROM chat_reactions r
 JOIN chat_members sender ON sender.id=r.sender_id
 WHERE r.message_id=ANY($1::uuid[]) AND sender.account_id=$2
 AND r.deleted_at IS NULL AND sender.deleted_at IS NULL`, array, account)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, symbol string
			if err = rows.Scan(&id, &symbol); err != nil {
				rows.Close()
				return nil, err
			}
			i, ok := index[id]
			if !ok {
				rows.Close()
				return nil, ErrInvalidStoredProjection
			}
			if messages[i].ReactionsMade[symbol] {
				rows.Close()
				return nil, fmt.Errorf("%w: duplicate reaction symbol", ErrInvalidStoredProjection)
			}
			messages[i].ReactionsMade[symbol] = true
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err = rows.Close(); err != nil {
			return nil, err
		}
	}
	// Source uses raw SQL with only thread_id and deleted_at filters. Preserve that
	// boundary rather than guessing that reply sender/room membership affects count.
	rows, err = tx.QueryContext(ctx, `SELECT thread_id::text,count(*) FROM chat_messages
 WHERE thread_id=ANY($1::uuid[]) AND deleted_at IS NULL GROUP BY thread_id`, array)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var count int64
		if err = rows.Scan(&id, &count); err != nil {
			rows.Close()
			return nil, err
		}
		i, ok := index[id]
		if !ok || count <= 0 || count > math.MaxInt32 {
			rows.Close()
			return nil, ErrInvalidStoredProjection
		}
		messages[i].ThreadRepliesCount = int32(count)
		messages[i].IsThreadRoot = true
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	return messages, nil
}

// SyntheticUnreadPolicy supplies only fixture enum values; none is guessed from
// an unlicensed enum. Distinct values prevent contradictory fixture policy.
type SyntheticUnreadPolicy struct{ All, Mentions, None int32 }

func NewSyntheticProjectionRepositoryWithUnread(db *sql.DB, noneEncryptionMode int32, policy SyntheticUnreadPolicy) (*Repository, error) {
	if policy.All == policy.Mentions || policy.All == policy.None || policy.Mentions == policy.None {
		return nil, messager.ErrSyncInvalidRequest
	}
	repo := NewSyntheticProjectionRepository(db, noneEncryptionMode)
	repo.unreadPolicy = &policy
	return repo, nil
}

var _ messager.SyncUnreadReader = (*Repository)(nil)

// Unread ports CountUnreadMessageForUser as an aggregate. It deliberately does
// not add thread/sender/room visibility restrictions absent from that source
// query; only the explicit candidate member/message soft-delete policy applies.
func (r *Repository) Unread(ctx context.Context, account string) (map[string]messager.UnreadSummary, error) {
	if err := r.ready(ctx); err != nil {
		return nil, err
	}
	if r.unreadPolicy == nil {
		return nil, ErrUnverifiedPolicy
	}
	if account == "" {
		return nil, messager.ErrSyncUnauthorized
	}
	if messager.ValidateSyncID(account) != nil {
		return nil, messager.ErrSyncInvalidRequest
	}
	tx, err := r.transaction(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	filter := ""
	if r.owned {
		gate := `NOT r.owned_relation_guard`
		if r.roomAccess != nil {
			gate = r.roomAccess.ReadSQL("r.id", "$1")
		}
		filter = ` AND NOT member.owned_banned AND EXISTS(SELECT 1 FROM chat_rooms r WHERE r.id=member.chat_room_id AND r.deleted_at IS NULL AND ` + gate + `)`
	}
	rows, err := tx.QueryContext(ctx, `SELECT member.chat_room_id::text,
 count(msg.id) FILTER (WHERE member.notify=$2 OR
 (member.notify=$3 AND msg.members_mentioned @> jsonb_build_array($1::text))),
 count(msg.id)
 FROM chat_members member
 LEFT JOIN chat_messages msg ON msg.chat_room_id=member.chat_room_id
 AND msg.sender_id<>member.id AND msg.type<>'placeholder' AND left(msg.type,9)<>'messages.'
 AND (member.last_read_at IS NULL OR msg.created_at>member.last_read_at)
 AND msg.deleted_at IS NULL
 WHERE member.account_id=$1::uuid AND member.joined_at IS NOT NULL
 AND member.leave_at IS NULL AND member.deleted_at IS NULL
	`+filter+` GROUP BY member.chat_room_id`, strings.ToLower(account), r.unreadPolicy.All, r.unreadPolicy.Mentions)
	if err != nil {
		return nil, err
	}
	result := make(map[string]messager.UnreadSummary)
	for rows.Next() {
		var room string
		var count, raw int64
		if err = rows.Scan(&room, &count, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		if count < 0 || raw < count || count > math.MaxInt32 || raw > math.MaxInt32 {
			rows.Close()
			return nil, messager.ErrUnreadCountOverflow
		}
		result[room] = messager.UnreadSummary{UnreadCount: int32(count), HasUnread: raw > 0}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}
