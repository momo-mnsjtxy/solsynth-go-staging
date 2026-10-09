// SPDX-License-Identifier: AGPL-3.0-only
package notificationpg

import (
	"context"
	"solsynth.local/go-migration/ports/notifications"
	"sort"
)

func (r *Repository) WithFriendEvents(p notifications.AudienceEventProjector) *Repository {
	r.friends = p
	return r
}

// ProjectFriends creates stream markers only. It cannot write inbox records,
// delivery attempts, contact users or call an external notification provider.
func (r *Repository) ProjectFriends(ctx context.Context, take int) (int, error) {
	if r == nil || r.friends == nil {
		return 0, nil
	}
	if take < 1 || take > 100 {
		return 0, notifications.ErrInvalid
	}
	pending, e := r.friends.PendingAudienceEvents(ctx, take)
	if e != nil {
		return 0, e
	}
	count := 0
	for _, v := range pending {
		e = func() error {
			tx, e := r.begin(ctx)
			if e != nil {
				return e
			}
			defer tx.Rollback()
			ids := []string{v.Owner, v.Recipient}
			sort.Strings(ids)
			active := true
			for _, id := range ids {
				ok, x := lockAccount(ctx, tx, id)
				if x != nil {
					return x
				}
				active = active && ok
			}
			ready, e := r.friends.LockAudienceEvent(ctx, tx, v.Recipient, v.ID)
			if e != nil || !ready {
				return e
			}
			appended := false
			if active {
				event, allow, e := r.friends.ProjectAudienceEvent(ctx, tx, v.Recipient, v.ID)
				if e != nil {
					return e
				}
				if allow {
					appended = true
					if e = appendEvent(ctx, tx, v.Recipient, r.app, "friend:"+v.ID, event.Type, "friend", &v.ID, []byte(`{}`), event.CreatedAt); e != nil {
						return e
					}
				}
			}
			if appended {
				_, current, checkErr := r.friends.ProjectAudienceEvent(ctx, tx, v.Recipient, v.ID)
				if checkErr != nil {
					return checkErr
				}
				if !current {
					return notifications.ErrUnavailable
				}
			}
			if e = r.friends.CompleteAudienceEvent(ctx, tx, v.Recipient, v.ID); e != nil {
				return e
			}
			if e = tx.Commit(); e != nil {
				return e
			}
			count++
			return nil
		}()
		if e != nil {
			return count, e
		}
	}
	return count, nil
}

// RevalidateEvent observes current authorization immediately before transport
// emission, using a new bounded transaction and the original reader proof.
// It never holds SQL locks while a WebSocket/SSE writer can block.
func (r *Repository) RevalidateEvent(ctx context.Context, actor, app string, event notifications.Event) (notifications.Event, bool, error) {
	if event.Sequence < 1 {
		return notifications.Event{}, false, notifications.ErrInvalid
	}
	p, e := r.Replay(ctx, actor, app, event.Sequence-1, 1)
	if e != nil {
		return notifications.Event{}, false, e
	}
	if len(p.Events) == 1 && p.Events[0].Sequence == event.Sequence {
		return p.Events[0], true, nil
	}
	return notifications.Event{}, false, nil
}
