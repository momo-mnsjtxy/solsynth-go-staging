// SPDX-License-Identifier: AGPL-3.0-only
package rewindpg

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/rewind"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed schema.sql
var Schema string

type AccountReferences interface {
	RewindAccount(context.Context, *sql.DB, *sql.Tx, string) (accounts.Account, error)
	RewindPublisher(context.Context, *sql.DB, *sql.Tx, string, string) (accounts.Publisher, error)
}
type RoomReferences interface {
	ReadSQL(string, string) string
	RewindRoom(context.Context, *sql.DB, *sql.Tx, string, string) (rooms.Room, error)
}

// Optional trusted local providers. They must honor the supplied transaction,
// account/window and bounds; no HTTP request can select or configure them.
type ActionLogs interface {
	ActionTimes(context.Context, *sql.DB, *sql.Tx, string, time.Time, time.Time, int) ([]time.Time, error)
}
type Tokenizer interface {
	TopWords(context.Context, []string, int) ([]rewind.Word, error)
}
type Options struct {
	Now                     func() time.Time
	ReportingYear           int
	ActionLogs              ActionLogs
	Tokenizer               Tokenizer
	CanonicalActionActivity bool
	Dictionary              *DictionaryConfig
}
type Repository struct {
	db         *sql.DB
	guard      *authoritypg.TransactionGuard
	accounts   AccountReferences
	rooms      RoomReferences
	visibility postpg.VisibilityProvider
	options    Options
}

func New(db *sql.DB, g *authoritypg.TransactionGuard, a AccountReferences, rooms RoomReferences, v postpg.VisibilityProvider, o Options) (*Repository, error) {
	if db == nil || g == nil || a == nil || rooms == nil {
		return nil, rewind.ErrUnavailable
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.ReportingYear != 0 {
		if _, _, e := rewind.Window(o.ReportingYear); e != nil {
			return nil, e
		}
	}
	if o.CanonicalActionActivity {
		if o.ActionLogs != nil {
			return nil, rewind.ErrInvalid
		}
		var err error
		o.ActionLogs, err = NewCanonicalActionLogs(db)
		if err != nil {
			return nil, err
		}
	}
	if o.Dictionary != nil {
		if o.Tokenizer != nil {
			return nil, rewind.ErrInvalid
		}
		var err error
		o.Tokenizer, err = NewDictionaryTokenizer(*o.Dictionary)
		if err != nil {
			return nil, err
		}
	}
	return &Repository{db, g, a, rooms, v, o}, nil
}
func (r *Repository) BindAuthority(ctx context.Context, p authority.Principal) context.Context {
	return r.guard.Bind(ctx, p)
}
func (r *Repository) now() time.Time { return r.options.Now().UTC().Truncate(time.Microsecond) }
func (r *Repository) Ready(ctx context.Context) error {
	_, e := r.db.ExecContext(ctx, `SELECT id,snapshot,sharable_code FROM owned_account_rewind_points LIMIT 0;SELECT day FROM owned_progression_checkins LIMIT 0;SELECT status FROM owned_social_effective_relationships LIMIT 0;SELECT content FROM owned_text_posts LIMIT 0;SELECT attitude FROM owned_post_reactions LIMIT 0;SELECT sender_id FROM chat_messages LIMIT 0;SELECT ended_at FROM call_v1.calls LIMIT 0`)
	if e != nil {
		return e
	}
	for _, provider := range []any{r.options.ActionLogs, r.options.Tokenizer} {
		if ready, ok := provider.(interface{ Ready(context.Context) error }); ok {
			if e = ready.Ready(ctx); e != nil {
				return e
			}
		}
	}
	return nil
}
func id() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}

var codePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{32}$`)

func shareCode() (string, error) {
	var b [24]byte
	_, e := rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:]), e
}

type rowScanner interface{ Scan(...any) error }

func scan(row rowScanner) (p rewind.Point, s rewind.Snapshot, e error) {
	var raw []byte
	e = row.Scan(&p.ID, &p.AccountID, &p.Year, &p.SchemaVersion, &p.SharableCode, &raw, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		return p, s, rewind.ErrNotFound
	}
	if e != nil {
		return
	}
	if len(raw) > 131072 {
		return p, s, rewind.ErrUnavailable
	}
	e = json.Unmarshal(raw, &s)
	return
}

const columns = `id::text,account_id::text,year,schema_version,sharable_code,snapshot,created_at,updated_at`

func (r *Repository) ownerTx(ctx context.Context, actor string, share bool) (*sql.Tx, error) {
	if !authority.ValidID(actor) {
		return nil, rewind.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return nil, e
	}
	if share {
		_, e = r.guard.Authorize(ctx, r.db, tx, actor, "rewind.create")
	} else {
		_, e = r.guard.Authenticate(ctx, r.db, tx, actor)
	}
	if e != nil {
		tx.Rollback()
		return nil, e
	}
	return tx, nil
}
func audit(ctx context.Context, tx *sql.Tx, p rewind.Point, action string, now time.Time) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_account_rewind_audit(account_id,point_id,action,occurred_at)VALUES($1::uuid,$2::uuid,$3::text,$4::timestamptz)`, p.AccountID, p.ID, action, now)
	return e
}

// wordPreparation contains only authorized text captured by this request, never
// a reusable cross-account cache. Tokenization runs after its transaction ends.
type wordPreparation struct {
	texts   []string
	words   []rewind.Word
	limited bool
}

func validateWords(words []rewind.Word) error {
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
	return nil
}
func (r *Repository) Current(ctx context.Context, actor string) (out rewind.Point, e error) {
	year := r.options.ReportingYear
	if year == 0 {
		year = r.now().Year()
	}
	start, end, e := rewind.Window(year)
	if e != nil {
		return out, e
	}
	var prepared *wordPreparation
	// Two outside-lock computations plus one final capture bound concurrent edits.
	// A changing corpus returns unavailable; no partial/stale snapshot is stored.
	for attempt := 0; attempt < 3; attempt++ {
		var texts []string
		var compute bool
		out, texts, compute, e = r.currentAttempt(ctx, actor, year, start, end, prepared)
		if e != nil || !compute {
			return out, e
		}
		if attempt == 2 {
			return rewind.Point{}, rewind.ErrUnavailable
		}
		words, err := r.options.Tokenizer.TopWords(ctx, texts, 100)
		limited := errors.Is(err, ErrTokenLimit)
		if err != nil && !limited {
			return rewind.Point{}, err
		}
		if !limited {
			if err = validateWords(words); err != nil {
				return rewind.Point{}, err
			}
		}
		prepared = &wordPreparation{texts: texts, words: words, limited: limited}
	}
	return rewind.Point{}, rewind.ErrUnavailable
}
func (r *Repository) currentAttempt(ctx context.Context, actor string, year int, start, end time.Time, prepared *wordPreparation) (out rewind.Point, texts []string, compute bool, e error) {
	tx, e := r.ownerTx(ctx, actor, false)
	if e != nil {
		return out, nil, false, e
	}
	defer tx.Rollback()
	now := r.now()
	p, s, e := scan(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM owned_account_rewind_points WHERE account_id=$1::uuid AND year=$2::integer FOR SHARE`, actor, year))
	if errors.Is(e, rewind.ErrNotFound) {
		s, texts, e = r.snapshot(ctx, tx, actor, start, end, now)
		if e != nil {
			return out, nil, false, e
		}
		if r.options.Tokenizer != nil && texts != nil {
			if prepared == nil || !slices.Equal(texts, prepared.texts) {
				if _, e = r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
					return out, nil, false, e
				}
				// Release canonical account/SQL locks before any provider computation.
				return out, texts, true, tx.Commit()
			}
			if prepared.limited {
				s.Unavailable = append(s.Unavailable, "sphere.top_words_token_limit")
			} else {
				s.TopWords = prepared.words
				if s.TopWords == nil {
					s.TopWords = []rewind.Word{}
				}
			}
		}
		raw, err := json.Marshal(s)
		if err != nil || len(raw) > 131072 {
			return out, nil, false, rewind.ErrUnavailable
		}
		pid, err := id()
		if err != nil {
			return out, nil, false, err
		}
		if _, e = r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
			return out, nil, false, e
		}
		p, s, e = scan(tx.QueryRowContext(ctx, `INSERT INTO owned_account_rewind_points(id,account_id,year,schema_version,snapshot,created_at,updated_at)VALUES($1::uuid,$2::uuid,$3::integer,1,$4::jsonb,$5::timestamptz,$5::timestamptz)RETURNING `+columns, pid, actor, year, string(raw), now))
		if e != nil {
			return out, nil, false, e
		}
		if e = audit(ctx, tx, p, "rewind.create", now); e != nil {
			return out, nil, false, e
		}
	} else if e != nil {
		return out, nil, false, e
	}
	out, e = r.project(ctx, tx, p, s, actor)
	if e != nil {
		return out, nil, false, e
	}
	if _, e = r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
		return rewind.Point{}, nil, false, e
	}
	return out, nil, false, tx.Commit()
}
func (r *Repository) Share(ctx context.Context, actor string, year int, public bool) (out rewind.Point, e error) {
	if _, _, e = rewind.Window(year); e != nil {
		return
	}
	tx, e := r.ownerTx(ctx, actor, true)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	p, s, e := scan(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM owned_account_rewind_points WHERE account_id=$1::uuid AND year=$2::integer FOR UPDATE`, actor, year))
	if e != nil {
		return out, e
	}
	var code *string
	if public {
		v, e := shareCode()
		if e != nil {
			return out, e
		}
		code = &v
	}
	now := r.now()
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, "rewind.create"); e != nil {
		return out, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_account_rewind_points SET sharable_code=$2::text,updated_at=$3::timestamptz WHERE id=$1::uuid`, p.ID, code, now); e != nil {
		return out, e
	}
	p.SharableCode = code
	p.UpdatedAt = now
	action := "rewind.private"
	if public {
		action = "rewind.public"
	}
	if e = audit(ctx, tx, p, action, now); e != nil {
		return out, e
	}
	out, e = r.project(ctx, tx, p, s, actor)
	if e != nil {
		return out, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, "rewind.create"); e != nil {
		return rewind.Point{}, e
	}
	return out, tx.Commit()
}
func (r *Repository) Public(ctx context.Context, code string) (out rewind.Point, e error) {
	if !codePattern.MatchString(code) {
		return out, rewind.ErrNotFound
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	var actor string
	e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_account_rewind_points WHERE sharable_code=$1::text`, code).Scan(&actor)
	if errors.Is(e, sql.ErrNoRows) {
		return out, rewind.ErrNotFound
	}
	if e != nil {
		return out, e
	}
	// Account then point matches owner mutation order. Revocation/disable waits
	// observe committed state before a public projection is produced.
	var disabled bool
	if e = tx.QueryRowContext(ctx, `SELECT disabled FROM owned_authority_accounts WHERE id=$1::uuid FOR SHARE`, actor).Scan(&disabled); e != nil {
		return out, e
	}
	if disabled {
		return out, rewind.ErrNotFound
	}
	var punished bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_authority_punishments WHERE account_id=$1::uuid AND kind='disable_account' AND(starts_at IS NULL OR starts_at<=$2::timestamptz)AND(expires_at IS NULL OR expires_at>$2::timestamptz))`, actor, r.now()).Scan(&punished)
	if e != nil {
		return out, e
	}
	if punished {
		return out, rewind.ErrNotFound
	}
	p, s, e := scan(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM owned_account_rewind_points WHERE account_id=$1::uuid AND sharable_code=$2::text FOR SHARE`, actor, code))
	if e != nil {
		return out, e
	}
	out, e = r.project(ctx, tx, p, s, "")
	if e != nil {
		return out, e
	}
	if e = tx.QueryRowContext(ctx, `SELECT disabled OR EXISTS(SELECT 1 FROM owned_authority_punishments WHERE account_id=$1::uuid AND kind='disable_account' AND(starts_at IS NULL OR starts_at<=clock_timestamp())AND(expires_at IS NULL OR expires_at>clock_timestamp())) FROM owned_authority_accounts WHERE id=$1::uuid`, actor).Scan(&disabled); e != nil {
		return rewind.Point{}, e
	}
	if disabled {
		return rewind.Point{}, rewind.ErrNotFound
	}
	return out, tx.Commit()
}
