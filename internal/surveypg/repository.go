// SPDX-License-Identifier: AGPL-3.0-only
package surveypg

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"math"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/notifications"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/surveys"
	"sort"
	"strings"
	"time"
)

//go:embed schema.sql
var Schema string

type Repository struct {
	notificationApp string
	db              *sql.DB
	publishers      *accountpg.Repository
	guard           *authoritypg.TransactionGuard
	visibility      postpg.VisibilityProvider
	media           surveys.MediaResolver
	now             func() time.Time
}

func New(db *sql.DB) *Repository {
	return &Repository{notificationApp: notifications.DefaultApp, db: db, publishers: accountpg.New(db), now: time.Now}
}
func NewWithAuthority(db *sql.DB, g *authoritypg.TransactionGuard) *Repository {
	r := New(db)
	if g == nil {
		g = authoritypg.NewTransactionGuard(nil, nil)
	}
	r.guard = g
	return r
}
func (r *Repository) WithVisibility(v postpg.VisibilityProvider) *Repository {
	r.visibility = v
	realms, _ := v.(accountpg.RealmProvider)
	r.publishers.WithRealms(realms)
	return r
}
func (r *Repository) WithMedia(m surveys.MediaResolver) *Repository { r.media = m; return r }
func (r *Repository) BindAuthority(ctx context.Context, p authority.Principal) context.Context {
	if r.guard != nil {
		return r.guard.Bind(ctx, p)
	}
	return ctx
}

var _ surveys.Repository = (*Repository)(nil)

func uid() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func mapped(e error) error {
	if errors.Is(e, sql.ErrNoRows) {
		return surveys.ErrNotFound
	}
	var p *pgconn.PgError
	if errors.As(e, &p) && p.Code == "23505" {
		return surveys.ErrConflict
	}
	return e
}
func (r *Repository) begin(ctx context.Context, actor, permission string, required bool) (*sql.Tx, error) {
	if r == nil || r.db == nil {
		return nil, surveys.ErrUnavailable
	}
	if actor == "" && required || actor != "" && !authority.ValidID(actor) {
		return nil, surveys.ErrUnauthenticated
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return nil, e
	}
	if actor != "" {
		if r.guard != nil {
			if permission == "" {
				_, e = r.guard.Authenticate(ctx, r.db, tx, actor)
			} else {
				_, e = r.guard.Authorize(ctx, r.db, tx, actor, permission)
			}
			if e != nil {
				tx.Rollback()
				return nil, e
			}
		}
		var enabled bool
		e = tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, actor).Scan(&enabled)
		if e != nil || !enabled {
			tx.Rollback()
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return nil, e
			}
			return nil, surveys.ErrUnauthenticated
		}
	}
	return tx, nil
}

type record struct {
	s           surveys.Survey
	attachments []string
}

func legacyStats(v surveys.Results) map[string]map[string]int64 {
	out := map[string]map[string]int64{}
	for id, s := range v.Questions {
		m := map[string]int64{}
		for key, n := range s.Choices {
			m[key] = n
		}
		if s.Average != nil {
			m["rating"] = int64(math.RoundToEven(*s.Average))
		}
		out[id] = m
	}
	return out
}

func (r *Repository) load(ctx context.Context, tx *sql.Tx, actor, id string, edit bool, mutation ...bool) (record, error) {
	return r.loadWithPost(ctx, tx, actor, id, nil, edit, mutation...)
}
func (r *Repository) loadWithPost(ctx context.Context, tx *sql.Tx, actor, id string, newPost *string, edit bool, mutation ...bool) (record, error) {
	var x record
	if !authority.ValidID(id) {
		return x, surveys.ErrNotFound
	}
	var pub string
	var oldPost *string
	if e := tx.QueryRowContext(ctx, `SELECT publisher_id::text,post_id::text FROM owned_surveys WHERE id=$1 AND deleted_at IS NULL`, id).Scan(&pub, &oldPost); e != nil {
		return record{}, mapped(e)
	}
	_, e := r.lockPostScopes(ctx, tx, pub, edit, oldPost, newPost)
	if e != nil {
		return record{}, e
	}
	if edit {
		if _, e = r.publishers.RequirePostEditor(ctx, tx, actor, pub); e != nil {
			return record{}, e
		}
	}
	lock := " FOR SHARE OF s"
	if edit || len(mutation) > 0 && mutation[0] {
		lock = " FOR UPDATE OF s"
	}
	var questions, attachments []byte
	var verification []byte
	e = tx.QueryRowContext(ctx, `SELECT s.id::text,s.publisher_id::text,s.post_id::text,s.title,s.description,s.ended_at,s.is_anonymous,s.notify_subscribers,s.hide_results,s.status,s.questions,s.attachment_ids,s.created_at,s.updated_at,s.published_at,p.id::text,p.name,p.nick,p.bio,p.type,p.account_id::text,p.realm_id::text,p.created_at,p.updated_at,p.verification FROM owned_surveys s JOIN owned_publishers p ON p.id=s.publisher_id WHERE s.id=$1 AND s.deleted_at IS NULL AND p.deleted_at IS NULL AND (p.account_id IS NULL OR EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id=p.account_id AND NOT a.disabled))`+lock, id).Scan(&x.s.ID, &x.s.PublisherID, &x.s.PostID, &x.s.Title, &x.s.Description, &x.s.EndedAt, &x.s.Anonymous, &x.s.NotifySubscribers, &x.s.HideResults, &x.s.Status, &questions, &attachments, &x.s.CreatedAt, &x.s.UpdatedAt, &x.s.PublishedAt, &x.s.Publisher.ID, &x.s.Publisher.Name, &x.s.Publisher.Nick, &x.s.Publisher.Bio, &x.s.Publisher.Type, &x.s.Publisher.AccountID, &x.s.Publisher.RealmID, &x.s.Publisher.CreatedAt, &x.s.Publisher.UpdatedAt, &verification)
	if e != nil {
		return x, mapped(e)
	}
	x.s.Publisher.Verification, e = accounts.DecodeVerification(verification)
	if e != nil {
		return x, e
	}
	if x.s.PublisherID != pub || !postpg.SameOptionalID(x.s.PostID, oldPost) {
		return x, surveys.ErrConflict
	}
	visible, e := postpg.CanViewPublisher(ctx, tx, actor, pub, r.visibility)
	if e != nil {
		return x, e
	}
	if !visible {
		return x, surveys.ErrNotFound
	}
	if e = json.Unmarshal(questions, &x.s.Questions); e != nil {
		return x, e
	}
	if e = json.Unmarshal(attachments, &x.attachments); e != nil {
		return x, e
	}
	if !edit {
		allowed, e := r.role(ctx, tx, actor, pub, accounts.Editor)
		if e != nil {
			return x, e
		}
		if x.s.Status == surveys.Draft && !allowed {
			return x, surveys.ErrNotFound
		}
	}
	if r.visibility != nil && actor != "" && x.s.Publisher.AccountID != nil {
		blocked, e := r.visibility.IsBlockedInTransaction(ctx, tx, actor, *x.s.Publisher.AccountID)
		if e != nil {
			return x, e
		}
		if blocked {
			return x, surveys.ErrNotFound
		}
	}
	if x.s.PostID != nil {
		p, e := postpg.ReadWithVisibility(ctx, tx, actor, *x.s.PostID, r.visibility)
		if e != nil {
			return x, surveys.ErrNotFound
		}
		if p.PublisherID != pub {
			return x, surveys.ErrNotFound
		}
	}
	x.s.Attachments = []media.File{}
	x.s.Stats = map[string]map[string]int64{}
	for _, id := range x.attachments {
		if r.media == nil {
			return x, surveys.ErrUnavailable
		}
		f, e := r.media.ResolvePublicInTransaction(ctx, tx, actor, id)
		if errors.Is(e, media.ErrNotFound) {
			continue
		}
		if e != nil {
			return x, e
		}
		x.s.Attachments = append(x.s.Attachments, f)
	}
	return x, nil
}
func (r *Repository) role(ctx context.Context, tx *sql.Tx, actor, pub string, role accounts.Role) (bool, error) {
	if actor == "" {
		return false, nil
	}
	var ok bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id=$1 AND NOT a.disabled AND (a.is_superuser OR EXISTS(SELECT 1 FROM owned_publisher_members m JOIN owned_publishers u ON u.id=m.publisher_id WHERE m.account_id=a.id AND m.publisher_id=$2 AND m.role>=$3 AND m.joined_at IS NOT NULL AND m.revoked_at IS NULL AND `+postpg.PublisherMemberPredicate(r.visibility)+`)))`, actor, pub, role).Scan(&ok)
	return ok, e
}
func audit(ctx context.Context, tx *sql.Tx, actor, id, action string, now time.Time) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_survey_audit(actor_id,survey_id,action,occurred_at) VALUES($1,$2,$3,$4)`, actor, id, action, now)
	return e
}
func prepareQuestions(q []surveys.Question) ([]surveys.Question, error) {
	b, e := json.Marshal(q)
	if e != nil {
		return nil, e
	}
	var out []surveys.Question
	if e = json.Unmarshal(b, &out); e != nil {
		return nil, e
	}
	for i := range out {
		if out[i].ID == "" || out[i].ID == "00000000-0000-0000-0000-000000000000" {
			out[i].ID, e = uid()
			if e != nil {
				return nil, e
			}
		}
		if out[i].Options == nil {
			out[i].Options = []surveys.Option{}
		}
		for j := range out[i].Options {
			if out[i].Options[j].ID == "" || out[i].Options[j].ID == "00000000-0000-0000-0000-000000000000" {
				out[i].Options[j].ID, e = uid()
				if e != nil {
					return nil, e
				}
			}
		}
		sort.SliceStable(out[i].Options, func(a, b int) bool { return out[i].Options[a].Order < out[i].Options[b].Order })
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Order < out[b].Order })
	return out, nil
}
func (r *Repository) apply(ctx context.Context, tx *sql.Tx, actor string, x *record, p surveys.Patch) error {
	if p.NotifySubscribers != nil {
		x.s.NotifySubscribers = *p.NotifySubscribers
	}
	if p.Title != nil {
		x.s.Title = p.Title
	}
	if p.Description != nil {
		x.s.Description = p.Description
	}
	if p.ClearEndedAt && p.EndedAt != nil {
		return surveys.ErrInvalid
	}
	if p.ClearEndedAt {
		x.s.EndedAt = nil
	} else if p.EndedAt != nil {
		x.s.EndedAt = p.EndedAt
	}
	if p.Anonymous != nil {
		x.s.Anonymous = *p.Anonymous
	}
	if p.HideResults != nil {
		x.s.HideResults = *p.HideResults
	}
	if p.Questions != nil {
		q, e := prepareQuestions(*p.Questions)
		if e != nil {
			return e
		}
		x.s.Questions = q
	}
	if p.PostID != nil {
		if *p.PostID == "" {
			x.s.PostID = nil
		} else {
			post, e := postpg.ReadWithVisibility(ctx, tx, actor, *p.PostID, r.visibility)
			if e != nil || post.PublisherID != x.s.PublisherID {
				return surveys.ErrInvalid
			}
			x.s.PostID = p.PostID
		}
	}
	if p.Attachments != nil {
		if len(*p.Attachments) > 16 {
			return surveys.ErrInvalid
		}
		x.attachments = []string{}
		x.s.Attachments = []media.File{}
		seen := map[string]bool{}
		ids := append([]string{}, (*p.Attachments)...)
		sort.Strings(ids)
		resolved := map[string]media.File{}
		for _, id := range ids {
			if !authority.ValidID(id) || seen[id] {
				return surveys.ErrInvalid
			}
			seen[id] = true
			if r.media == nil {
				return surveys.ErrUnsupported
			}
			f, e := r.media.ResolvePublicInTransaction(ctx, tx, actor, id)
			if e != nil {
				return e
			}
			resolved[id] = f
		}
		for _, id := range *p.Attachments {
			x.attachments = append(x.attachments, id)
			x.s.Attachments = append(x.s.Attachments, resolved[id])
		}
	}
	return surveys.Validate(x.s)
}
func insert(ctx context.Context, tx *sql.Tx, x record) error {
	q, e := json.Marshal(x.s.Questions)
	if e != nil {
		return e
	}
	a, e := json.Marshal(x.attachments)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_surveys(id,publisher_id,post_id,title,description,ended_at,is_anonymous,hide_results,status,questions,attachment_ids,created_at,updated_at,published_at,notify_subscribers) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, x.s.ID, x.s.PublisherID, x.s.PostID, x.s.Title, x.s.Description, x.s.EndedAt, x.s.Anonymous, x.s.HideResults, x.s.Status, q, a, x.s.CreatedAt, x.s.UpdatedAt, x.s.PublishedAt, x.s.NotifySubscribers)
	return e
}
func (r *Repository) Create(ctx context.Context, actor, selector string, p surveys.Patch) (surveys.Survey, error) {
	tx, e := r.begin(ctx, actor, "surveys.create", true)
	if e != nil {
		return surveys.Survey{}, e
	}
	defer tx.Rollback()
	pubID, e := r.publishers.ObservePostPublisherID(ctx, tx, actor, selector)
	if e != nil {
		return surveys.Survey{}, e
	}
	if _, e = r.lockPostScopes(ctx, tx, pubID, true, p.PostID); e != nil {
		return surveys.Survey{}, e
	}
	actual, e := r.publishers.ObservePostPublisherID(ctx, tx, actor, selector)
	if e != nil {
		return surveys.Survey{}, e
	}
	if actual != pubID {
		return surveys.Survey{}, surveys.ErrConflict
	}
	pub, e := r.publishers.RequirePostEditor(ctx, tx, actor, pubID)
	if e != nil {
		return surveys.Survey{}, e
	}
	if pub.Type != 0 && (pub.Type != 1 || pub.RealmID == nil) {
		return surveys.Survey{}, surveys.ErrUnsupported
	}
	id, e := uid()
	if e != nil {
		return surveys.Survey{}, e
	}
	now := r.now().UTC()
	x := record{s: surveys.Survey{ID: id, PublisherID: pub.ID, Publisher: pub, Status: surveys.Draft, CreatedAt: now, UpdatedAt: now, Attachments: []media.File{}}, attachments: []string{}}
	if e = r.apply(ctx, tx, actor, &x, p); e != nil {
		return surveys.Survey{}, e
	}
	if e = insert(ctx, tx, x); e != nil {
		return surveys.Survey{}, mapped(e)
	}
	if x.s.NotifySubscribers {
		if _, e = ensureSubscription(ctx, tx, actor, id, now); e != nil {
			return surveys.Survey{}, e
		}
	}
	if e = r.recheck(ctx, tx, actor, "surveys.create"); e != nil {
		return surveys.Survey{}, e
	}
	if e = audit(ctx, tx, actor, id, "create", now); e != nil {
		return surveys.Survey{}, e
	}
	if e = tx.Commit(); e != nil {
		return surveys.Survey{}, e
	}
	return x.s, nil
}
func (r *Repository) Update(ctx context.Context, actor, id string, p surveys.Patch) (surveys.Survey, error) {
	tx, e := r.begin(ctx, actor, "surveys.update", true)
	if e != nil {
		return surveys.Survey{}, e
	}
	defer tx.Rollback()
	x, e := r.loadWithPost(ctx, tx, actor, id, p.PostID, true)
	if e != nil {
		return surveys.Survey{}, e
	}
	if x.s.Status != surveys.Draft {
		return surveys.Survey{}, surveys.ErrConflict
	}
	if e = r.apply(ctx, tx, actor, &x, p); e != nil {
		return surveys.Survey{}, e
	}
	x.s.UpdatedAt = r.now().UTC()
	q, _ := json.Marshal(x.s.Questions)
	a, _ := json.Marshal(x.attachments)
	_, e = tx.ExecContext(ctx, `UPDATE owned_surveys SET title=$2,description=$3,ended_at=$4,is_anonymous=$5,hide_results=$6,questions=$7,attachment_ids=$8,post_id=$9,updated_at=$10,notify_subscribers=$11 WHERE id=$1`, id, x.s.Title, x.s.Description, x.s.EndedAt, x.s.Anonymous, x.s.HideResults, q, a, x.s.PostID, x.s.UpdatedAt, x.s.NotifySubscribers)
	if e != nil {
		return surveys.Survey{}, e
	}
	if x.s.NotifySubscribers {
		if _, e = ensureSubscription(ctx, tx, actor, id, x.s.UpdatedAt); e != nil {
			return surveys.Survey{}, e
		}
	}
	if e = r.recheck(ctx, tx, actor, "surveys.update"); e != nil {
		return surveys.Survey{}, e
	}
	if e = audit(ctx, tx, actor, id, "update", x.s.UpdatedAt); e != nil {
		return surveys.Survey{}, e
	}
	if e = tx.Commit(); e != nil {
		return surveys.Survey{}, e
	}
	return x.s, nil
}
func (r *Repository) Transition(ctx context.Context, actor, id, action string) (surveys.Survey, error) {
	if action != "publish" && action != "archive" && action != "clone" {
		return surveys.Survey{}, surveys.ErrInvalid
	}
	tx, e := r.begin(ctx, actor, "surveys."+action, true)
	if e != nil {
		return surveys.Survey{}, e
	}
	defer tx.Rollback()
	x, e := r.load(ctx, tx, actor, id, true)
	if e != nil {
		return surveys.Survey{}, e
	}
	now := r.now().UTC()
	x.s.UpdatedAt = now
	switch action {
	case "publish":
		if x.s.PostID != nil {
			if _, err := postpg.ReadWithVisibility(ctx, tx, actor, *x.s.PostID, r.visibility); err != nil {
				return surveys.Survey{}, surveys.ErrNotFound
			}
		}
		if x.s.Status != surveys.Draft || x.s.EndedAt != nil && !x.s.EndedAt.After(now) {
			return surveys.Survey{}, surveys.ErrConflict
		}
		if e = surveys.Validate(x.s); e != nil {
			return surveys.Survey{}, e
		}
		x.s.Status = surveys.Published
		x.s.PublishedAt = &now
	case "archive":
		if x.s.Status != surveys.Published {
			return surveys.Survey{}, surveys.ErrConflict
		}
		x.s.Status = surveys.Archived
	case "clone":
		x.s.ID, e = uid()
		if e != nil {
			return surveys.Survey{}, e
		}
		x.s.Status = surveys.Draft
		x.s.PublishedAt = nil
		x.s.PostID = nil
		x.s.EndedAt = nil
		x.s.CreatedAt = now
		for i := range x.s.Questions {
			x.s.Questions[i].ID, e = uid()
			if e != nil {
				return surveys.Survey{}, e
			}
			for j := range x.s.Questions[i].Options {
				x.s.Questions[i].Options[j].ID, e = uid()
				if e != nil {
					return surveys.Survey{}, e
				}
			}
		}
		if e = insert(ctx, tx, x); e != nil {
			return surveys.Survey{}, e
		}
	}
	if action != "clone" {
		_, e = tx.ExecContext(ctx, `UPDATE owned_surveys SET status=$2,published_at=$3,updated_at=$4 WHERE id=$1`, id, x.s.Status, x.s.PublishedAt, now)
		if e != nil {
			return surveys.Survey{}, e
		}
	}
	if e = audit(ctx, tx, actor, x.s.ID, action, now); e != nil {
		return surveys.Survey{}, e
	}
	if e = tx.Commit(); e != nil {
		return surveys.Survey{}, e
	}
	return x.s, nil
}
func (r *Repository) Delete(ctx context.Context, actor, id string) error {
	tx, e := r.begin(ctx, actor, "surveys.delete", true)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = r.load(ctx, tx, actor, id, true); e != nil {
		return e
	}
	now := r.now().UTC()
	if _, e = tx.ExecContext(ctx, `UPDATE owned_surveys SET deleted_at=$2,updated_at=$2 WHERE id=$1`, id, now); e != nil {
		return e
	}
	if e = audit(ctx, tx, actor, id, "delete", now); e != nil {
		return e
	}
	return tx.Commit()
}
func scanAnswer(row interface{ Scan(...any) error }, anonymous bool) (surveys.Answer, error) {
	var a surveys.Answer
	var account string
	var raw []byte
	e := row.Scan(&a.ID, &a.SurveyID, &account, &raw, &a.CreatedAt, &a.UpdatedAt)
	if e != nil {
		return a, mapped(e)
	}
	if !anonymous {
		a.AccountID = &account
	}
	e = json.Unmarshal(raw, &a.Answer)
	return a, e
}

const answerFields = `id::text,survey_id::text,account_id::text,answer,created_at,updated_at`

func (r *Repository) Answer(ctx context.Context, actor, id string, v map[string]json.RawMessage) (surveys.Answer, error) {
	tx, e := r.begin(ctx, actor, "surveys.answer", true)
	if e != nil {
		return surveys.Answer{}, e
	}
	defer tx.Rollback()
	x, e := r.load(ctx, tx, actor, id, false, true)
	if e != nil {
		return surveys.Answer{}, e
	}
	now := r.now().UTC()
	if x.s.Status != surveys.Published || x.s.EndedAt != nil && !now.Before(*x.s.EndedAt) {
		return surveys.Answer{}, surveys.ErrConflict
	}
	if e = surveys.ValidateAnswer(x.s, v); e != nil {
		return surveys.Answer{}, e
	}
	raw, e := json.Marshal(v)
	if e != nil || len(raw) > 256<<10 {
		return surveys.Answer{}, surveys.ErrValidation
	}
	if e = r.recheck(ctx, tx, actor, "surveys.answer"); e != nil {
		return surveys.Answer{}, e
	}
	var unchanged bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_survey_answers WHERE survey_id=$1 AND account_id=$2 AND answer=$3::jsonb)`, id, actor, raw).Scan(&unchanged); e != nil {
		return surveys.Answer{}, e
	}
	if unchanged {
		a, e := scanAnswer(tx.QueryRowContext(ctx, `SELECT `+answerFields+` FROM owned_survey_answers WHERE survey_id=$1 AND account_id=$2`, id, actor), x.s.Anonymous)
		if e != nil {
			return surveys.Answer{}, e
		}
		return a, tx.Commit()
	}
	key, e := uid()
	if e != nil {
		return surveys.Answer{}, e
	}
	a, e := scanAnswer(tx.QueryRowContext(ctx, `INSERT INTO owned_survey_answers(id,survey_id,account_id,answer,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$5) ON CONFLICT(survey_id,account_id) DO UPDATE SET answer=EXCLUDED.answer,updated_at=EXCLUDED.updated_at,revision=owned_survey_answers.revision+1 RETURNING `+answerFields, key, id, actor, raw, now), x.s.Anonymous)
	if e != nil {
		return a, e
	}
	if x.s.NotifySubscribers {
		if e = enqueueAnswer(ctx, tx, actor, x.s, a.ID, r.notificationApp, now); e != nil {
			return surveys.Answer{}, e
		}
	}
	if e = r.recheck(ctx, tx, actor, "surveys.answer"); e != nil {
		return surveys.Answer{}, e
	}
	if e = audit(ctx, tx, actor, id, "answer", now); e != nil {
		return surveys.Answer{}, e
	}
	if e = tx.Commit(); e != nil {
		return surveys.Answer{}, e
	}
	return a, nil
}
func (r *Repository) Withdraw(ctx context.Context, actor, id string) error {
	tx, e := r.begin(ctx, actor, "surveys.answer", true)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = r.load(ctx, tx, actor, id, false, true); e != nil {
		return e
	}
	res, e := tx.ExecContext(ctx, `DELETE FROM owned_survey_answers WHERE survey_id=$1 AND account_id=$2`, id, actor)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n > 0 {
		if e = audit(ctx, tx, actor, id, "withdraw", r.now().UTC()); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (r *Repository) stats(ctx context.Context, tx *sql.Tx, x record) (surveys.Results, error) {
	out := surveys.Results{Questions: map[string]surveys.QuestionStats{}}
	if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_survey_answers WHERE survey_id=$1`, x.s.ID).Scan(&out.Total); e != nil {
		return out, e
	}
	for _, q := range x.s.Questions {
		stat := surveys.QuestionStats{}
		if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_survey_answers WHERE survey_id=$1 AND answer ? $2 AND answer->$2<>'null'::jsonb`, x.s.ID, q.ID).Scan(&stat.Count); e != nil {
			return out, e
		}
		if q.Type == surveys.Rating {
			if e := tx.QueryRowContext(ctx, `SELECT avg((answer->>$2)::double precision) FROM owned_survey_answers WHERE survey_id=$1 AND jsonb_typeof(answer->$2)='number'`, x.s.ID, q.ID).Scan(&stat.Average); e != nil {
				return out, e
			}
		} else if q.Type != surveys.FreeText {
			stat.Choices = map[string]int64{}
			query := `SELECT answer->>$2,count(*) FROM owned_survey_answers WHERE survey_id=$1 AND answer ? $2 AND answer->$2<>'null'::jsonb GROUP BY 1`
			if q.Type == surveys.MultipleChoice {
				query = `SELECT v.value,count(*) FROM owned_survey_answers a CROSS JOIN LATERAL jsonb_array_elements_text(CASE WHEN jsonb_typeof(answer->$2)='array' THEN answer->$2 ELSE '[]'::jsonb END) v(value) WHERE survey_id=$1 GROUP BY 1`
			}
			rows, e := tx.QueryContext(ctx, query, x.s.ID, q.ID)
			if e != nil {
				return out, e
			}
			for rows.Next() {
				var key string
				var n int64
				if e = rows.Scan(&key, &n); e != nil {
					rows.Close()
					return out, e
				}
				stat.Choices[key] = n
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return out, e
			}
		}
		out.Questions[q.ID] = stat
	}
	return out, nil
}
func (r *Repository) Get(ctx context.Context, actor, id string) (surveys.Survey, error) {
	tx, e := r.begin(ctx, actor, "", false)
	if e != nil {
		return surveys.Survey{}, e
	}
	defer tx.Rollback()
	x, e := r.load(ctx, tx, actor, id, false)
	if e != nil {
		return surveys.Survey{}, e
	}
	if actor != "" {
		a, e := scanAnswer(tx.QueryRowContext(ctx, `SELECT `+answerFields+` FROM owned_survey_answers WHERE survey_id=$1 AND account_id=$2`, id, actor), x.s.Anonymous)
		if e == nil {
			x.s.UserAnswer = &a
		} else if !errors.Is(e, surveys.ErrNotFound) {
			return surveys.Survey{}, e
		}
		ok, e := r.role(ctx, tx, actor, x.s.PublisherID, accounts.Viewer)
		if e != nil {
			return surveys.Survey{}, e
		}
		if !x.s.HideResults || ok {
			v, e := r.stats(ctx, tx, x)
			if e != nil {
				return surveys.Survey{}, e
			}
			x.s.Stats = legacyStats(v)
		}
	}
	if e = tx.Commit(); e != nil {
		return surveys.Survey{}, e
	}
	return x.s, nil
}
func (r *Repository) Results(ctx context.Context, actor, id string) (surveys.Results, error) {
	tx, e := r.begin(ctx, actor, "", true)
	if e != nil {
		return surveys.Results{}, e
	}
	defer tx.Rollback()
	x, e := r.load(ctx, tx, actor, id, false)
	if e != nil {
		return surveys.Results{}, e
	}
	ok, e := r.role(ctx, tx, actor, x.s.PublisherID, accounts.Viewer)
	if e != nil {
		return surveys.Results{}, e
	}
	if x.s.HideResults && !ok {
		return surveys.Results{}, surveys.ErrForbidden
	}
	v, e := r.stats(ctx, tx, x)
	if e != nil {
		return v, e
	}
	return v, tx.Commit()
}
func validPage(p surveys.Page) bool {
	return p.Take > 0 && p.Take <= 100 && p.Offset >= 0 && p.Offset <= 100000
}
func (r *Repository) Feedback(ctx context.Context, actor, id string, p surveys.Page) ([]surveys.Answer, int64, error) {
	if !validPage(p) {
		return nil, 0, surveys.ErrInvalid
	}
	tx, e := r.begin(ctx, actor, "", true)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	x, e := r.load(ctx, tx, actor, id, false)
	if e != nil {
		return nil, 0, e
	}
	ok, e := r.role(ctx, tx, actor, x.s.PublisherID, accounts.Viewer)
	if e != nil {
		return nil, 0, e
	}
	if !ok {
		return nil, 0, surveys.ErrForbidden
	}
	var count int64
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_survey_answers WHERE survey_id=$1`, id).Scan(&count); e != nil {
		return nil, 0, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+answerFields+` FROM owned_survey_answers WHERE survey_id=$1 ORDER BY created_at DESC,id LIMIT $2 OFFSET $3`, id, p.Take, p.Offset)
	if e != nil {
		return nil, 0, e
	}
	out := []surveys.Answer{}
	for rows.Next() {
		a, e := scanAnswer(rows, x.s.Anonymous)
		if e != nil {
			rows.Close()
			return nil, 0, e
		}
		out = append(out, a)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, 0, e
	}
	return out, count, tx.Commit()
}
func (r *Repository) List(ctx context.Context, actor string, p surveys.Page) ([]surveys.Survey, int64, error) {
	if !validPage(p) {
		return nil, 0, surveys.ErrInvalid
	}
	tx, e := r.begin(ctx, actor, "", true)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT s.id::text FROM owned_surveys s JOIN owned_publishers p ON p.id=s.publisher_id JOIN owned_publisher_members m ON m.publisher_id=p.id WHERE m.account_id=$1 AND m.joined_at IS NOT NULL AND m.revoked_at IS NULL AND s.deleted_at IS NULL AND p.deleted_at IS NULL AND ($2='' OR `+accountpg.PublisherNamePredicate("p", "$2")+` OR p.id::text=$2) AND (NOT $3 OR s.status=1 AND (s.ended_at IS NULL OR s.ended_at>CURRENT_TIMESTAMP)) ORDER BY s.created_at DESC,s.id`, actor, strings.TrimSpace(p.Publisher), p.Active)
	if e != nil {
		return nil, 0, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, 0, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, 0, e
	}
	out := []surveys.Survey{}
	var total int64
	for _, id := range ids {
		x, e := r.load(ctx, tx, actor, id, false)
		if errors.Is(e, surveys.ErrNotFound) {
			continue
		}
		if e != nil {
			return nil, 0, e
		}
		if total >= int64(p.Offset) && len(out) < p.Take {
			out = append(out, x.s)
		}
		total++
	}
	return out, total, tx.Commit()
}
