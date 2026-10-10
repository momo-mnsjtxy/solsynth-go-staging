// SPDX-License-Identifier: AGPL-3.0-only
package livestreampg

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
	"strings"
	"time"
)

type run struct {
	Stream                                   string
	Generation                               int64
	Room, Provider, Endpoint, Actor, Session string
	Version, Epoch                           int64
	Owner                                    string
	OwnerVersion                             int64
	Permission                               string
	Mode                                     int
	Identity, Name, Participant              string
	Transcoding                              bool
	Hash                                     []byte
	Phase                                    string
	Ingress                                  *string
	CreatedAt, UpdatedAt, ExpiresAt          time.Time
}

const runFields = `stream_id::text,generation,room_name,provider,endpoint,actor_id::text,session_id::text,account_version,session_epoch,owner_id::text,owner_version,permission,mode,identity,ingress_name,participant_name,transcoding,request_hash,phase,ingress_id,created_at,updated_at,expires_at`

func loadRun(ctx context.Context, tx *sql.Tx, id string, generation int64) (v run, e error) {
	e = tx.QueryRowContext(ctx, `SELECT `+runFields+` FROM owned_livestream_runs WHERE stream_id=$1 AND generation=$2 FOR UPDATE`, id, generation).Scan(&v.Stream, &v.Generation, &v.Room, &v.Provider, &v.Endpoint, &v.Actor, &v.Session, &v.Version, &v.Epoch, &v.Owner, &v.OwnerVersion, &v.Permission, &v.Mode, &v.Identity, &v.Name, &v.Participant, &v.Transcoding, &v.Hash, &v.Phase, &v.Ingress, &v.CreatedAt, &v.UpdatedAt, &v.ExpiresAt)
	return
}
func roomName(id string, generation int64) string {
	return fmt.Sprintf("livestream_%s_%d", strings.ReplaceAll(id, "-", ""), generation)
}
func (r *Repository) providerMatches(v run) error {
	if e := r.configured(); e != nil {
		return e
	}
	if v.Provider != r.provider.Name() || v.Endpoint != r.provider.Endpoint() {
		return live.ErrUnconfigured
	}
	return nil
}
func normalize(p live.StartRequest, nick string) (mode int, transcode bool, name string) {
	mode = 1
	transcode = true
	name = nick
	if name == "" {
		name = "streamer"
	}
	if p.NoIngress != nil && *p.NoIngress {
		mode = 0
	}
	if p.UseWHIP != nil && *p.UseWHIP {
		mode = 2
	}
	if p.EnableTranscoding != nil {
		transcode = *p.EnableTranscoding
	}
	if p.ParticipantName != nil {
		name = *p.ParticipantName
	}
	return
}
func matches(v run, ing live.Ingress) bool {
	return ing.Room == v.Room && ing.Name == v.Name && ing.Identity == v.Identity && ing.InputType == v.Mode-1
}
func (r *Repository) findIngress(ctx context.Context, v run) (*live.Ingress, error) {
	items, e := r.provider.ListIngress(ctx, v.Room)
	if e != nil {
		return nil, live.ErrProvider
	}
	var found *live.Ingress
	for _, ing := range items {
		if !matches(v, ing) {
			continue
		}
		if found != nil {
			return nil, live.ErrConflict
		}
		if v.Ingress != nil && *v.Ingress != ing.ID {
			return nil, live.ErrConflict
		}
		copy := ing
		found = &copy
	}
	return found, nil
}
func (r *Repository) Start(ctx context.Context, actor, id string, req live.StartRequest) (out live.StartResponse, e error) {
	stage := "validate request"
	defer func() {
		if e != nil {
			e = fmt.Errorf("livestream start (%s): %w", stage, e)
		}
	}()
	if e = live.ValidateStart(req); e != nil {
		return
	}
	if e = r.configured(); e != nil {
		return
	}
	stage = "lock initial authority and stream"
	tx, p, v, e := r.locked(ctx, actor, id, "", "livestreams.start", true, false)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if _, e = r.visible(ctx, tx, actor, &v); e != nil {
		return out, e
	}
	mode, transcode, name := normalize(req, v.Publisher.Nick)
	if !live.Text(name, 128) {
		return out, live.ErrInvalid
	}
	digest := hash(struct {
		Mode      int
		Transcode bool
		Name      string
	}{mode, transcode, name})
	create := false
	if v.OperationState == "ready" && (v.Status == live.Pending || v.Status == live.Ended) {
		if pending, x := r.outputPending(ctx, tx, id); x != nil {
			return out, x
		} else if pending {
			return out, live.ErrConflict
		}
		at, x := now(ctx, tx)
		if x != nil {
			return out, x
		}
		generation := v.Generation + 1
		if generation > 1000000 {
			return out, live.ErrConflict
		}
		room := roomName(id, generation)
		identity := "ingress_" + strings.ReplaceAll(id, "-", "") + fmt.Sprintf("_%d", generation)
		ownerScope, x := observe(ctx, tx, v.PublisherID, "")
		if x != nil {
			return out, x
		}
		var ownerVersion int64
		if x = tx.QueryRowContext(ctx, `SELECT version FROM owned_authority_accounts WHERE id=$1`, ownerScope.owner).Scan(&ownerVersion); x != nil {
			return out, x
		}
		stage = "insert durable run intent"
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_livestream_runs(stream_id,generation,room_name,provider,endpoint,actor_id,session_id,account_version,session_epoch,owner_id,owner_version,mode,identity,ingress_name,participant_name,transcoding,request_hash,phase,created_at,updated_at,expires_at,next_attempt_at)VALUES($1::uuid,$2::bigint,$3::text,$4::text,$5::text,$6::uuid,$7::uuid,$8::bigint,$9::bigint,$17::uuid,$18::bigint,$10::integer,$11::text,$3::text,$12::text,$13::boolean,$14::bytea,'attempted',$15::timestamptz,$15::timestamptz,$16::timestamptz,$15::timestamptz)`, id, generation, room, r.provider.Name(), r.provider.Endpoint(), actor, p.Session.ID, p.Account.Version, p.Session.AuthorizationEpoch, mode, identity, name, transcode, digest, at, at.Add(r.cfg.MaxRunDuration), ownerScope.owner, ownerVersion)
		if e != nil {
			return out, e
		}
		stage = "mark stream starting"
		_, e = tx.ExecContext(ctx, `UPDATE owned_livestreams SET generation=$2,room_name=$3,operation_state='starting',status=0,started_at=NULL,ended_at=NULL,viewer_count=0,updated_at=$4::timestamptz WHERE id=$1::uuid`, id, generation, room, at)
		if e != nil {
			return out, e
		}
		create = true
		if e = audit(ctx, tx, id, actor, "livestreams.start.requested"); e != nil {
			return out, e
		}
	} else if v.OperationState != "starting" && v.OperationState != "active" {
		return out, live.ErrConflict
	}
	// The attempt is durable before any provider operation. Only this invocation
	// may perform the first create; later invocations only discover its result.
	stage = "commit durable run intent"
	if e = r.commit(ctx, tx, actor, "livestreams.start"); e != nil {
		return out, e
	}
	stage = "lock activation authority and stream"
	tx, p, v, e = r.locked(ctx, actor, id, "", "livestreams.start", true, false)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	stage = "load recorded run intent"
	run, e := loadRun(ctx, tx, id, v.Generation)
	if e != nil {
		return out, e
	}
	if e = r.providerMatches(run); e != nil {
		return out, e
	}
	if run.Actor != actor || run.Session != p.Session.ID || run.Version != p.Account.Version || run.Epoch != p.Session.AuthorizationEpoch || !bytes.Equal(run.Hash, digest) || (run.Phase != "attempted" && run.Phase != "active") {
		return out, live.ErrConflict
	}
	at, e := now(ctx, tx)
	if e != nil {
		return out, e
	}
	if !run.ExpiresAt.After(at) {
		return out, live.ErrConflict
	}
	op, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stage = "ensure provider room"
	if e = r.provider.EnsureRoom(op, run.Room); e != nil {
		return out, live.ErrProvider
	}
	out = live.StartResponse{URL: r.provider.Endpoint(), RoomName: run.Room}
	if run.Mode != 0 {
		stage = "reconcile provider ingress"
		ing, x := r.findIngress(op, run)
		if x != nil {
			return out, x
		}
		if ing == nil {
			if !create {
				return out, live.ErrConflict
			}
			created, x := r.provider.CreateIngress(op, live.IngressRequest{Room: run.Room, Identity: run.Identity, Name: run.Name, ParticipantName: run.Participant, WHIP: run.Mode == 2, Transcoding: run.Transcoding})
			if x != nil {
				return out, live.ErrProvider
			}
			if !matches(run, created) || created.ID == "" || created.Key == "" {
				return out, live.ErrProvider
			}
			ing = &created
		}
		if ing.Key == "" {
			return out, live.ErrProvider
		}
		run.Ingress = &ing.ID
		out.URL = ing.URL
		out.StreamKey = &ing.Key
	}
	if run.Phase == "attempted" {
		at, e = now(ctx, tx)
		if e != nil {
			return out, e
		}
		if run.Ingress != nil {
			if _, e = tx.ExecContext(ctx, `INSERT INTO owned_livestream_ingresses(stream_id,generation,ingress_id)VALUES($1,$2,$3)ON CONFLICT DO NOTHING`, id, run.Generation, *run.Ingress); e != nil {
				return out, e
			}
		}
		stage = "activate durable run"
		if _, e = tx.ExecContext(ctx, `UPDATE owned_livestream_runs SET phase='active',ingress_id=$3::text,updated_at=$4::timestamptz,next_attempt_at=$4::timestamptz+interval '10 seconds' WHERE stream_id=$1::uuid AND generation=$2::bigint`, id, run.Generation, run.Ingress, at); e != nil {
			return out, e
		}
		stage = "activate stream"
		if _, e = tx.ExecContext(ctx, `UPDATE owned_livestreams SET operation_state='active',status=1,started_at=$2::timestamptz,ended_at=NULL,updated_at=$2::timestamptz WHERE id=$1::uuid`, id, at); e != nil {
			return out, e
		}
		stage = "audit activation"
		if e = audit(ctx, tx, id, actor, "livestreams.start"); e != nil {
			return out, e
		}
		if r.activations != nil {
			stage = "record subscriber notification intent"
			if e = r.activations.RecordLivestreamActivation(ctx, r.db, tx, id, v.PublisherID, run.Generation, at); e != nil {
				return out, e
			}
		}
	}
	stage = "commit activation"
	if e = r.commit(ctx, tx, actor, "livestreams.start"); e != nil {
		return live.StartResponse{}, e
	}
	return out, nil
}

func (r *Repository) Token(ctx context.Context, actor, id string, streamer, tool bool) (out live.TokenResponse, e error) {
	if e = r.configured(); e != nil {
		return
	}
	permission := "livestreams.view"
	if streamer {
		permission = "livestreams.start"
	}
	tx, p, v, e := r.locked(ctx, actor, id, "", permission, false, false)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	editor, e := r.visible(ctx, tx, actor, &v)
	if e != nil {
		return out, e
	}
	if streamer && !editor {
		return out, live.ErrForbidden
	}
	if v.Status != live.Active || v.OperationState != "active" {
		return out, live.ErrConflict
	}
	run, e := loadRun(ctx, tx, id, v.Generation)
	if e != nil {
		return out, e
	}
	if e = r.providerMatches(run); e != nil {
		return out, e
	}
	if alive, x := r.runAlive(ctx, tx, run); x != nil {
		return out, x
	} else if !alive {
		return out, live.ErrForbidden
	}
	at, e := now(ctx, tx)
	if e != nil {
		return out, e
	}
	expiry := at.Add(r.cfg.TokenTTL)
	for _, bound := range []time.Time{p.Session.ExpiresAt, run.ExpiresAt} {
		if expiry.After(bound) {
			expiry = bound
		}
	}
	if !expiry.After(at) {
		return out, live.ErrForbidden
	}
	var identity, state string
	e = tx.QueryRowContext(ctx, `SELECT identity::text,state FROM owned_livestream_admissions WHERE stream_id=$1 AND generation=$2 AND session_id=$3 AND tool=$4 AND publishing=$5`, id, run.Generation, p.Session.ID, tool, streamer).Scan(&identity, &state)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return out, e
	}
	if state == "removing" {
		return out, live.ErrConflict
	}
	if identity == "" || state == "removed" {
		var total, own int
		if e = tx.QueryRowContext(ctx, `SELECT count(*),count(*)FILTER(WHERE actor_id=$3) FROM owned_livestream_admissions WHERE stream_id=$1 AND generation=$2`, id, run.Generation, actor).Scan(&total, &own); e != nil {
			return out, e
		}
		if identity == "" && (total >= 100 || own >= 8) {
			return out, live.ErrConflict
		}
		identity, e = uid()
		if e != nil {
			return out, e
		}
	}
	token, e := r.provider.StreamToken(ctx, live.Grant{Room: run.Room, Identity: identity, Publish: streamer, IssuedAt: at, ExpiresAt: expiry})
	if e != nil || token == "" {
		return out, live.ErrProvider
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_livestream_admissions(stream_id,generation,session_id,actor_id,tool,publishing,permission,identity,account_version,session_epoch,state,issued_at,expires_at)VALUES($1::uuid,$2::bigint,$3::uuid,$4::uuid,$5::boolean,$6::boolean,$12::text,$7::uuid,$8::bigint,$9::bigint,'admitted',$10::timestamptz,$11::timestamptz)ON CONFLICT(stream_id,generation,session_id,tool,publishing)DO UPDATE SET identity=EXCLUDED.identity,account_version=EXCLUDED.account_version,session_epoch=EXCLUDED.session_epoch,state='admitted',permission=EXCLUDED.permission,issued_at=EXCLUDED.issued_at,expires_at=EXCLUDED.expires_at`, id, run.Generation, p.Session.ID, actor, tool, streamer, identity, p.Account.Version, p.Session.AuthorizationEpoch, at, expiry, permission)
	if e != nil {
		return out, e
	}
	if e = r.commit(ctx, tx, actor, permission); e != nil {
		return out, e
	}
	return live.TokenResponse{Token: token, RoomName: run.Room, URL: r.provider.Endpoint(), IsStreamer: streamer, Identity: identity, ExpiresAt: expiry}, nil
}
func (r *Repository) Details(ctx context.Context, actor, id string) (live.Details, error) {
	v, e := r.Get(ctx, actor, id)
	if e != nil {
		return live.Details{}, e
	}
	if e = r.configured(); e != nil {
		return live.Details{}, e
	}
	op, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	participants, e := r.provider.StreamParticipants(op, v.RoomName)
	if e != nil {
		return live.Details{}, live.ErrProvider
	}
	return live.Details{ParticipantCount: len(participants), ViewerCount: v.ViewerCount, PeakViewerCount: v.PeakViewerCount}, nil
}

func markEnd(ctx context.Context, tx *sql.Tx, v live.Stream, deleting bool, actor string) error {
	at, e := now(ctx, tx)
	if e != nil {
		return e
	}
	duration := int64(0)
	if v.Status == live.Active && v.StartedAt != nil && at.After(*v.StartedAt) {
		duration = int64(at.Sub(*v.StartedAt) / time.Second)
	}
	state := "ending"
	if v.Generation == 0 {
		state = "ready"
	}
	var deleted any
	if deleting {
		deleted = at
		state = "deleted"
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_livestreams SET status=2,operation_state=$2,ended_at=COALESCE(ended_at,$3::timestamptz),total_duration_seconds=total_duration_seconds+$4,viewer_count=0,deleted_at=COALESCE(deleted_at,$5::timestamptz),updated_at=$3::timestamptz WHERE id=$1::uuid`, v.ID, state, at, duration, deleted)
	if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_livestream_runs SET phase='ending',updated_at=$2::timestamptz,next_attempt_at=$2::timestamptz WHERE stream_id=$1::uuid AND phase<>'done'`, v.ID, at); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_livestream_admissions SET state='removing' WHERE stream_id=$1 AND state='admitted'`, v.ID); e != nil {
		return e
	}
	action := "livestreams.end"
	if deleting {
		action = "livestreams.delete"
	}
	return audit(ctx, tx, v.ID, actor, action)
}
func (r *Repository) End(ctx context.Context, actor, id string, deleting bool) error {
	permission := "livestreams.end"
	if deleting {
		permission = "livestreams.delete"
	}
	tx, _, v, e := r.locked(ctx, actor, id, "", permission, true, deleting)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if v.DeletedAt == nil && !(v.Status == live.Ended && v.OperationState == "ready" && !deleting) {
		if e = markEnd(ctx, tx, v, deleting, actor); e != nil {
			return e
		}
	}
	if e = r.commit(ctx, tx, actor, permission); e != nil {
		return e
	}
	if v.Generation == 0 {
		return nil
	}
	return r.processRun(ctx, id, v.Generation)
}
