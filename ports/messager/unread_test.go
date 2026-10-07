// SPDX-License-Identifier: AGPL-3.0-only
package messager

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

func unreadID(n byte) UnreadID { return UnreadID{n} }

func TestUnreadNotificationPolicy(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	account, room, memberID := unreadID(1), unreadID(2), unreadID(3)
	messages := []UnreadMessage{
		{RoomID: room, SenderID: unreadID(4), Type: "text", CreatedAt: at.Add(time.Second), MembersMentioned: []UnreadID{memberID}},
		{RoomID: room, SenderID: unreadID(4), Type: "text", CreatedAt: at.Add(time.Second), MembersMentioned: []UnreadID{account, account}},
	}
	for _, tc := range []struct {
		mode          UnreadNotify
		single, multi int32
	}{
		{UnreadNotifyAll, 2, 2}, {UnreadNotifyMentions, 1, 1}, {UnreadNotifyNone, 0, 0}, {UnreadNotify("unresolved"), 2, 0},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			members := []UnreadMember{{ID: memberID, AccountID: account, RoomID: room, JoinedAt: &at, LastReadAt: &at, Notify: tc.mode}}
			got, err := CountUnreadMessage(account, room, members, messages)
			if err != nil || got != tc.single {
				t.Fatalf("single %d %v", got, err)
			}
			many, err := CountUnreadMessageForUser(account, nil, members, messages)
			if err != nil || many[room] != (UnreadSummary{tc.multi, true}) {
				t.Fatalf("multi %v %v", many, err)
			}
		})
	}
}

func TestUnreadSelectionBoundaries(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	account, room, memberID := unreadID(1), unreadID(2), unreadID(3)
	member := UnreadMember{ID: memberID, AccountID: account, RoomID: room, JoinedAt: &at, LastReadAt: &at, Notify: UnreadNotifyAll}
	for _, tc := range []struct {
		name    string
		message UnreadMessage
		want    int32
	}{
		{"after", UnreadMessage{room, unreadID(4), "text", at.Add(time.Nanosecond), nil}, 1},
		{"same instant offset", UnreadMessage{room, unreadID(4), "text", at.In(time.FixedZone("offset", 3600)), nil}, 0},
		{"before", UnreadMessage{room, unreadID(4), "text", at.Add(-time.Nanosecond), nil}, 0},
		{"self", UnreadMessage{room, memberID, "text", at.Add(time.Second), nil}, 0},
		{"wrong room", UnreadMessage{unreadID(9), unreadID(4), "text", at.Add(time.Second), nil}, 0},
		{"placeholder", UnreadMessage{room, unreadID(4), "placeholder", at.Add(time.Second), nil}, 0},
		{"control", UnreadMessage{room, unreadID(4), "messages.update", at.Add(time.Second), nil}, 0},
		{"prefix only", UnreadMessage{room, unreadID(4), "messages", at.Add(time.Second), nil}, 1},
		{"case sensitive", UnreadMessage{room, unreadID(4), "Messages.update", at.Add(time.Second), nil}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CountUnreadMessage(account, room, []UnreadMember{member}, []UnreadMessage{tc.message})
			if err != nil || got != tc.want {
				t.Fatalf("%d %v", got, err)
			}
			many, err := CountUnreadMessageForUser(account, nil, []UnreadMember{member}, []UnreadMessage{tc.message})
			if err != nil || many[room] != (UnreadSummary{tc.want, tc.want > 0}) {
				t.Fatalf("%v %v", many, err)
			}
		})
	}
	member.LastReadAt = nil
	got, err := CountUnreadMessage(account, room, []UnreadMember{member}, []UnreadMessage{{RoomID: room, SenderID: unreadID(4), Type: "text", CreatedAt: at.Add(-time.Hour)}})
	if got != 1 || err != nil {
		t.Fatal(got, err)
	}
}

func TestUnreadMembershipScopeAndMultiplicity(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	account, room := unreadID(1), unreadID(2)
	active := UnreadMember{ID: unreadID(3), AccountID: account, RoomID: room, JoinedAt: &at, Notify: UnreadNotifyAll}
	pending, left, other, emptyRoom := active, active, active, active
	pending.JoinedAt = nil
	left.LeaveAt = &at
	other.AccountID = unreadID(9)
	emptyRoom.RoomID = unreadID(8)
	message := UnreadMessage{RoomID: room, SenderID: unreadID(4), Type: "text", CreatedAt: at}
	members := []UnreadMember{pending, left, other, active, emptyRoom}
	for _, tc := range []struct {
		ids  []UnreadID
		want map[UnreadID]UnreadSummary
	}{
		{nil, map[UnreadID]UnreadSummary{room: {1, true}, emptyRoom.RoomID: {0, false}}},
		{[]UnreadID{}, map[UnreadID]UnreadSummary{}},
		{[]UnreadID{room, room}, map[UnreadID]UnreadSummary{room: {1, true}}},
		{[]UnreadID{unreadID(99)}, map[UnreadID]UnreadSummary{}},
	} {
		got, err := CountUnreadMessageForUser(account, tc.ids, members, []UnreadMessage{message})
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("got %v want %v err %v", got, tc.want, err)
		}
	}
	got, err := CountUnreadMessage(account, room, members, []UnreadMessage{message})
	if err != nil || got != 1 {
		t.Fatal(got, err)
	}
	got, err = CountUnreadMessage(account, room, members[:3], []UnreadMessage{message})
	if err != nil || got != 0 {
		t.Fatal(got, err)
	}
	duplicate := []UnreadMember{active, active}
	multi, err := CountUnreadMessageForUser(account, nil, duplicate, []UnreadMessage{message})
	if err != nil || multi[room].UnreadCount != 2 {
		t.Fatal(multi, err)
	}
	duplicate[0].Notify = UnreadNotifyNone
	got, err = CountUnreadMessage(account, room, duplicate, []UnreadMessage{message})
	if err != nil || got != 0 {
		t.Fatal(got, err)
	}
}

func TestUnreadCountOverflow(t *testing.T) {
	n := int32(math.MaxInt32 - 1)
	if err := incrementUnreadCount(&n); err != nil || n != math.MaxInt32 {
		t.Fatal(n, err)
	}
	if err := incrementUnreadCount(&n); !errors.Is(err, ErrUnreadCountOverflow) || n != math.MaxInt32 {
		t.Fatal(n, err)
	}
}
