// SPDX-License-Identifier: AGPL-3.0-only
package rewindpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/passport/rewind"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

func (r *Repository) snapshot(ctx context.Context, tx *sql.Tx, actor string, start, end, now time.Time) (s rewind.Snapshot, e error) {
	s.Unavailable = []string{}
	s.MostCalledAccounts = []rewind.Reference{}
	s.MostCalledChatMembers = []string{}
	if e = r.passportMetrics(ctx, tx, actor, start, end, &s); e != nil {
		return
	}
	if e = r.sphereMetrics(ctx, tx, actor, start, end, &s); e != nil {
		return
	}
	if e = r.messagerMetrics(ctx, tx, actor, start, end, now, &s); e != nil {
		return
	}
	return
}
func (r *Repository) passportMetrics(ctx context.Context, tx *sql.Tx, actor string, start, end time.Time, s *rewind.Snapshot) error {
	if e := tx.QueryRowContext(ctx, `SELECT count(*)FILTER(WHERE status=100),count(*)FILTER(WHERE status=-100)FROM owned_social_effective_relationships WHERE account_id=$1::text::uuid AND created_at>=$2::timestamptz AND created_at<$3::timestamptz`, actor, start, end).Scan(&s.Passport.NewFriendsCount, &s.Passport.NewBlockedCount); e != nil {
		return e
	}
	rows, e := tx.QueryContext(ctx, `SELECT DISTINCT day FROM owned_progression_checkins WHERE account_id=$1::text::uuid AND created_at>=$2::timestamptz AND created_at<$3::timestamptz ORDER BY day LIMIT 368`, actor, start, end)
	if e != nil {
		return e
	}
	days := []time.Time{}
	for rows.Next() {
		var d time.Time
		if e = rows.Scan(&d); e != nil {
			rows.Close()
			return e
		}
		days = append(days, d)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if len(days) > 367 {
		return rewind.ErrUnavailable
	}
	s.Passport.MaxCheckInStreak, s.Passport.CheckInCompleteness = rewind.CheckInMetrics(days)
	if r.options.ActionLogs == nil {
		s.Unavailable = append(s.Unavailable, "passport.action_activity")
		return nil
	}
	instants, e := r.options.ActionLogs.ActionTimes(ctx, r.db, tx, actor, start, end, 100000)
	if e != nil {
		return e
	}
	if len(instants) > 100000 {
		return rewind.ErrUnavailable
	}
	var zone string
	e = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT time_zone FROM owned_accounts_profiles WHERE account_id=$1::text::uuid),'')`, actor).Scan(&zone)
	if e != nil {
		return e
	}
	loc := time.UTC
	if zone != "" && zone != "Local" {
		loc, e = time.LoadLocation(zone)
		if e != nil {
			return rewind.ErrUnavailable
		}
	}
	activityMetrics(&s.Passport, instants, start, end, loc)
	return nil
}
func activityMetrics(out *rewind.Passport, values []time.Time, start, end time.Time, loc *time.Location) {
	days := map[string]int{}
	weekdays := map[string]int{}
	latest, early := -1, -1
	for _, v := range values {
		if v.Before(start) || !v.Before(end) {
			continue
		}
		days[v.UTC().Format("2006-01-02")]++
		weekdays[v.UTC().Weekday().String()]++
		local := v.In(loc)
		minute := local.Hour()*60 + local.Minute()
		if minute > latest {
			latest = minute
		}
		if minute < 360 && minute > early {
			early = minute
		}
	}
	best := func(m map[string]int) *string {
		keys := []string{}
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		winner := ""
		n := 0
		for _, k := range keys {
			if m[k] > n {
				winner = k
				n = m[k]
			}
		}
		if n == 0 {
			return nil
		}
		return &winner
	}
	out.MostActiveDay = best(days)
	out.MostActiveWeekday = best(weekdays)
	if early >= 0 {
		latest = early
	}
	if latest >= 0 {
		v := time.Date(2000, 1, 1, latest/60, latest%60, 0, 0, time.UTC).Format("15:04")
		out.LatestActiveTime = &v
	}
}
func (r *Repository) postSource() string {
	return ` FROM owned_text_posts p JOIN owned_publishers u ON u.id=p.publisher_id WHERE p.deleted_at IS NULL AND u.deleted_at IS NULL AND(u.account_id IS NULL OR EXISTS(SELECT 1 FROM owned_authority_accounts owner WHERE owner.id=u.account_id AND NOT owner.disabled)) AND ` + postpg.DetailPredicate(r.visibility)
}
func (r *Repository) creatorScope() string {
	return ` AND EXISTS(SELECT 1 FROM owned_publisher_members m WHERE m.publisher_id=u.id AND m.account_id=$1::text::uuid AND m.joined_at IS NOT NULL AND m.revoked_at IS NULL) AND ` + postpg.PublisherMemberPredicate(r.visibility)
}
func ref(row *sql.Row) (*rewind.Reference, error) {
	v := &rewind.Reference{}
	e := row.Scan(&v.ID, &v.Count)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	return v, e
}
func (r *Repository) sphereMetrics(ctx context.Context, tx *sql.Tx, actor string, start, end time.Time, s *rewind.Snapshot) error {
	base := r.postSource()
	owned := base + r.creatorScope()
	args := []any{actor, start, end}
	if e := tx.QueryRowContext(ctx, `SELECT count(*)`+owned+` AND p.created_at>=$2::timestamptz AND p.created_at<$3::timestamptz`, args...).Scan(&s.TotalPosts); e != nil {
		return e
	}
	if e := tx.QueryRowContext(ctx, `SELECT COALESCE(sum((SELECT count(*) FROM owned_post_reactions reaction WHERE reaction.post_id=p.id AND reaction.attitude=0 AND reaction.created_at>=$2::timestamptz AND reaction.created_at<$3::timestamptz)),0)`+owned, args...).Scan(&s.TotalUpvotes); e != nil {
		return e
	}
	e := tx.QueryRowContext(ctx, `SELECT p.id::text`+owned+` AND p.created_at>=$2::timestamptz AND p.created_at<$3::timestamptz ORDER BY(SELECT COALESCE(sum(CASE WHEN reaction.attitude=0 THEN 1 WHEN reaction.attitude=2 THEN -1 ELSE 0 END),0)FROM owned_post_reactions reaction WHERE reaction.post_id=p.id)DESC,p.id LIMIT 1`, args...).Scan(&s.MostPopularPost)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	day := &rewind.ProductiveDay{}
	e = tx.QueryRowContext(ctx, `SELECT to_char(p.created_at AT TIME ZONE 'UTC','YYYY-MM-DD'),count(*)`+owned+` AND p.created_at>=$2::timestamptz AND p.created_at<$3::timestamptz GROUP BY 1 ORDER BY count(*)DESC,1 LIMIT 1`, args...).Scan(&day.Date, &day.PostCount)
	if e == nil {
		s.MostProductiveDay = day
	} else if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	s.MostLovedPublisher, e = ref(tx.QueryRowContext(ctx, `SELECT u.id::text,count(*) FROM owned_post_reactions reaction JOIN owned_text_posts p ON p.id=reaction.post_id JOIN owned_publishers u ON u.id=p.publisher_id WHERE reaction.account_id=$1::text::uuid AND reaction.attitude=0 AND reaction.created_at>=$2::timestamptz AND reaction.created_at<$3::timestamptz AND p.deleted_at IS NULL AND u.deleted_at IS NULL AND `+postpg.DetailPredicate(r.visibility)+` GROUP BY u.id ORDER BY count(*)DESC,u.id LIMIT 1`, args...))
	if e != nil {
		return e
	}
	s.MostLovedAudience, e = ref(tx.QueryRowContext(ctx, `SELECT reaction.account_id::text,count(*) FROM owned_post_reactions reaction JOIN owned_text_posts p ON p.id=reaction.post_id JOIN owned_publishers u ON u.id=p.publisher_id WHERE reaction.attitude=0 AND reaction.created_at>=$2::timestamptz AND reaction.created_at<$3::timestamptz AND p.deleted_at IS NULL AND u.deleted_at IS NULL AND `+postpg.DetailPredicate(r.visibility)+r.creatorScope()+` GROUP BY reaction.account_id ORDER BY count(*)DESC,reaction.account_id LIMIT 1`, args...))
	if e != nil {
		return e
	}
	if r.options.Tokenizer == nil {
		s.Unavailable = append(s.Unavailable, "sphere.top_words")
		return nil
	}
	rows, e := tx.QueryContext(ctx, `SELECT p.content`+owned+` AND p.created_at>=$2::timestamptz AND p.created_at<$3::timestamptz AND p.content<>'' ORDER BY length(p.content)DESC,p.id LIMIT 1000`, args...)
	if e != nil {
		return e
	}
	texts := []string{}
	size := 0
	for rows.Next() {
		var v string
		if e = rows.Scan(&v); e != nil {
			rows.Close()
			return e
		}
		size += len(v)
		if size > 1048576 {
			rows.Close()
			s.Unavailable = append(s.Unavailable, "sphere.top_words_input_limit")
			return nil
		}
		texts = append(texts, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	words, e := r.options.Tokenizer.TopWords(ctx, texts, 100)
	if e != nil {
		return e
	}
	if len(words) > 100 {
		return rewind.ErrUnavailable
	}
	seen := map[string]bool{}
	for _, v := range words {
		if v.Word == "" || len(v.Word) > 256 || !utf8.ValidString(v.Word) || strings.ContainsRune(v.Word, 0) || v.Count < 1 || seen[v.Word] {
			return rewind.ErrUnavailable
		}
		seen[v.Word] = true
	}
	s.TopWords = words
	if s.TopWords == nil {
		s.TopWords = []rewind.Word{}
	}
	return nil
}
func (r *Repository) roomSource() string {
	return ` AND room.deleted_at IS NULL AND EXISTS(SELECT 1 FROM chat_members active WHERE active.chat_room_id=room.id AND active.account_id=$1::text::uuid AND active.joined_at IS NOT NULL AND active.leave_at IS NULL AND active.deleted_at IS NULL AND NOT active.owned_banned) AND ` + r.rooms.ReadSQL("room.id", "$1")
}
func (r *Repository) messagerMetrics(ctx context.Context, tx *sql.Tx, actor string, start, end, now time.Time, s *rewind.Snapshot) error {
	args := []any{actor, start, end}
	base := ` FROM chat_messages message JOIN chat_members sender ON sender.id=message.sender_id JOIN chat_rooms room ON room.id=message.chat_room_id WHERE sender.account_id=$1::text::uuid AND message.created_at>=$2::timestamptz AND message.created_at<$3::timestamptz AND message.deleted_at IS NULL` + r.roomSource()
	var e error
	s.MostMessagedChat, e = ref(tx.QueryRowContext(ctx, `SELECT room.id::text,count(*)`+base+` AND room.owned_type=0 GROUP BY room.id ORDER BY count(*)DESC,room.id LIMIT 1`, args...))
	if e != nil {
		return e
	}
	s.MostMessagedDirectChat, e = ref(tx.QueryRowContext(ctx, `SELECT room.id::text,count(*)`+base+` AND room.owned_type=1 GROUP BY room.id ORDER BY count(*)DESC,room.id LIMIT 1`, args...))
	if e != nil {
		return e
	}
	calls := ` FROM call_v1.calls call JOIN chat_members sender ON sender.id=call.sender_id JOIN chat_rooms room ON room.id=call.room_id WHERE sender.account_id=$1::text::uuid AND call.created_at>=$2::timestamptz AND call.created_at<$3::timestamptz AND call.state IN('active','ending','ended')` + r.roomSource()
	duration := `COALESCE(sum(GREATEST(0,floor(EXTRACT(EPOCH FROM(LEAST(COALESCE(call.ended_at,$4::timestamptz),$4::timestamptz,$3::timestamptz)-call.created_at))))),0)::bigint`
	callArgs := []any{actor, start, end, now}
	s.MostCalledChat, e = ref(tx.QueryRowContext(ctx, `SELECT room.id::text,`+duration+calls+` AND room.owned_type=0 GROUP BY room.id ORDER BY 2 DESC,room.id LIMIT 1`, callArgs...))
	if e != nil {
		return e
	}
	rows, e := tx.QueryContext(ctx, `SELECT room.id::text,`+duration+calls+` AND room.owned_type=1 GROUP BY room.id ORDER BY 2 DESC,room.id LIMIT 3`, callArgs...)
	if e != nil {
		return e
	}
	direct := []rewind.Reference{}
	for rows.Next() {
		var v rewind.Reference
		if e = rows.Scan(&v.ScopeID, &v.Count); e != nil {
			rows.Close()
			return e
		}
		direct = append(direct, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, v := range direct {
		e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM chat_members WHERE chat_room_id=$1::text::uuid AND account_id<>$2::uuid AND joined_at IS NOT NULL AND leave_at IS NULL AND deleted_at IS NULL AND NOT owned_banned ORDER BY account_id LIMIT 1`, v.ScopeID, actor).Scan(&v.ID)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			return e
		}
		s.MostCalledAccounts = append(s.MostCalledAccounts, v)
	}
	if s.MostCalledChat != nil {
		rows, e := tx.QueryContext(ctx, `SELECT sender.account_id::text FROM chat_messages message JOIN chat_members sender ON sender.id=message.sender_id WHERE message.chat_room_id=$1::text::uuid AND message.created_at>=$2::timestamptz AND message.created_at<$3::timestamptz AND message.deleted_at IS NULL GROUP BY sender.account_id ORDER BY count(*)DESC,sender.account_id LIMIT 3`, s.MostCalledChat.ID, start, end)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				return e
			}
			s.MostCalledChatMembers = append(s.MostCalledChatMembers, id)
		}
		if e = rows.Err(); e != nil {
			return e
		}
	}
	return nil
}
