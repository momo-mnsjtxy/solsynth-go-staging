// SPDX-License-Identifier: AGPL-3.0-only
package postembedpg

import (
	"context"
	"database/sql"
	_ "embed"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/ports/sphere/posts"
)

// These are read predicates against canonical lifecycle stores, never copied
// grants. $1 is the viewer, $2 the original target, $3 the source publisher,
// $4 is the source post for surveys or the current canonical editor decision
// for streams. Only the latter two are relevant to publisher references.
//
//go:embed queries/survey.sql
var surveySQL string

//go:embed queries/live_stream.sql
var live_streamSQL string

//go:embed queries/notable_day.sql
var notable_daySQL string

//go:embed queries/fund.sql
var fundSQL string

//go:embed queries/calendar_event.sql
var calendar_eventSQL string

//go:embed queries/meet.sql
var meetSQL string

func (r *Repository) visible(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post, x posts.Embed) (bool, error) {
	if x.Type == "location" {
		return true, nil
	}
	var query string
	args := []any{actor, x.ID}
	switch x.Type {
	case "survey":
		// A bound survey cannot transfer the audience of another canonical post.
		query = surveySQL
		args = append(args, p.PublisherID, p.ID)
	case "live_stream":
		query = live_streamSQL
		// A retained publisher role is not current editor authority after leaving
		// an organization realm. Use the canonical membership observer, which
		// reads this transaction's snapshot and acquires no new scope locks.
		realms, _ := r.visibility.(accountpg.RealmProvider)
		editor, e := accountpg.New(r.db).WithRealms(realms).CanEditPublisherInTransaction(ctx, tx, actor, p.PublisherID)
		if e != nil {
			return false, e
		}
		args = append(args, p.PublisherID, editor)
	case "notable_day":
		query = notable_daySQL
	case "fund":
		query = fundSQL
	case "calendar_event":
		query = calendar_eventSQL
	case "meet":
		// Canonical meet discovery needs either participants/friends or the viewer's
		// explicit nearby origin. A post does not invent a viewer location.
		query = meetSQL
	default:
		return false, posts.ErrUnsupported
	}
	var ok bool
	e := tx.QueryRowContext(ctx, query, args...).Scan(&ok)
	return ok, e
}
