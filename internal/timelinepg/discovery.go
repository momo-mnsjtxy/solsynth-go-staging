// SPDX-License-Identifier: AGPL-3.0-only
package timelinepg

import (
	"context"
	"database/sql"
	"math"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/timeline"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

func (r *Repository) interestMap(ctx context.Context, tx *sql.Tx, actor string) (map[string]timeline.Interest, error) {
	out := map[string]timeline.Interest{}
	if actor == "" {
		return out, nil
	}
	rows, e := tx.QueryContext(ctx, `SELECT kind,reference_id::text,score,interaction_count,last_interacted_at,last_signal_type FROM owned_timeline_interests WHERE account_id=$1 ORDER BY kind,reference_id`, actor)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var v timeline.Interest
		if e = rows.Scan(&v.Kind, &v.ReferenceID, &v.Score, &v.InteractionCount, &v.LastInteractedAt, &v.LastSignalType); e != nil {
			return nil, e
		}
		out[v.Kind+":"+v.ReferenceID] = v
	}
	return out, rows.Err()
}
func interestKind(kind string) bool {
	return kind == "publisher" || kind == "collection" || kind == "tag" || kind == "category"
}
func preferenceKind(kind string) bool {
	return kind == "publisher" || kind == "realm" || kind == "account"
}
func textValid(v string, limit int) bool {
	return len(v) <= limit && utf8.ValidString(v) && !strings.ContainsRune(v, 0)
}
func (r *Repository) reference(ctx context.Context, tx *sql.Tx, actor, kind, ref string) (string, error) {
	if !authority.ValidID(ref) {
		return "", timeline.ErrInvalid
	}
	var label string
	var e error
	switch kind {
	case "publisher":
		if _, _, e = r.publisher(ctx, tx, actor, ref, false); e != nil {
			return "", e
		}
		e = tx.QueryRowContext(ctx, `SELECT nick FROM owned_publishers WHERE id=$1`, ref).Scan(&label)
	case "tag", "category":
		table := "post_tags"
		if kind == "category" {
			table = "post_categories"
		}
		e = tx.QueryRowContext(ctx, `SELECT COALESCE(name,slug) FROM owned_taxonomy.`+table+` WHERE id=$1 AND deleted_at IS NULL`, ref).Scan(&label)
	case "collection":
		var pub string
		e = tx.QueryRowContext(ctx, `SELECT publisher_id::text,COALESCE(name,slug) FROM owned_post_collections WHERE id=$1 AND deleted_at IS NULL`, ref).Scan(&pub, &label)
		if e == nil {
			_, _, e = r.publisher(ctx, tx, actor, pub, false)
		}
	case "post":
		p, err := r.post(ctx, tx, actor, ref)
		e = err
		label = p.ID
	case "account":
		e = tx.QueryRowContext(ctx, `SELECT nick FROM owned_authority_accounts a WHERE id=$2 AND NOT disabled AND NOT EXISTS(SELECT 1 FROM owned_social_relationships s WHERE s.status=-100 AND((s.account_id=$1 AND s.related_id=a.id)OR(s.related_id=$1 AND s.account_id=a.id)))`, actor, ref).Scan(&label)
	case "realm":
		e = tx.QueryRowContext(ctx, `SELECT name FROM owned_realms r JOIN owned_authority_accounts a ON a.id=r.account_id WHERE r.id=$2 AND r.deleted_at IS NULL AND NOT a.disabled AND(r.is_public OR EXISTS(SELECT 1 FROM owned_realm_members m WHERE m.realm_id=r.id AND m.account_id=$1 AND m.joined_at IS NOT NULL AND m.leave_at IS NULL))`, actor, ref).Scan(&label)
	default:
		return "", timeline.ErrInvalid
	}
	return label, missing(e)
}
func (r *Repository) adjust(ctx context.Context, tx *sql.Tx, actor string, v timeline.WeightRequest, now time.Time) (timeline.Interest, error) {
	var out timeline.Interest
	v.Kind = strings.ToLower(strings.TrimSpace(v.Kind))
	if !interestKind(v.Kind) || !authority.ValidID(v.ReferenceID) || math.IsNaN(v.ScoreDelta) || math.IsInf(v.ScoreDelta, 0) || v.ScoreDelta < -100 || v.ScoreDelta > 100 || v.InteractionCount < 0 || v.InteractionCount > 1000 || !textValid(v.SignalType, 128) {
		return out, timeline.ErrInvalid
	}
	if v.InteractionCount == 0 {
		v.InteractionCount = 1
	}
	if v.SignalType == "" {
		v.SignalType = "manual:" + v.Kind
	}
	label, e := r.reference(ctx, tx, actor, v.Kind, v.ReferenceID)
	if e != nil {
		return out, e
	}
	out.Kind = v.Kind
	out.ReferenceID = v.ReferenceID
	out.Label = label
	e = tx.QueryRowContext(ctx, `INSERT INTO owned_timeline_interests(account_id,kind,reference_id,score,interaction_count,last_interacted_at,last_signal_type)VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(account_id,kind,reference_id)DO UPDATE SET score=greatest(-100,least(100,owned_timeline_interests.score+EXCLUDED.score)),interaction_count=owned_timeline_interests.interaction_count+EXCLUDED.interaction_count,last_interacted_at=EXCLUDED.last_interacted_at,last_signal_type=EXCLUDED.last_signal_type RETURNING score,interaction_count,last_interacted_at,last_signal_type`, actor, v.Kind, v.ReferenceID, v.ScoreDelta, v.InteractionCount, now, v.SignalType).Scan(&out.Score, &out.InteractionCount, &out.LastInteractedAt, &out.LastSignalType)
	return out, e
}
func (r *Repository) AdjustWeight(ctx context.Context, actor string, v timeline.WeightRequest) (timeline.Interest, error) {
	var out timeline.Interest
	e := r.write(ctx, actor, "timelines.weights.manage", func(tx *sql.Tx, now time.Time) error {
		var e error
		out, e = r.adjust(ctx, tx, actor, v, now)
		if e != nil {
			return e
		}
		return audit(ctx, tx, actor, "discovery.weight", v.ReferenceID, now)
	})
	return out, e
}
func scanPreference(s interface{ Scan(...any) error }) (timeline.Preference, error) {
	var v timeline.Preference
	e := s.Scan(&v.ID, &v.AccountID, &v.Kind, &v.ReferenceID, &v.Reason, &v.AppliedAt, &v.CreatedAt, &v.UpdatedAt)
	return v, missing(e)
}

const preferenceColumns = `id::text,account_id::text,kind,reference_id::text,reason,applied_at,created_at,updated_at`

func (r *Repository) suppress(ctx context.Context, tx *sql.Tx, actor string, v timeline.PreferenceRequest, now time.Time) (timeline.Preference, error) {
	var out timeline.Preference
	v.Kind = strings.ToLower(strings.TrimSpace(v.Kind))
	if !preferenceKind(v.Kind) || v.Reason != nil && !textValid(*v.Reason, 1024) {
		return out, timeline.ErrInvalid
	}
	if _, e := r.reference(ctx, tx, actor, v.Kind, v.ReferenceID); e != nil {
		return out, e
	}
	uid, e := id()
	if e != nil {
		return out, e
	}
	return scanPreference(tx.QueryRowContext(ctx, `INSERT INTO owned_timeline_preferences(id,account_id,kind,reference_id,reason,applied_at,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$6,$6,$6) ON CONFLICT(account_id,kind,reference_id)DO UPDATE SET reason=EXCLUDED.reason,applied_at=EXCLUDED.applied_at,updated_at=EXCLUDED.updated_at RETURNING `+preferenceColumns, uid, actor, v.Kind, v.ReferenceID, v.Reason, now))
}
func (r *Repository) Suppress(ctx context.Context, actor string, v timeline.PreferenceRequest) (timeline.Preference, error) {
	var out timeline.Preference
	e := r.write(ctx, actor, "timelines.feedback", func(tx *sql.Tx, now time.Time) error {
		var e error
		out, e = r.suppress(ctx, tx, actor, v, now)
		if e != nil {
			return e
		}
		return audit(ctx, tx, actor, "discovery.suppress", v.ReferenceID, now)
	})
	return out, e
}
func (r *Repository) Unsuppress(ctx context.Context, actor, kind, ref string) error {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if !preferenceKind(kind) || !authority.ValidID(ref) {
		return timeline.ErrInvalid
	}
	return r.write(ctx, actor, "timelines.feedback", func(tx *sql.Tx, now time.Time) error {
		result, e := tx.ExecContext(ctx, `DELETE FROM owned_timeline_preferences WHERE account_id=$1 AND kind=$2 AND reference_id=$3`, actor, kind, ref)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if n == 0 {
			return posts.ErrNotFound
		}
		return audit(ctx, tx, actor, "discovery.unsuppress", ref, now)
	})
}
func (r *Repository) Feedback(ctx context.Context, actor string, v timeline.FeedbackRequest) (timeline.FeedbackResult, error) {
	out := timeline.FeedbackResult{UpdatedProfiles: []timeline.Interest{}}
	v.Kind = strings.ToLower(strings.TrimSpace(v.Kind))
	v.Feedback = strings.ToLower(strings.TrimSpace(v.Feedback))
	score := 0.
	switch v.Feedback {
	case "positive", "good", "like":
		score = 4
	case "negative", "bad", "dislike":
		score = -30
	default:
		return out, timeline.ErrInvalid
	}
	if !interestKind(v.Kind) && v.Kind != "post" || v.Reason != nil && !textValid(*v.Reason, 1024) {
		return out, timeline.ErrInvalid
	}
	e := r.write(ctx, actor, "timelines.feedback", func(tx *sql.Tx, now time.Time) error {
		adjustments := []timeline.WeightRequest{{Kind: v.Kind, ReferenceID: v.ReferenceID, ScoreDelta: score}}
		if v.Kind == "post" {
			p, e := r.post(ctx, tx, actor, v.ReferenceID)
			if e != nil {
				return e
			}
			if p.DraftedAt != nil {
				return posts.ErrNotFound
			}
			adjustments = []timeline.WeightRequest{{Kind: "publisher", ReferenceID: p.PublisherID, ScoreDelta: score}}
			for _, t := range p.Tags {
				adjustments = append(adjustments, timeline.WeightRequest{Kind: "tag", ReferenceID: t.ID, ScoreDelta: score * .75 / float64(len(p.Tags))})
			}
			for _, c := range p.Categories {
				adjustments = append(adjustments, timeline.WeightRequest{Kind: "category", ReferenceID: c.ID, ScoreDelta: score / float64(len(p.Categories))})
			}
			for _, c := range p.PublisherCollections {
				adjustments = append(adjustments, timeline.WeightRequest{Kind: "collection", ReferenceID: c.ID, ScoreDelta: score * .65 / float64(len(p.PublisherCollections))})
			}
		}
		for _, a := range adjustments {
			a.SignalType = "feedback:" + v.Kind + ":" + v.Feedback
			i, e := r.adjust(ctx, tx, actor, a, now)
			if e != nil {
				return e
			}
			out.UpdatedProfiles = append(out.UpdatedProfiles, i)
		}
		if v.Suppress && score < 0 && v.Kind == "publisher" {
			p, e := r.suppress(ctx, tx, actor, timeline.PreferenceRequest{Kind: v.Kind, ReferenceID: v.ReferenceID, Reason: v.Reason}, now)
			if e != nil {
				return e
			}
			out.Preference = &p
		}
		return audit(ctx, tx, actor, "discovery.feedback", v.ReferenceID, now)
	})
	return out, e
}
func (r *Repository) ResetInterests(ctx context.Context, actor string) (int64, error) {
	var count int64
	e := r.write(ctx, actor, "timelines.reset", func(tx *sql.Tx, now time.Time) error {
		if _, e := tx.ExecContext(ctx, `DELETE FROM owned_timeline_interests WHERE account_id=$1`, actor); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, `SELECT 'publisher',publisher_id::text FROM owned_publisher_follows WHERE account_id=$1 AND state='accepted' AND ended_at IS NULL UNION SELECT 'tag',tag_id::text FROM owned_taxonomy.post_category_subscriptions WHERE account_id=$1 AND deleted_at IS NULL AND tag_id IS NOT NULL UNION SELECT 'category',category_id::text FROM owned_taxonomy.post_category_subscriptions WHERE account_id=$1 AND deleted_at IS NULL AND category_id IS NOT NULL`, actor)
		if e != nil {
			return e
		}
		adjustments := []timeline.WeightRequest{}
		for rows.Next() {
			a := timeline.WeightRequest{ScoreDelta: 4, SignalType: "reset"}
			if e = rows.Scan(&a.Kind, &a.ReferenceID); e != nil {
				rows.Close()
				return e
			}
			adjustments = append(adjustments, a)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, a := range adjustments {
			if _, e = r.reference(ctx, tx, actor, a.Kind, a.ReferenceID); e != nil {
				if isNotFound(e) {
					continue
				}
				return e
			}
			if _, e = r.adjust(ctx, tx, actor, a, now); e != nil {
				return e
			}
			count++
		}
		return audit(ctx, tx, actor, "discovery.reset", "", now)
	})
	return count, e
}
func (r *Repository) Discovery(ctx context.Context, actor string) (timeline.Profile, error) {
	now := r.now().UTC()
	out := timeline.Profile{GeneratedAt: now, Interests: []timeline.Interest{}, SuggestedPublishers: []timeline.Suggestion{}, SuggestedAccounts: []timeline.Suggestion{}, SuggestedRealms: []timeline.Suggestion{}, Suppressed: []timeline.Suggestion{}}
	tx, e := r.read(ctx, actor, true)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	interests, e := r.interestMap(ctx, tx, actor)
	if e != nil {
		return out, e
	}
	di := sphere.DiscoveryInterests{Tags: map[string]float64{}, Categories: map[string]float64{}, Collections: map[string]float64{}, Publishers: map[string]float64{}}
	for _, v := range interests {
		label, err := r.reference(ctx, tx, actor, v.Kind, v.ReferenceID)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return out, err
		}
		v.Label = label
		v.Score = sphere.DecayedInterestScore(v.Score, &v.LastInteractedAt, now)
		out.Interests = append(out.Interests, v)
		switch v.Kind {
		case "tag":
			di.Tags[v.ReferenceID] = v.Score
		case "category":
			di.Categories[v.ReferenceID] = v.Score
		case "publisher":
			di.Publishers[v.ReferenceID] = v.Score
		case "collection":
			di.Collections[v.ReferenceID] = v.Score
		}
	}
	sort.Slice(out.Interests, func(i, j int) bool {
		if out.Interests[i].Score == out.Interests[j].Score {
			return out.Interests[i].ReferenceID < out.Interests[j].ReferenceID
		}
		return out.Interests[i].Score > out.Interests[j].Score
	})
	if len(out.Interests) > 20 {
		out.Interests = out.Interests[:20]
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+preferenceColumns+` FROM owned_timeline_preferences WHERE account_id=$1 ORDER BY kind,reference_id`, actor)
	if e != nil {
		return out, e
	}
	hiddenPub := map[string]bool{}
	hiddenAccount := map[string]bool{}
	for rows.Next() {
		v, e := scanPreference(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		reasons := []string{}
		if v.Reason != nil {
			reasons = append(reasons, *v.Reason)
		}
		out.Suppressed = append(out.Suppressed, timeline.Suggestion{Kind: v.Kind, ReferenceID: v.ReferenceID, Label: v.ReferenceID, Reasons: reasons, Data: map[string]string{"id": v.ReferenceID}})
		if v.Kind == "publisher" {
			hiddenPub[v.ReferenceID] = true
		}
		if v.Kind == "account" {
			hiddenAccount[v.ReferenceID] = true
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	in := sphere.DiscoveryInputs{Now: now, Interests: di, Publishers: map[string]sphere.DiscoveryPublisher{}, OwnPublishers: map[string]bool{}, SubscribedPublishers: map[string]bool{}, HiddenPublishers: hiddenPub, HiddenAccounts: hiddenAccount}
	rows, e = tx.QueryContext(ctx, `SELECT u.id::text,EXISTS(SELECT 1 FROM owned_publisher_members m WHERE m.publisher_id=u.id AND m.account_id=$1 AND m.joined_at IS NOT NULL AND m.revoked_at IS NULL),EXISTS(SELECT 1 FROM owned_publisher_follows f WHERE f.publisher_id=u.id AND f.account_id=$1 AND f.state='accepted' AND f.ended_at IS NULL) FROM owned_publishers u WHERE u.deleted_at IS NULL`, actor)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var pid string
		var own, sub bool
		if e = rows.Scan(&pid, &own, &sub); e != nil {
			rows.Close()
			return out, e
		}
		in.OwnPublishers[pid] = own
		in.SubscribedPublishers[pid] = sub
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	where := r.base() + ` AND p.visibility=0 AND p.published_at>=$2 AND NOT EXISTS(SELECT 1 FROM owned_post_relations l WHERE l.post_id=p.id AND(l.replied_post_id IS NOT NULL OR l.chained_post_id IS NOT NULL))`
	candidates, _, e := r.selectPosts(ctx, tx, actor, where, []any{actor, now.Add(-45 * 24 * time.Hour)}, `p.published_at DESC,p.id DESC`, 0, 48)
	if e != nil {
		return out, e
	}
	dp := []sphere.DiscoveryPost{}
	byPublisher := map[string]posts.Post{}
	for _, p := range candidates {
		v := sphere.DiscoveryPost{Post: sphere.Post{ID: p.ID, PublisherID: p.PublisherID, CreatedAt: p.CreatedAt, PublishedAt: p.PublishedAt}, RepliesCount: int(p.RepliesCount)}
		if p.Type == posts.Article {
			v.Type = "Article"
		}
		if e = tx.QueryRowContext(ctx, `SELECT COALESCE(sum(CASE WHEN attitude=0 THEN 1 WHEN attitude=2 THEN -1 ELSE 0 END),0) FROM owned_post_reactions r JOIN owned_authority_accounts a ON a.id=r.account_id WHERE r.post_id=$1 AND NOT a.disabled`, p.ID).Scan(&v.ReactionScore); e != nil {
			return out, e
		}
		for _, t := range p.Tags {
			v.Tags = append(v.Tags, sphere.DiscoveryLabel{ID: t.ID, Name: t.Name, Slug: t.Slug})
		}
		for _, c := range p.Categories {
			v.Categories = append(v.Categories, sphere.DiscoveryLabel{ID: c.ID, Name: c.Name, Slug: c.Slug})
		}
		for _, c := range p.PublisherCollections {
			v.CollectionIDs = append(v.CollectionIDs, c.ID)
		}
		dp = append(dp, v)
		owner := ""
		if p.Publisher.AccountID != nil {
			owner = *p.Publisher.AccountID
		}
		in.Publishers[p.PublisherID] = sphere.DiscoveryPublisher{AccountID: owner}
		byPublisher[p.PublisherID] = p
	}
	selected := sphere.SelectDiscoveryTargets(dp, in)
	for _, s := range selected.Publishers {
		p := byPublisher[s.ReferenceID].Publisher
		out.SuggestedPublishers = append(out.SuggestedPublishers, timeline.Suggestion{Kind: "publisher", ReferenceID: p.ID, Label: p.Nick, Score: s.Score, Reasons: s.Reasons, Data: p})
		if len(out.SuggestedPublishers) == 3 {
			break
		}
	}
	return out, tx.Commit()
}
