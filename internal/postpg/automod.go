// SPDX-License-Identifier: AGPL-3.0-only
package postpg

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"solsynth.local/go-migration/ports/passport/social"
	a "solsynth.local/go-migration/ports/sphere/automod"
	"solsynth.local/go-migration/ports/sphere/posts"
	"time"
)

// WithAutomod records local-rule decisions atomically on canonical writes.
// It never enables a classifier or changes draft/private visibility. Runtime
// enforcement follows the authored ranked-feed contract, including Latest bypass.
func (r *Repository) WithAutomod(review bool) *Repository {
	r.automod = true
	r.automodReview = review
	return r
}

type AutomodSnapshot struct {
	Generation int64
	Rules      []a.Rule
	At         time.Time
}

// ReadAutomodSnapshot reads rules AFTER the optional generation lock. Rule CRUD
// holds that row FOR UPDATE. Writers call this only after canonical post locks;
// read-only repeatable-read feed transactions use their existing snapshot.
func ReadAutomodSnapshot(ctx context.Context, tx *sql.Tx, lock bool) (AutomodSnapshot, error) {
	s := AutomodSnapshot{Rules: []a.Rule{}}
	if tx == nil {
		return s, posts.ErrUnavailable
	}
	q := `SELECT generation FROM owned_automod_state WHERE singleton`
	if lock {
		q += ` FOR SHARE`
	}
	if e := tx.QueryRowContext(ctx, q).Scan(&s.Generation); e != nil {
		return s, e
	}
	if e := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&s.At); e != nil {
		return s, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT id::text,name,description,type,default_action,pattern,is_regex,derank_weight,is_enabled,priority,expires_at,created_at,updated_at FROM owned_automod_rules WHERE deleted_at IS NULL AND is_enabled AND(expires_at IS NULL OR expires_at>$1) ORDER BY priority,id LIMIT 1001`, s.At)
	if e != nil {
		return s, e
	}
	defer rows.Close()
	for rows.Next() {
		var r a.Rule
		if e = rows.Scan(&r.ID, &r.Name, &r.Description, &r.Type, &r.DefaultAction, &r.Pattern, &r.IsRegex, &r.DerankWeight, &r.IsEnabled, &r.Priority, &r.ExpiresAt, &r.CreatedAt, &r.UpdatedAt); e != nil {
			return s, e
		}
		s.Rules = append(s.Rules, r)
	}
	if len(s.Rules) > a.MaxRules {
		return s, posts.ErrUnavailable
	}
	return s, rows.Err()
}

func LoadAutomodInTransaction(ctx context.Context, tx *sql.Tx, id string) (*posts.AutomodDecision, error) {
	var b []byte
	if e := tx.QueryRowContext(ctx, `SELECT automod_decision FROM owned_text_posts WHERE id=$1`, id).Scan(&b); e != nil {
		return nil, e
	}
	if len(b) == 0 {
		return nil, nil
	}
	var d posts.AutomodDecision
	if e := json.Unmarshal(b, &d); e != nil {
		return nil, e
	}
	return &d, nil
}

func automodContentHash(p posts.Post) string {
	b, _ := json.Marshal([]any{p.Title, p.Description, p.Content})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// MarkAutomodFreshness annotates historical evidence, without running a matcher
// or making oversized imported content unreadable to its current editor.
func MarkAutomodFreshness(ctx context.Context, tx *sql.Tx, p *posts.Post) error {
	if p.Automod == nil {
		return nil
	}
	s, e := ReadAutomodSnapshot(ctx, tx, false)
	if e != nil {
		return e
	}
	_, fingerprint := a.RuleSnapshot(s.Rules, s.At)
	p.Automod.Stale = p.Automod.Generation != s.Generation || p.Automod.RulesFingerprint != fingerprint || p.Automod.ContentHash != automodContentHash(*p)
	return nil
}

func SameAutomodInput(a, b *posts.AutomodDecision) bool {
	return a != nil && b != nil && a.Generation == b.Generation && a.RulesFingerprint == b.RulesFingerprint && a.ContentHash == b.ContentHash
}
func ComputeAutomod(ctx context.Context, s AutomodSnapshot, p posts.Post, prior *posts.AutomodDecision, ignoreReview bool) (*posts.AutomodDecision, error) {
	text := a.Content(p.Title, p.Description, p.Content)
	v, e := a.Evaluate(ctx, s.Rules, text, s.At)
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, posts.ErrUnavailable
	}
	// Include exact field boundaries so text moved between fields cannot carry
	// an old review to a different content revision.
	h := automodContentHash(p)
	d := &posts.AutomodDecision{EvaluationWork: v.Work, Generation: s.Generation, RulesFingerprint: v.Fingerprint, ContentHash: h, Outcome: v.Outcome, Hidden: v.Hidden, Penalty: v.Penalty, Flagged: v.Flagged, Matches: []posts.AutomodMatch{}, EvaluatedAt: s.At, Classifier: "unavailable"}
	for _, m := range v.Matches {
		d.Matches = append(d.Matches, posts.AutomodMatch{RuleID: m.RuleID, RuleName: m.RuleName, Type: m.Type, Action: m.Action, DerankWeight: m.DerankWeight, MatchedText: m.MatchedText})
	}
	if !ignoreReview && SameAutomodInput(prior, d) && prior.Review != nil {
		d.Review = prior.Review
		d.Outcome = "dismissed"
		d.Hidden = false
		d.Penalty = 0
		d.Flagged = false
	}
	return d, nil
}

func SaveAutomodInTransaction(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post, d *posts.AutomodDecision, transition string) error {
	var revision int64
	// Caller already owns this canonical post lock, before the generation lock.
	if e := tx.QueryRowContext(ctx, `SELECT automod_revision FROM owned_text_posts WHERE id=$1`, p.ID).Scan(&revision); e != nil {
		return e
	}
	d.Revision = revision + 1
	b, e := json.Marshal(d)
	if e != nil || len(b) > 2<<20 {
		return posts.ErrUnavailable
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_text_posts SET automod_revision=$2,automod_decision=$3::jsonb WHERE id=$1`, p.ID, d.Revision, b); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_post_automod_events(post_id,revision,actor_id,transition,decision,occurred_at)VALUES($1,$2,$3,$4,$5::jsonb,$6)`, p.ID, d.Revision, actor, transition, b, d.EvaluatedAt); e != nil {
		return e
	}
	p.Automod = d
	return nil
}

// AutomodInTransaction is the scheduling seam. The caller must already own the
// canonical actor/realm/publisher/post locks and current write authorization,
// and must recheck authority AFTER this method's possible generation-row wait.
// Call immediately before publication in the SAME transaction. Repeated calls
// for unchanged text/rules are idempotent; no scheduler approval is synthesized.
func (r *Repository) AutomodInTransaction(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post, transition string) error {
	if r == nil || !r.automod {
		return posts.ErrUnavailable
	}
	if e := evaluateAutomodInTransaction(ctx, tx, actor, p, transition, r.automodReview); e != nil {
		return e
	}
	posts.RedactAutomodForEditor(p)
	return nil
}
func evaluateAutomodInTransaction(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post, transition string, review bool) error {
	prior, e := LoadAutomodInTransaction(ctx, tx, p.ID)
	if e != nil {
		return e
	}
	if p.DeletedAt != nil {
		if prior != nil && prior.Outcome == "deleted" {
			p.Automod = prior
			return nil
		}
		d := &posts.AutomodDecision{Outcome: "deleted", Matches: []posts.AutomodMatch{}, EvaluatedAt: time.Now().UTC(), Classifier: "unavailable"}
		// Deletion must remain possible even for content exceeding evaluator bounds.
		return SaveAutomodInTransaction(ctx, tx, actor, p, d, transition)
	}
	s, e := ReadAutomodSnapshot(ctx, tx, true)
	if e != nil {
		return e
	}
	d, e := ComputeAutomod(ctx, s, *p, prior, !review)
	if e != nil {
		return e
	}
	if SameAutomodInput(prior, d) && prior.Outcome == d.Outcome {
		p.Automod = prior
		return nil
	}
	return SaveAutomodInTransaction(ctx, tx, actor, p, d, transition)
}
func (r *Repository) evaluateAutomod(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post, transition string) error {
	if !r.automod {
		return nil
	}
	if e := r.AutomodInTransaction(ctx, tx, actor, p, transition); e != nil {
		return e
	}
	if p.DeletedAt != nil {
		return nil
	}
	// Reobserve current editor/owner/realm gates after a rule-generation wait.
	// No new lock or policy identity is introduced here.
	var allowed bool
	if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1`+base+` AND p.id=$2 AND `+EditorPredicateWithVisibility(r.visibility)+`)`, actor, p.ID).Scan(&allowed); e != nil {
		return e
	}
	if !allowed {
		return posts.ErrForbidden
	}
	if p.Publisher.Type == 1 && p.Publisher.RealmID != nil {
		key := social.CanPost
		if p.RepliedPostID != nil {
			key = social.CanComment
		}
		if e := RequireRealmAction(ctx, tx, r.visibility, actor, *p.Publisher.RealmID, key); e != nil {
			return e
		}
	}
	return r.authorizeRealms(ctx, tx, actor, p.Publisher.Type, p.RealmID, posts.Patch{}, false, p.RepliedPostID != nil)
}
func (r *Repository) loadAutomod(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post) error {
	p.Automod = nil
	if !r.automod || actor == "" {
		return nil
	}
	var editor bool
	if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1`+base+` AND p.id=$2 AND `+EditorPredicateWithVisibility(r.visibility)+`)`, actor, p.ID).Scan(&editor); e != nil {
		return e
	}
	if !editor {
		return nil
	}
	d, e := LoadAutomodInTransaction(ctx, tx, p.ID)
	p.Automod = d
	if e != nil {
		return e
	}
	if e := MarkAutomodFreshness(ctx, tx, p); e != nil {
		return e
	}
	posts.RedactAutomodForEditor(p)
	return nil
}
