// SPDX-License-Identifier: AGPL-3.0-only
package fortunepg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/fortunes"
	"strings"
	"time"
	_ "time/tzdata"
)

//go:embed schema.sql
var Schema string

type Config struct {
	Now                     func() time.Time
	Provider                fortunes.Provider
	AllowAnonymousCatalogue bool
	Timeout                 time.Duration
	MaxAttempts             int
}
type Repository struct {
	db    *sql.DB
	guard *authoritypg.TransactionGuard
	cfg   Config
}

func New(db *sql.DB, guard *authoritypg.TransactionGuard, cfg Config) (*Repository, error) {
	if db == nil || guard == nil {
		return nil, fortunes.ErrUnavailable
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 90 * time.Second
	}
	if cfg.Timeout < time.Second || cfg.Timeout > 15*time.Minute {
		return nil, fortunes.ErrInvalid
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.MaxAttempts < 1 || cfg.MaxAttempts > 20 {
		return nil, fortunes.ErrInvalid
	}
	if cfg.Provider != nil && (cfg.Provider.Revision() == "" || len(cfg.Provider.Revision()) > 100) {
		return nil, fortunes.ErrInvalid
	}
	return &Repository{db, guard, cfg}, nil
}
func (r *Repository) BindAuthority(ctx context.Context, p authority.Principal) context.Context {
	return r.guard.Bind(ctx, p)
}
func (r *Repository) Ready(ctx context.Context) error {
	_, e := r.db.ExecContext(ctx, `SELECT revision,definition FROM owned_fortune_corpora LIMIT 0;SELECT corpus_revision FROM owned_fortune_config LIMIT 0;SELECT checkin_id,request_data,generation,lease_id FROM owned_fortune_requests LIMIT 0;SELECT id,account_id,day,level FROM owned_progression_checkins LIMIT 0;SELECT account_id,time_zone FROM owned_accounts_profiles LIMIT 0`)
	return e
}
func newID() (string, error) {
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
		return fortunes.ErrNotFound
	}
	return e
}
func (r *Repository) within(ctx context.Context, actor string, fn func(*sql.Tx) error) error {
	if !authority.ValidID(actor) {
		return authority.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
		return e
	}
	if e = fn(tx); e != nil {
		return mapped(e)
	}
	return tx.Commit()
}

// InstallCorpus is a trusted host configuration operation, not an HTTP/admin
// endpoint. Corpus bytes and provenance must be explicitly supplied; no seed or
// built-in fallback is present. Existing revisions are immutable.
func (r *Repository) InstallCorpus(ctx context.Context, c fortunes.Corpus) error {
	b, e := json.Marshal(c)
	if e != nil || len(b) > 2<<20 {
		return fortunes.ErrInvalid
	}
	if e = fortunes.Decode(b, &c); e != nil {
		return e
	}
	if e = fortunes.ValidateCorpus(&c); e != nil {
		return e
	}
	b, e = json.Marshal(c)
	if e != nil {
		return e
	}
	sum := sha256.Sum256(b)
	hash := hex.EncodeToString(sum[:])
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `LOCK TABLE owned_fortune_corpora IN SHARE ROW EXCLUSIVE MODE`); e != nil {
		return e
	}
	var prior string
	e = tx.QueryRowContext(ctx, `SELECT sha256 FROM owned_fortune_corpora WHERE revision=$1::text`, c.Revision).Scan(&prior)
	if e == nil && prior != hash {
		return fortunes.ErrConflict
	}
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	now := r.cfg.Now().UTC()
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_fortune_corpora(revision,sha256,definition,created_at)VALUES($1::text,$2::text,$3::jsonb,$4::timestamptz)ON CONFLICT(revision)DO NOTHING`, c.Revision, hash, string(b), now); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_fortune_config(id,corpus_revision,updated_at)VALUES(1,$1::text,$2::timestamptz)ON CONFLICT(id)DO UPDATE SET corpus_revision=EXCLUDED.corpus_revision,updated_at=EXCLUDED.updated_at`, c.Revision, now); e != nil {
		return e
	}
	return tx.Commit()
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadCorpus(ctx context.Context, q queryer, revision string) (fortunes.Corpus, error) {
	var b []byte
	var c fortunes.Corpus
	e := q.QueryRowContext(ctx, `SELECT definition FROM owned_fortune_corpora WHERE revision=COALESCE(NULLIF($1::text,''),(SELECT corpus_revision FROM owned_fortune_config WHERE id=1))`, revision).Scan(&b)
	if errors.Is(e, sql.ErrNoRows) {
		return c, fortunes.ErrUnavailable
	}
	if e != nil {
		return c, e
	}
	if e = fortunes.Decode(b, &c); e != nil {
		return c, e
	}
	return c, fortunes.ValidateCorpus(&c)
}
func (r *Repository) provider(ctx context.Context, q queryer, revision string) (fortunes.Provider, error) {
	if r.cfg.Provider != nil && (revision == "" || r.cfg.Provider.Revision() == revision) {
		return r.cfg.Provider, nil
	}
	if strings.HasPrefix(revision, "corpus.") || revision == "" {
		c, e := loadCorpus(ctx, q, strings.TrimPrefix(revision, "corpus."))
		if e != nil {
			return nil, e
		}
		return fortunes.NewCorpusProvider(c)
	}
	if r.cfg.Provider != nil && r.cfg.Provider.Revision() == revision {
		return r.cfg.Provider, nil
	}
	return nil, fortunes.ErrUnavailable
}
func (r *Repository) Catalogue(ctx context.Context, actor, language string, offset, take int, random bool) (out []fortunes.Saying, total int64, e error) {
	if language != "" && !fortunes.ValidLanguage(language) || offset < 0 || offset > 100000 || take < 1 || take > 100 {
		return nil, 0, fortunes.ErrInvalid
	}
	if actor == "" && !r.cfg.AllowAnonymousCatalogue {
		return nil, 0, authority.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	if actor != "" {
		if _, e = r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
			return nil, 0, e
		}
	}
	c, e := loadCorpus(ctx, tx, "")
	if e != nil {
		return nil, 0, e
	}
	list := []fortunes.Saying{}
	for _, v := range c.Sayings {
		if language == "" || strings.EqualFold(language, v.Language) {
			list = append(list, v)
		}
	}
	total = int64(len(list))
	if random {
		if len(list) == 0 {
			return nil, 0, fortunes.ErrNotFound
		}
		n, e := rand.Int(rand.Reader, big.NewInt(int64(len(list))))
		if e != nil {
			return nil, 0, e
		}
		out = []fortunes.Saying{list[n.Int64()]}
	} else {
		start := min(offset, len(list))
		out = list[start:min(len(list), start+take)]
	}
	e = tx.Commit()
	return
}
