// SPDX-License-Identifier: AGPL-3.0-only
// Source-informed translation; see unread-PROVENANCE.md.
package messager

import (
	"errors"
	"math"
	"strings"
	"time"
)

// UnreadID is an already-decoded identifier, not a wire GUID codec.
type UnreadID [16]byte

// UnreadNotify uses symbolic values deliberately: the source enum's numeric
// encoding is not established by the licensed snapshots available here.
type UnreadNotify string

const (
	UnreadNotifyAll      UnreadNotify = "all"
	UnreadNotifyMentions UnreadNotify = "mentions"
	UnreadNotifyNone     UnreadNotify = "none"
)

// UnreadMember is a projection of an eligible member row. Callers must resolve
// database global filters before constructing snapshots. This is not auth.
type UnreadMember struct {
	ID, AccountID, RoomID         UnreadID
	JoinedAt, LeaveAt, LastReadAt *time.Time
	Notify                        UnreadNotify
}

// UnreadMessage contains only fields read by the unread queries. There is no
// guessed deletion, thread, or room-visibility filter in this projection.
type UnreadMessage struct {
	RoomID, SenderID UnreadID
	Type             string
	CreatedAt        time.Time
	MembersMentioned []UnreadID
}

type UnreadSummary struct {
	UnreadCount int32
	HasUnread   bool
}

var ErrUnreadCountOverflow = errors.New("unread count exceeds signed 32-bit source count")

func activeUnreadMember(m UnreadMember, account UnreadID) bool {
	return m.AccountID == account && m.JoinedAt != nil && m.LeaveAt == nil
}

func isUnreadMessage(m UnreadMessage, member UnreadMember) bool {
	return m.RoomID == member.RoomID && m.SenderID != member.ID &&
		m.Type != "placeholder" && !strings.HasPrefix(m.Type, "messages.") &&
		(member.LastReadAt == nil || m.CreatedAt.After(*member.LastReadAt))
}

func mentionsUnreadAccount(m UnreadMessage, account UnreadID) bool {
	for _, id := range m.MembersMentioned {
		if id == account {
			return true
		}
	}
	return false
}

func incrementUnreadCount(count *int32) error {
	if *count == math.MaxInt32 {
		return ErrUnreadCountOverflow
	}
	*count++
	return nil
}

// CountUnreadMessage computes the single-room query on a prefiltered snapshot.
// If duplicate active memberships exist, the first supplied row wins, matching
// FirstOrDefault on that row order; upstream database ordering is unspecified.
func CountUnreadMessage(account, room UnreadID, members []UnreadMember, messages []UnreadMessage) (int32, error) {
	for _, member := range members {
		if !activeUnreadMember(member, account) || member.RoomID != room {
			continue
		}
		if member.Notify == UnreadNotifyNone {
			return 0, nil
		}
		var count int32
		for _, message := range messages {
			if !isUnreadMessage(message, member) {
				continue
			}
			if member.Notify == UnreadNotifyMentions && !mentionsUnreadAccount(message, account) {
				continue
			}
			if err := incrementUnreadCount(&count); err != nil {
				return 0, err
			}
		}
		return count, nil
	}
	return 0, nil
}

// CountUnreadMessageForUser computes per-room notification counts and unread
// indicators. nil roomIDs selects all rooms; an empty non-nil slice selects none.
// HasUnread is independent of notification preference, including None.
// Duplicate memberships retain join multiplicity, as in the source SQL query.
func CountUnreadMessageForUser(account UnreadID, roomIDs []UnreadID, members []UnreadMember, messages []UnreadMessage) (map[UnreadID]UnreadSummary, error) {
	selected := make(map[UnreadID]bool, len(roomIDs))
	for _, id := range roomIDs {
		selected[id] = true
	}
	byRoom := make(map[UnreadID][]UnreadMessage)
	for _, message := range messages {
		byRoom[message.RoomID] = append(byRoom[message.RoomID], message)
	}
	result := make(map[UnreadID]UnreadSummary)
	// Source also materializes Count() for HasUnread; preserve its overflow limit.
	rawCounts := make(map[UnreadID]int32)
	for _, member := range members {
		if !activeUnreadMember(member, account) || (roomIDs != nil && !selected[member.RoomID]) {
			continue
		}
		summary := result[member.RoomID]
		for _, message := range byRoom[member.RoomID] {
			if !isUnreadMessage(message, member) {
				continue
			}
			raw := rawCounts[member.RoomID]
			if err := incrementUnreadCount(&raw); err != nil {
				return nil, err
			}
			rawCounts[member.RoomID] = raw
			summary.HasUnread = true
			if member.Notify == UnreadNotifyAll || (member.Notify == UnreadNotifyMentions && mentionsUnreadAccount(message, account)) {
				if err := incrementUnreadCount(&summary.UnreadCount); err != nil {
					return nil, err
				}
			}
		}
		result[member.RoomID] = summary
	}
	return result, nil
}
