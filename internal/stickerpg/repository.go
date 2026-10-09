// SPDX-License-Identifier: AGPL-3.0-only
package stickerpg

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/stickers"
	"solsynth.local/go-migration/ports/sphere/surveys"
	"sort"
	"strings"
	"time"
)

//go:embed schema.sql
var Schema string

type Repository struct {
	db         *sql.DB
	publishers *accountpg.Repository
	guard      *authoritypg.TransactionGuard
	visibility postpg.VisibilityProvider
	media      surveys.MediaResolver
	now        func() time.Time
}

func New(db *sql.DB) *Repository {
	return &Repository{db: db, publishers: accountpg.New(db), now: time.Now}
}
func NewWithAuthority(db *sql.DB, g *authoritypg.TransactionGuard) *Repository {
	r := New(db)
	if g == nil {
		g = authoritypg.NewTransactionGuard(nil, nil)
	}
	r.guard = g
	return r
}
func (r *Repository) WithMedia(m surveys.MediaResolver) *Repository { r.media = m; return r }
func (r *Repository) WithVisibility(v postpg.VisibilityProvider) *Repository {
	r.visibility = v
	realms, _ := v.(accountpg.RealmProvider)
	r.publishers.WithRealms(realms)
	return r
}
func (r *Repository) BindAuthority(ctx context.Context, p authority.Principal) context.Context {
	if r.guard != nil {
		return r.guard.Bind(ctx, p)
	}
	return ctx
}

var _ stickers.Repository = (*Repository)(nil)

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
		return stickers.ErrNotFound
	}
	var p *pgconn.PgError
	if errors.As(e, &p) && p.Code == "23505" {
		return stickers.ErrConflict
	}
	return e
}
func (r *Repository) begin(ctx context.Context, actor, permission string, required bool) (*sql.Tx, error) {
	if r == nil || r.db == nil {
		return nil, stickers.ErrUnavailable
	}
	if actor == "" && required || actor != "" && !authority.ValidID(actor) {
		return nil, stickers.ErrUnauthenticated
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
			return nil, stickers.ErrUnauthenticated
		}
	}
	return tx, nil
}
func audit(ctx context.Context, tx *sql.Tx, actor, id, action string, now time.Time) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_sticker_audit(actor_id,pack_id,action,occurred_at) VALUES($1,$2,$3,$4)`, actor, id, action, now)
	return e
}
func (r *Repository) image(ctx context.Context, tx *sql.Tx, actor, id string) (media.File, error) {
	if r.media == nil {
		return media.File{}, stickers.ErrUnavailable
	}
	f, e := r.media.ResolvePublicInTransaction(ctx, tx, actor, id)
	if e != nil {
		return f, e
	}
	switch f.MIMEType {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/avif":
		return f, nil
	default:
		return media.File{}, stickers.ErrInvalid
	}
}

type packRecord struct {
	p    stickers.Pack
	icon *string
}

func (r *Repository) pack(ctx context.Context, tx *sql.Tx, actor, selector string, edit bool) (packRecord, error) {
	var x packRecord
	var pub, id string
	e := tx.QueryRowContext(ctx, `SELECT id::text,publisher_id::text FROM owned_sticker_packs WHERE deleted_at IS NULL AND (id::text=$1 OR lower(prefix)=lower($1))`, selector).Scan(&id, &pub)
	if e != nil {
		return x, mapped(e)
	}
	scopes, e := postpg.PrelockPublisherRealms(ctx, tx, r.visibility, []string{pub})
	if e != nil {
		if errors.Is(e, posts.ErrNotFound) {
			return x, stickers.ErrNotFound
		}
		return x, e
	}
	if edit {
		if _, e = r.publishers.RequirePostEditor(ctx, tx, actor, pub); e != nil {
			return x, e
		}
	} else {
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, pub).Scan(&pub); e != nil {
			return x, mapped(e)
		}
	}
	lock := " FOR SHARE OF s"
	if edit {
		lock = " FOR UPDATE OF s"
	}
	var verification []byte
	e = tx.QueryRowContext(ctx, `SELECT s.id::text,s.publisher_id::text,s.name,s.description,s.prefix,s.icon_id::text,s.created_at,s.updated_at,p.id::text,p.name,p.nick,p.bio,p.type,p.account_id::text,p.realm_id::text,p.created_at,p.updated_at,p.verification FROM owned_sticker_packs s JOIN owned_publishers p ON p.id=s.publisher_id WHERE s.id=$1 AND s.deleted_at IS NULL AND p.deleted_at IS NULL AND (p.account_id IS NULL OR EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id=p.account_id AND NOT a.disabled))`+lock, id).Scan(&x.p.ID, &x.p.PublisherID, &x.p.Name, &x.p.Description, &x.p.Prefix, &x.icon, &x.p.CreatedAt, &x.p.UpdatedAt, &x.p.Publisher.ID, &x.p.Publisher.Name, &x.p.Publisher.Nick, &x.p.Publisher.Bio, &x.p.Publisher.Type, &x.p.Publisher.AccountID, &x.p.Publisher.RealmID, &x.p.Publisher.CreatedAt, &x.p.Publisher.UpdatedAt, &verification)
	if e != nil {
		return x, mapped(e)
	}
	x.p.Publisher.Verification, e = accounts.DecodeVerification(verification)
	if e != nil {
		return x, e
	}
	if x.p.PublisherID != pub {
		return x, stickers.ErrConflict
	}
	if e = accountpg.VerifyPublisherRealms(ctx, tx, scopes); e != nil {
		return x, e
	}
	visible, e := postpg.CanViewPublisher(ctx, tx, actor, pub, r.visibility)
	if e != nil {
		return x, e
	}
	if !visible {
		return x, stickers.ErrNotFound
	}
	if edit && x.p.Publisher.Type != 0 {
		return x, stickers.ErrUnsupported
	}
	if r.visibility != nil && actor != "" && x.p.Publisher.AccountID != nil {
		blocked, e := r.visibility.IsBlockedInTransaction(ctx, tx, actor, *x.p.Publisher.AccountID)
		if e != nil {
			return x, e
		}
		if blocked {
			return x, stickers.ErrNotFound
		}
	}
	x.p.Stickers = []stickers.Sticker{}
	if x.icon != nil {
		f, e := r.image(ctx, tx, actor, *x.icon)
		if e != nil && !errors.Is(e, media.ErrNotFound) {
			return x, e
		}
		if e == nil {
			x.p.Icon = &f
		}
	}
	return x, nil
}
func (r *Repository) applyPack(ctx context.Context, tx *sql.Tx, actor string, x *packRecord, p stickers.PackPatch) error {
	if p.Name != nil {
		x.p.Name = *p.Name
	}
	if p.Description != nil {
		x.p.Description = *p.Description
	}
	if p.Prefix != nil {
		x.p.Prefix = *p.Prefix
	}
	if e := stickers.ValidatePack(x.p); e != nil {
		return e
	}
	if p.IconID != nil {
		if *p.IconID == "" {
			x.icon = nil
			x.p.Icon = nil
		} else {
			f, e := r.image(ctx, tx, actor, *p.IconID)
			if e != nil {
				return e
			}
			x.icon = p.IconID
			x.p.Icon = &f
		}
	}
	return nil
}
func (r *Repository) CreatePack(ctx context.Context, actor, selector string, p stickers.PackPatch) (stickers.Pack, error) {
	tx, e := r.begin(ctx, actor, "stickers.packs.create", true)
	if e != nil {
		return stickers.Pack{}, e
	}
	defer tx.Rollback()
	pub, e := r.publishers.ResolveForPost(ctx, tx, actor, selector)
	if e != nil {
		return stickers.Pack{}, e
	}
	if pub.Type != 0 || pub.AccountID == nil || *pub.AccountID != actor {
		return stickers.Pack{}, stickers.ErrForbidden
	}
	id, e := uid()
	if e != nil {
		return stickers.Pack{}, e
	}
	now := r.now().UTC()
	x := packRecord{p: stickers.Pack{ID: id, PublisherID: pub.ID, Publisher: pub, CreatedAt: now, UpdatedAt: now, Stickers: []stickers.Sticker{}}}
	if e = r.applyPack(ctx, tx, actor, &x, p); e != nil {
		return stickers.Pack{}, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_sticker_packs(id,publisher_id,name,description,prefix,icon_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$7)`, id, pub.ID, x.p.Name, x.p.Description, x.p.Prefix, x.icon, now)
	if e != nil {
		return stickers.Pack{}, mapped(e)
	}
	if e = audit(ctx, tx, actor, id, "pack.create", now); e != nil {
		return stickers.Pack{}, e
	}
	if e = tx.Commit(); e != nil {
		return stickers.Pack{}, e
	}
	return x.p, nil
}
func (r *Repository) UpdatePack(ctx context.Context, actor, id string, p stickers.PackPatch) (stickers.Pack, error) {
	tx, e := r.begin(ctx, actor, "stickers.packs.update", true)
	if e != nil {
		return stickers.Pack{}, e
	}
	defer tx.Rollback()
	x, e := r.pack(ctx, tx, actor, id, true)
	if e != nil {
		return stickers.Pack{}, e
	}
	if e = r.applyPack(ctx, tx, actor, &x, p); e != nil {
		return stickers.Pack{}, e
	}
	now := r.now().UTC()
	x.p.UpdatedAt = now
	_, e = tx.ExecContext(ctx, `UPDATE owned_sticker_packs SET name=$2,description=$3,prefix=$4,icon_id=$5,updated_at=$6 WHERE id=$1`, x.p.ID, x.p.Name, x.p.Description, x.p.Prefix, x.icon, now)
	if e != nil {
		return stickers.Pack{}, mapped(e)
	}
	if e = audit(ctx, tx, actor, x.p.ID, "pack.update", now); e != nil {
		return stickers.Pack{}, e
	}
	return x.p, tx.Commit()
}
func (r *Repository) DeletePack(ctx context.Context, actor, id string) error {
	tx, e := r.begin(ctx, actor, "stickers.packs.delete", true)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	x, e := r.pack(ctx, tx, actor, id, true)
	if e != nil {
		return e
	}
	now := r.now().UTC()
	if _, e = tx.ExecContext(ctx, `UPDATE owned_sticker_packs SET deleted_at=$2,updated_at=$2 WHERE id=$1`, x.p.ID, now); e != nil {
		return e
	}
	if e = audit(ctx, tx, actor, x.p.ID, "pack.delete", now); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *Repository) GetPack(ctx context.Context, actor, id string) (stickers.Pack, error) {
	tx, e := r.begin(ctx, actor, "", false)
	if e != nil {
		return stickers.Pack{}, e
	}
	defer tx.Rollback()
	x, e := r.pack(ctx, tx, actor, id, false)
	if e != nil {
		return stickers.Pack{}, e
	}
	return x.p, tx.Commit()
}
func (r *Repository) sticker(ctx context.Context, tx *sql.Tx, actor string, p stickers.Pack, id string, replaceImage ...bool) (stickers.Sticker, error) {
	var s stickers.Sticker
	var image string
	e := tx.QueryRowContext(ctx, `SELECT id::text,pack_id::text,slug,name,image_id::text,size,mode,ordering,created_at,updated_at FROM owned_stickers WHERE id::text=$1 AND pack_id=$2 AND deleted_at IS NULL`, id, p.ID).Scan(&s.ID, &s.PackID, &s.Slug, &s.Name, &image, &s.Size, &s.Mode, &s.Order, &s.CreatedAt, &s.UpdatedAt)
	if e != nil {
		return s, mapped(e)
	}
	if len(replaceImage) > 0 && replaceImage[0] {
		s.Image.ID = image
		s.Identifier = p.Prefix + "+" + s.Slug
		return s, nil
	}
	s.Image, e = r.image(ctx, tx, actor, image)
	if errors.Is(e, media.ErrNotFound) {
		return s, stickers.ErrNotFound
	}
	s.Identifier = p.Prefix + "+" + s.Slug
	return s, e
}
func (r *Repository) content(ctx context.Context, tx *sql.Tx, actor string, p stickers.Pack) ([]stickers.Sticker, error) {
	rows, e := tx.QueryContext(ctx, `SELECT id::text FROM owned_stickers WHERE pack_id=$1 AND deleted_at IS NULL ORDER BY ordering,id`, p.ID)
	if e != nil {
		return nil, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	out := []stickers.Sticker{}
	for _, id := range ids {
		s, e := r.sticker(ctx, tx, actor, p, id)
		if errors.Is(e, stickers.ErrNotFound) {
			continue
		}
		if e != nil {
			return nil, e
		}
		out = append(out, s)
	}
	return out, nil
}
func (r *Repository) Content(ctx context.Context, actor, id string) ([]stickers.Sticker, error) {
	tx, e := r.begin(ctx, actor, "", false)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	x, e := r.pack(ctx, tx, actor, id, false)
	if e != nil {
		return nil, e
	}
	out, e := r.content(ctx, tx, actor, x.p)
	if e != nil {
		return nil, e
	}
	return out, tx.Commit()
}
func (r *Repository) GetSticker(ctx context.Context, actor, pack, id string) (stickers.Sticker, error) {
	tx, e := r.begin(ctx, actor, "", false)
	if e != nil {
		return stickers.Sticker{}, e
	}
	defer tx.Rollback()
	x, e := r.pack(ctx, tx, actor, pack, false)
	if e != nil {
		return stickers.Sticker{}, e
	}
	s, e := r.sticker(ctx, tx, actor, x.p, id)
	if e != nil {
		return s, e
	}
	return s, tx.Commit()
}
func (r *Repository) applySticker(ctx context.Context, tx *sql.Tx, actor string, s *stickers.Sticker, p stickers.StickerPatch) error {
	if p.Slug != nil {
		s.Slug = *p.Slug
	}
	if p.Name != nil {
		s.Name = *p.Name
	}
	if p.Size != nil {
		s.Size = *p.Size
	}
	if p.Mode != nil {
		s.Mode = *p.Mode
	}
	if p.ImageID != nil {
		f, e := r.image(ctx, tx, actor, *p.ImageID)
		if e != nil {
			return e
		}
		s.Image = f
	}
	return stickers.ValidateSticker(*s)
}
func (r *Repository) CreateSticker(ctx context.Context, actor, pack string, p stickers.StickerPatch) (stickers.Sticker, error) {
	tx, e := r.begin(ctx, actor, "stickers.create", true)
	if e != nil {
		return stickers.Sticker{}, e
	}
	defer tx.Rollback()
	x, e := r.pack(ctx, tx, actor, pack, true)
	if e != nil {
		return stickers.Sticker{}, e
	}
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_stickers WHERE pack_id=$1 AND deleted_at IS NULL`, x.p.ID).Scan(&count); e != nil {
		return stickers.Sticker{}, e
	}
	if count >= stickers.MaxStickers {
		return stickers.Sticker{}, stickers.ErrCapacity
	}
	id, e := uid()
	if e != nil {
		return stickers.Sticker{}, e
	}
	now := r.now().UTC()
	s := stickers.Sticker{ID: id, PackID: x.p.ID, Order: count, CreatedAt: now, UpdatedAt: now}
	if p.Name == nil && p.Slug != nil {
		s.Name = *p.Slug
	}
	if e = r.applySticker(ctx, tx, actor, &s, p); e != nil {
		return stickers.Sticker{}, e
	}
	s.Identifier = x.p.Prefix + "+" + s.Slug
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_stickers(id,pack_id,slug,name,image_id,size,mode,ordering,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)`, s.ID, s.PackID, s.Slug, s.Name, s.Image.ID, s.Size, s.Mode, s.Order, now)
	if e != nil {
		return stickers.Sticker{}, mapped(e)
	}
	if e = audit(ctx, tx, actor, x.p.ID, "sticker.create", now); e != nil {
		return stickers.Sticker{}, e
	}
	return s, tx.Commit()
}
func (r *Repository) UpdateSticker(ctx context.Context, actor, pack, id string, p stickers.StickerPatch) (stickers.Sticker, error) {
	tx, e := r.begin(ctx, actor, "stickers.content.update", true)
	if e != nil {
		return stickers.Sticker{}, e
	}
	defer tx.Rollback()
	x, e := r.pack(ctx, tx, actor, pack, true)
	if e != nil {
		return stickers.Sticker{}, e
	}
	s, e := r.sticker(ctx, tx, actor, x.p, id, p.ImageID != nil)
	if e != nil {
		return s, e
	}
	if e = r.applySticker(ctx, tx, actor, &s, p); e != nil {
		return stickers.Sticker{}, e
	}
	now := r.now().UTC()
	s.UpdatedAt = now
	s.Identifier = x.p.Prefix + "+" + s.Slug
	_, e = tx.ExecContext(ctx, `UPDATE owned_stickers SET slug=$2,name=$3,image_id=$4,size=$5,mode=$6,updated_at=$7 WHERE id=$1`, s.ID, s.Slug, s.Name, s.Image.ID, s.Size, s.Mode, now)
	if e != nil {
		return stickers.Sticker{}, mapped(e)
	}
	if e = audit(ctx, tx, actor, x.p.ID, "sticker.update", now); e != nil {
		return stickers.Sticker{}, e
	}
	return s, tx.Commit()
}
func (r *Repository) DeleteSticker(ctx context.Context, actor, pack, id string) error {
	tx, e := r.begin(ctx, actor, "stickers.content.delete", true)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	x, e := r.pack(ctx, tx, actor, pack, true)
	if e != nil {
		return e
	}
	now := r.now().UTC()
	res, e := tx.ExecContext(ctx, `UPDATE owned_stickers SET deleted_at=$3,updated_at=$3 WHERE id::text=$1 AND pack_id=$2 AND deleted_at IS NULL`, id, x.p.ID, now)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return stickers.ErrNotFound
	}
	if e = audit(ctx, tx, actor, x.p.ID, "sticker.delete", now); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *Repository) Lookup(ctx context.Context, actor, identifier string) (stickers.Sticker, error) {
	tx, e := r.begin(ctx, actor, "", false)
	if e != nil {
		return stickers.Sticker{}, e
	}
	defer tx.Rollback()
	identifier = strings.ToLower(strings.TrimSpace(identifier))
	if len(identifier) > 257 {
		return stickers.Sticker{}, stickers.ErrInvalid
	}
	rows, e := tx.QueryContext(ctx, `SELECT s.id::text,s.pack_id::text FROM owned_stickers s JOIN owned_sticker_packs p ON p.id=s.pack_id WHERE s.deleted_at IS NULL AND p.deleted_at IS NULL AND (s.id::text=$1 OR lower(p.prefix||'+'||s.slug)=$1 OR lower(p.prefix||s.slug)=$1) LIMIT 2`, identifier)
	if e != nil {
		return stickers.Sticker{}, e
	}
	var id, pack string
	count := 0
	for rows.Next() {
		count++
		if e = rows.Scan(&id, &pack); e != nil {
			rows.Close()
			return stickers.Sticker{}, e
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return stickers.Sticker{}, e
	}
	if count != 1 {
		return stickers.Sticker{}, stickers.ErrNotFound
	}
	x, e := r.pack(ctx, tx, actor, pack, false)
	if e != nil {
		return stickers.Sticker{}, e
	}
	s, e := r.sticker(ctx, tx, actor, x.p, id)
	if e != nil {
		return s, e
	}
	return s, tx.Commit()
}
func validPage(p stickers.Page) bool {
	return p.Take > 0 && p.Take <= 100 && p.Offset >= 0 && p.Offset <= 100000 && len(p.Query) <= 256
}
func (r *Repository) List(ctx context.Context, actor string, p stickers.Page) ([]stickers.Pack, int64, error) {
	if !validPage(p) {
		return nil, 0, stickers.ErrInvalid
	}
	tx, e := r.begin(ctx, actor, "", false)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT s.id::text FROM owned_sticker_packs s JOIN owned_publishers p ON p.id=s.publisher_id WHERE s.deleted_at IS NULL AND p.deleted_at IS NULL AND ($1='' OR `+accountpg.PublisherNamePredicate("p", "$1")+` OR p.id::text=$1) AND ($2='' OR strpos(lower(s.name||' '||s.description),lower($2))>0) ORDER BY s.created_at DESC,s.id`, p.Publisher, p.Query)
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
	out := []stickers.Pack{}
	var total int64
	for _, id := range ids {
		x, e := r.pack(ctx, tx, actor, id, false)
		if errors.Is(e, stickers.ErrNotFound) {
			continue
		}
		if e != nil {
			return nil, 0, e
		}
		if total >= int64(p.Offset) && len(out) < p.Take {
			x.p.Stickers, e = r.content(ctx, tx, actor, x.p)
			if e != nil {
				return nil, 0, e
			}
			if len(x.p.Stickers) > 8 {
				x.p.Stickers = x.p.Stickers[:8]
			}
			out = append(out, x.p)
		}
		total++
	}
	return out, total, tx.Commit()
}
func (r *Repository) Search(ctx context.Context, actor string, p stickers.Page) ([]stickers.Sticker, int64, error) {
	if !validPage(p) {
		return nil, 0, stickers.ErrInvalid
	}
	tx, e := r.begin(ctx, actor, "", false)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT s.id::text,s.pack_id::text FROM owned_stickers s JOIN owned_sticker_packs p ON p.id=s.pack_id WHERE s.deleted_at IS NULL AND p.deleted_at IS NULL AND left(lower(p.prefix||'+'||s.slug),length($1))=lower($1) ORDER BY s.created_at DESC,s.id`, p.Query)
	if e != nil {
		return nil, 0, e
	}
	pairs := [][2]string{}
	for rows.Next() {
		var pair [2]string
		if e = rows.Scan(&pair[0], &pair[1]); e != nil {
			rows.Close()
			return nil, 0, e
		}
		pairs = append(pairs, pair)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, 0, e
	}
	out := []stickers.Sticker{}
	var total int64
	for _, pair := range pairs {
		x, e := r.pack(ctx, tx, actor, pair[1], false)
		if errors.Is(e, stickers.ErrNotFound) {
			continue
		}
		if e != nil {
			return nil, 0, e
		}
		s, e := r.sticker(ctx, tx, actor, x.p, pair[0])
		if errors.Is(e, stickers.ErrNotFound) {
			continue
		}
		if e != nil {
			return nil, 0, e
		}
		if total >= int64(p.Offset) && len(out) < p.Take {
			out = append(out, s)
		}
		total++
	}
	return out, total, tx.Commit()
}
func (r *Repository) Rendering(ctx context.Context, actor, pack string, v stickers.Rendering) ([]stickers.Sticker, error) {
	if len(v.StickerIDs) == 0 || len(v.StickerIDs) > 24 || v.Size == nil && v.Mode == nil || v.Size != nil && (*v.Size < 0 || *v.Size > 3) || v.Mode != nil && (*v.Mode < 0 || *v.Mode > 1) {
		return nil, stickers.ErrInvalid
	}
	ids := append([]string{}, v.StickerIDs...)
	sort.Strings(ids)
	for i, id := range ids {
		if !authority.ValidID(id) || i > 0 && id == ids[i-1] {
			return nil, stickers.ErrInvalid
		}
	}
	tx, e := r.begin(ctx, actor, "stickers.content.update", true)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	x, e := r.pack(ctx, tx, actor, pack, true)
	if e != nil {
		return nil, e
	}
	out := []stickers.Sticker{}
	for _, id := range ids {
		s, e := r.sticker(ctx, tx, actor, x.p, id)
		if e != nil {
			return nil, e
		}
		if v.Size != nil {
			s.Size = *v.Size
		}
		if v.Mode != nil {
			s.Mode = *v.Mode
		}
		out = append(out, s)
	}
	now := r.now().UTC()
	for i := range out {
		out[i].UpdatedAt = now
		if _, e = tx.ExecContext(ctx, `UPDATE owned_stickers SET size=$2,mode=$3,updated_at=$4 WHERE id=$1`, out[i].ID, out[i].Size, out[i].Mode, now); e != nil {
			return nil, e
		}
	}
	if e = audit(ctx, tx, actor, x.p.ID, "sticker.rendering", now); e != nil {
		return nil, e
	}
	return out, tx.Commit()
}
func (r *Repository) Reorder(ctx context.Context, actor, pack string, v []stickers.OrderItem) error {
	if e := stickers.ValidateOrder(v); e != nil {
		return e
	}
	permission := "stickers.content.update"
	if pack == "" {
		permission = "stickers.packs.order"
	} else if len(v) > 24 {
		return stickers.ErrInvalid
	}
	tx, e := r.begin(ctx, actor, permission, true)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	id := ""
	if pack != "" {
		x, e := r.pack(ctx, tx, actor, pack, true)
		if e != nil {
			return e
		}
		id = x.p.ID
	}
	for _, item := range v {
		var res sql.Result
		if pack != "" {
			res, e = tx.ExecContext(ctx, `UPDATE owned_stickers SET ordering=$3 WHERE id=$1 AND pack_id=$2 AND deleted_at IS NULL`, item.ID, id, item.Order)
		} else {
			res, e = tx.ExecContext(ctx, `UPDATE owned_sticker_ownerships SET ordering=$3 WHERE id=$1 AND account_id=$2`, item.ID, actor, item.Order)
		}
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return stickers.ErrNotFound
		}
	}
	if pack != "" {
		if e = audit(ctx, tx, actor, id, "sticker.order", r.now().UTC()); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func scanOwn(row interface{ Scan(...any) error }) (stickers.Ownership, error) {
	var v stickers.Ownership
	e := row.Scan(&v.ID, &v.PackID, &v.AccountID, &v.Order, &v.CreatedAt)
	return v, mapped(e)
}

const ownFields = `id::text,pack_id::text,account_id::text,ordering,created_at`

func (r *Repository) Own(ctx context.Context, actor, pack string) (stickers.Ownership, error) {
	tx, e := r.begin(ctx, actor, "stickers.packs.own", true)
	if e != nil {
		return stickers.Ownership{}, e
	}
	defer tx.Rollback()
	x, e := r.pack(ctx, tx, actor, pack, false)
	if e != nil {
		return stickers.Ownership{}, e
	}
	id, e := uid()
	if e != nil {
		return stickers.Ownership{}, e
	}
	now := r.now().UTC()
	res, e := tx.ExecContext(ctx, `INSERT INTO owned_sticker_ownerships(id,pack_id,account_id,created_at) VALUES($1,$2,$3,$4) ON CONFLICT(pack_id,account_id) DO NOTHING`, id, x.p.ID, actor, now)
	if e != nil {
		return stickers.Ownership{}, e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return stickers.Ownership{}, e
	}
	if n > 0 {
		if e = audit(ctx, tx, actor, x.p.ID, "pack.own", now); e != nil {
			return stickers.Ownership{}, e
		}
	}
	v, e := scanOwn(tx.QueryRowContext(ctx, `SELECT `+ownFields+` FROM owned_sticker_ownerships WHERE pack_id=$1 AND account_id=$2`, x.p.ID, actor))
	if e != nil {
		return v, e
	}
	return v, tx.Commit()
}
func (r *Repository) Ownership(ctx context.Context, actor, pack string) (stickers.Ownership, error) {
	tx, e := r.begin(ctx, actor, "", true)
	if e != nil {
		return stickers.Ownership{}, e
	}
	defer tx.Rollback()
	x, e := r.pack(ctx, tx, actor, pack, false)
	if e != nil {
		return stickers.Ownership{}, e
	}
	v, e := scanOwn(tx.QueryRowContext(ctx, `SELECT `+ownFields+` FROM owned_sticker_ownerships WHERE pack_id=$1 AND account_id=$2`, x.p.ID, actor))
	if e != nil {
		return v, e
	}
	return v, tx.Commit()
}
func (r *Repository) Release(ctx context.Context, actor, pack string) error {
	if !authority.ValidID(pack) {
		return stickers.ErrNotFound
	}
	tx, e := r.begin(ctx, actor, "stickers.packs.own", true)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	res, e := tx.ExecContext(ctx, `DELETE FROM owned_sticker_ownerships WHERE pack_id=$1 AND account_id=$2`, pack, actor)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return stickers.ErrNotFound
	}
	if e = audit(ctx, tx, actor, pack, "pack.release", r.now().UTC()); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *Repository) Owned(ctx context.Context, actor string) ([]stickers.Ownership, error) {
	tx, e := r.begin(ctx, actor, "", true)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT `+ownFields+` FROM owned_sticker_ownerships WHERE account_id=$1 ORDER BY ordering,created_at,id`, actor)
	if e != nil {
		return nil, e
	}
	all := []stickers.Ownership{}
	for rows.Next() {
		v, e := scanOwn(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		all = append(all, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	out := []stickers.Ownership{}
	for _, v := range all {
		x, e := r.pack(ctx, tx, actor, v.PackID, false)
		if errors.Is(e, stickers.ErrNotFound) {
			continue
		}
		if e != nil {
			return nil, e
		}
		x.p.Stickers, e = r.content(ctx, tx, actor, x.p)
		if e != nil {
			return nil, e
		}
		v.Pack = &x.p
		out = append(out, v)
	}
	return out, tx.Commit()
}
