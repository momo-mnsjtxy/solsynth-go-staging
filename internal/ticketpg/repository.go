// SPDX-License-Identifier: AGPL-3.0-only
package ticketpg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/tickets"
)

//go:embed schema.sql
var Schema string

type ActivationPolicy struct {
	RequiredTestKeys       []string
	RequireVerifiedContact bool
}

// RewardSink joins the same SQL transaction and must be idempotent per canonical
// account/test, never make network calls, or mint a second progression ledger.
type RewardSink interface {
	GrantExamRewardInTransaction(context.Context, *sql.Tx, string, string, string, int64) error
}
type Config struct {
	GrantedGroups map[string]string
	Activation    *ActivationPolicy
	Rewards       RewardSink
	Now           func() time.Time
}
type Repository struct {
	db    *sql.DB
	guard *authoritypg.TransactionGuard
	cfg   Config
	media MediaStore
}

func New(db *sql.DB, guard *authoritypg.TransactionGuard, cfg Config) (*Repository, error) {
	if db == nil || guard == nil {
		return nil, tickets.ErrUnavailable
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	groups := map[string]string{}
	for key, id := range cfg.GrantedGroups {
		if !tickets.ValidKey(key) || !authority.ValidID(id) {
			return nil, tickets.ErrInvalid
		}
		groups[key] = id
	}
	cfg.GrantedGroups = groups
	if cfg.Activation != nil {
		copy := *cfg.Activation
		copy.RequiredTestKeys = append([]string{}, cfg.Activation.RequiredTestKeys...)
		if len(copy.RequiredTestKeys) > 32 {
			return nil, tickets.ErrInvalid
		}
		seen := map[string]bool{}
		for _, key := range copy.RequiredTestKeys {
			if !tickets.ValidKey(key) || seen[strings.ToLower(key)] {
				return nil, tickets.ErrInvalid
			}
			seen[strings.ToLower(key)] = true
		}
		cfg.Activation = &copy
	}
	return &Repository{db: db, guard: guard, cfg: cfg}, nil
}
func (r *Repository) BindAuthority(ctx context.Context, p authority.Principal) context.Context {
	if r == nil || r.guard == nil {
		return ctx
	}
	return r.guard.Bind(ctx, p)
}
func (r *Repository) Ready(ctx context.Context) error {
	if r == nil || r.db == nil {
		return tickets.ErrUnavailable
	}
	_, e := r.db.ExecContext(ctx, `SELECT id,creator_id,status FROM owned_ticket_cases LIMIT 0; SELECT id,snapshot,submission_hash FROM owned_exam_attempts LIMIT 0; SELECT account_id,state FROM owned_exam_claims LIMIT 0; SELECT account_id,state FROM owned_exam_rewards LIMIT 0; SELECT account_id,state FROM owned_exam_activation_requests LIMIT 0; SELECT id,data FROM owned_exam_groups LIMIT 0; SELECT id,data FROM owned_exam_questions LIMIT 0; SELECT id,data FROM owned_exams LIMIT 0; SELECT id,data FROM owned_exam_trials LIMIT 0; SELECT account_id FROM owned_ticket_on_call LIMIT 0; SELECT id,sender_id,request_key FROM owned_ticket_messages LIMIT 0; SELECT test_id,group_id FROM owned_exam_group_links LIMIT 0; SELECT id,value,reviewed_by_id FROM owned_exam_answers LIMIT 0; SELECT actor_id,operation FROM owned_ticket_exam_audit LIMIT 0`)
	if e == nil && r.cfg.Activation != nil && r.cfg.Activation.RequireVerifiedContact {
		_, e = r.db.ExecContext(ctx, `SELECT account_id,verified_at FROM owned_authority_credentials LIMIT 0`)
	}
	if e == nil && r.media != nil {
		e = r.media.Ready(ctx)
	}
	return e
}
func uuid() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func mapped(e error) error {
	if errors.Is(e, sql.ErrNoRows) {
		return tickets.ErrNotFound
	}
	var p *pgconn.PgError
	if errors.As(e, &p) {
		switch p.Code {
		case "23505", "55P03", "40P01":
			return tickets.ErrConflict
		case "23503":
			return tickets.ErrNotFound
		case "23514", "22001":
			return tickets.ErrInvalid
		}
	}
	return e
}
func marshal(v any) ([]byte, error) { return json.Marshal(v) }
func digest(v any) (string, error) {
	b, e := marshal(v)
	if e != nil {
		return "", e
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:]), nil
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func permission(op string) (string, bool) {
	switch op {
	case "ticket.create":
		return "tickets.create", true
	case "ticket.update":
		return "tickets.update", true
	case "ticket.delete":
		return "tickets.delete", true
	case "ticket.message":
		return "tickets.messages.create", true
	case "ticket.status":
		return "tickets.status.update", true
	case "ticket.assign":
		return "tickets.assign", true
	case "ticket.list", "ticket.me", "ticket.count", "ticket.get":
		return "", false
	case "oncall.list":
		return "tickets.on_call.manage", false
	case "oncall.add", "oncall.remove":
		return "tickets.on_call.manage", true
	case "group.list", "question.list", "test.admin.list", "trial.list", "test.trial.preview", "question.export":
		return "tests.manage", false
	case "group.save", "group.delete", "question.save", "question.delete", "question.import", "question.prune", "test.save", "test.publish", "test.archive", "trial.save", "test.trial.create", "test.trial.start", "test.trial.grade":
		return "tests.manage", true
	case "attempt.list":
		return "tests.review", false
	case "answer.review":
		return "tests.review", true
	case "test.get", "trial.get", "attempt.get", "activation.get":
		return "tests.take", false
	case "attempt.start", "trial.start", "attempt.submit", "activation.recheck":
		return "tests.take", true
	case "test.public.list":
		return "", false
	default:
		return "!", false
	}
}
func (r *Repository) Execute(ctx context.Context, actor string, c tickets.Command) (out tickets.Result, err error) {
	if r == nil || r.db == nil || r.guard == nil {
		return out, tickets.ErrUnavailable
	}
	perm, write := permission(c.Operation)
	if perm == "!" {
		return out, tickets.ErrUnsupported
	}
	if !tickets.ValidIdempotency(c.IdempotencyKey) {
		return out, tickets.ErrInvalid
	}
	if c.IdempotencyKey != "" && c.Operation != "ticket.create" && c.Operation != "ticket.message" && c.Operation != "attempt.start" && c.Operation != "trial.start" && c.Operation != "test.trial.start" {
		return out, tickets.ErrUnsupported
	}
	public := c.Operation == "test.public.list" && actor == ""
	if !public && !authority.ValidID(actor) {
		return out, authority.ErrInvalid
	}
	options := &sql.TxOptions{Isolation: sql.LevelReadCommitted}
	if public {
		options = &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	}
	tx, e := r.db.BeginTx(ctx, options)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	// Assignments add a canonical account foreign key. Lock both accounts in
	// UUID order before the ticket row, including opposing assignments.
	if c.Operation == "ticket.assign" && c.AccountID != nil {
		if !authority.ValidID(*c.AccountID) {
			return out, tickets.ErrInvalid
		}
		if _, e = r.guard.Preflight(ctx, r.db, tx, actor, perm); e != nil {
			return out, e
		}
		rows, e := tx.QueryContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=ANY($1::uuid[]) ORDER BY id FOR UPDATE`, []string{actor, *c.AccountID})
		if e != nil {
			return out, e
		}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return out, e
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return out, e
		}
	}
	if !public {
		if perm == "" {
			_, e = r.guard.Authenticate(ctx, r.db, tx, actor)
		} else {
			_, e = r.guard.Authorize(ctx, r.db, tx, actor, perm)
		}
		if e != nil {
			return out, e
		}
	}
	now := r.cfg.Now().UTC().Truncate(time.Microsecond)
	switch {
	case strings.HasPrefix(c.Operation, "ticket."), strings.HasPrefix(c.Operation, "oncall."):
		out, e = r.support(ctx, tx, actor, c, now)
	case strings.HasPrefix(c.Operation, "group."), strings.HasPrefix(c.Operation, "question."), strings.HasPrefix(c.Operation, "test."), strings.HasPrefix(c.Operation, "trial."):
		if c.Operation == "attempt.start" || c.Operation == "trial.start" || c.Operation == "test.trial.start" {
			out, e = r.start(ctx, tx, actor, c, now)
		} else {
			out, e = r.bank(ctx, tx, actor, c, now)
		}
	case c.Operation == "attempt.start":
		out, e = r.start(ctx, tx, actor, c, now)
	case c.Operation == "attempt.get", c.Operation == "attempt.list", c.Operation == "attempt.submit", c.Operation == "answer.review":
		out, e = r.attempts(ctx, tx, actor, c, now)
	case c.Operation == "activation.get", c.Operation == "activation.recheck":
		var state tickets.ActivationState
		state, e = r.activation(ctx, tx, actor, c.Operation == "activation.recheck", now)
		out.Value = state
	default:
		e = tickets.ErrUnsupported
	}
	if e != nil {
		return tickets.Result{}, mapped(e)
	}
	if strings.HasPrefix(c.Operation, "ticket.") {
		if e = r.recheckTicket(ctx, tx, actor, c, perm); e != nil {
			return tickets.Result{}, mapped(e)
		}
	}
	if write && out.Changed {
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_ticket_exam_audit(actor_id,operation,resource_id,occurred_at)VALUES($1,$2,$3,$4)`, actor, c.Operation, nullable(out.ResourceID), now); e != nil {
			return tickets.Result{}, mapped(e)
		}
	}
	if e = tx.Commit(); e != nil {
		return tickets.Result{}, mapped(e)
	}
	if out.AfterCommit != nil {
		return out, out.AfterCommit
	}
	return out, nil
}
func (r *Repository) isAdmin(ctx context.Context, tx *sql.Tx, actor string) (bool, error) {
	_, e := r.guard.Authorize(ctx, r.db, tx, actor, "tickets.admin")
	if errors.Is(e, authority.ErrForbidden) {
		return false, nil
	}
	return e == nil, e
}
func account(ctx context.Context, tx *sql.Tx, id string) (tickets.Account, error) {
	var a tickets.Account
	e := tx.QueryRowContext(ctx, `SELECT a.id::text,a.name,a.nick FROM owned_authority_accounts a WHERE a.id=$1::uuid AND NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=statement_timestamp())AND(p.expires_at IS NULL OR p.expires_at>statement_timestamp()))`, id).Scan(&a.ID, &a.Name, &a.Nick)
	return a, mapped(e)
}
