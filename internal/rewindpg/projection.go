// SPDX-License-Identifier: AGPL-3.0-only
package rewindpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/rewind"
	"solsynth.local/go-migration/ports/sphere/posts"
)

func hidden(e error) bool {
	return errors.Is(e, sql.ErrNoRows) || errors.Is(e, accounts.ErrNotFound) || errors.Is(e, accounts.ErrForbidden) || errors.Is(e, posts.ErrNotFound) || errors.Is(e, posts.ErrForbidden) || errors.Is(e, rooms.ErrNotFound) || errors.Is(e, rooms.ErrForbidden) || errors.Is(e, rooms.ErrUnsupported)
}
func (r *Repository) project(ctx context.Context, tx *sql.Tx, p rewind.Point, s rewind.Snapshot, viewer string) (rewind.Point, error) {
	a, e := r.accounts.RewindAccount(ctx, r.db, tx, p.AccountID)
	if e != nil {
		if hidden(e) {
			return p, rewind.ErrNotFound
		}
		return p, e
	}
	p.Account = &a
	p.UnavailableComponents = append([]string{}, s.Unavailable...)
	p.Projection = "owned-partial-v1"
	sphere := map[string]any{"total_post_count": s.TotalPosts, "total_upvote_count": s.TotalUpvotes, "top_words": s.TopWords, "most_productive_day": s.MostProductiveDay, "most_popular_post": nil, "most_loved_publisher": nil, "most_loved_audience": nil}
	if s.MostPopularPost != "" {
		v, e := postpg.ReadWithVisibility(ctx, tx, viewer, s.MostPopularPost, r.visibility)
		if e == nil {
			sphere["most_popular_post"] = v
		} else if !hidden(e) {
			return p, e
		}
	}
	if s.MostLovedPublisher != nil {
		v, e := r.accounts.RewindPublisher(ctx, r.db, tx, viewer, s.MostLovedPublisher.ID)
		if e == nil {
			sphere["most_loved_publisher"] = map[string]any{"publisher": v, "upvote_counts": s.MostLovedPublisher.Count}
		} else if !hidden(e) {
			return p, e
		}
	}
	if s.MostLovedAudience != nil {
		v, e := r.accounts.RewindAccount(ctx, r.db, tx, s.MostLovedAudience.ID)
		if e == nil {
			sphere["most_loved_audience"] = map[string]any{"account": v, "upvote_counts": s.MostLovedAudience.Count}
		} else if !hidden(e) {
			return p, e
		}
	}
	messager := map[string]any{"most_messaged_chat": nil, "most_messaged_direct_chat": nil, "most_called_chat": map[string]any{"chat": nil, "duration": nil}, "most_called_chat_top_members": []accounts.Account{}, "most_called_accounts": []any{}}
	roomReference := func(ref *rewind.Reference, key, quantity string) error {
		if ref == nil {
			return nil
		}
		v, e := r.rooms.RewindRoom(ctx, r.db, tx, viewer, ref.ID)
		if e != nil {
			if hidden(e) {
				return nil
			}
			return e
		}
		messager[key] = map[string]any{"chat": v, quantity: ref.Count}
		return nil
	}
	if e = roomReference(s.MostMessagedChat, "most_messaged_chat", "message_counts"); e != nil {
		return p, e
	}
	if viewer != "" {
		if e = roomReference(s.MostMessagedDirectChat, "most_messaged_direct_chat", "message_counts"); e != nil {
			return p, e
		}
		if e = roomReference(s.MostCalledChat, "most_called_chat", "duration"); e != nil {
			return p, e
		}
		if s.MostCalledChat != nil {
			if _, e = r.rooms.RewindRoom(ctx, r.db, tx, viewer, s.MostCalledChat.ID); e == nil {
				members := []accounts.Account{}
				for _, id := range s.MostCalledChatMembers {
					v, e := r.accounts.RewindAccount(ctx, r.db, tx, id)
					if e == nil {
						members = append(members, v)
					} else if !hidden(e) {
						return p, e
					}
				}
				messager["most_called_chat_top_members"] = members
			} else if !hidden(e) {
				return p, e
			}
		}
		called := []any{}
		for _, ref := range s.MostCalledAccounts {
			if _, e = r.rooms.RewindRoom(ctx, r.db, tx, viewer, ref.ScopeID); e != nil {
				if hidden(e) {
					continue
				}
				return p, e
			}
			v, e := r.accounts.RewindAccount(ctx, r.db, tx, ref.ID)
			if e == nil {
				called = append(called, map[string]any{"account": v, "duration": ref.Count})
			} else if !hidden(e) {
				return p, e
			}
		}
		messager["most_called_accounts"] = called
	} else {
		// A share authorizes the owner's aggregate report, not disclosure of
		// private third-party messaging/call relationships. Never cache these.
		p.UnavailableComponents = append(p.UnavailableComponents, "messager.private_references_redacted")
		// Words may come from private posts included in the owner snapshot.
		sphere["top_words"] = nil
		p.UnavailableComponents = append(p.UnavailableComponents, "sphere.private_text_aggregation_redacted")
	}
	p.Data = map[string]any{"passport": s.Passport, "sphere": sphere, "messager": messager}
	return p, nil
}
