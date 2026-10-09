// SPDX-License-Identifier: AGPL-3.0-only
package livestreampg

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

//go:embed outputs_schema.sql
var OutputsSchema string

type OutputConfig struct {
	MaxOutputsPerStream, MaxSegments  int
	MaxPlaylistBytes, MaxSegmentBytes int64
	OperationTimeout                  time.Duration
	HLSNamespaces                     []string
	ArchiveImporter                   live.ArchiveImporter
}

func (r *Repository) WithOutputs(c OutputConfig, store live.ArtifactStore) error {
	if r == nil || c.MaxOutputsPerStream < 1 || c.MaxOutputsPerStream > 10000 || c.MaxSegments < 1 || c.MaxSegments > 2000 || c.MaxPlaylistBytes < 1024 || c.MaxPlaylistBytes > 256<<10 || c.MaxSegmentBytes < 1024 || c.MaxSegmentBytes > 32<<20 || c.OperationTimeout <= 0 || c.OperationTimeout > 5*time.Second || len(c.HLSNamespaces) > 32 {
		return live.ErrInvalid
	}
	p, ok := r.provider.(live.OutputProvider)
	if !ok || p.OutputContract() != live.EgressContract {
		return live.ErrOutputUnavailable
	}
	seen := map[string]bool{}
	for _, ns := range c.HLSNamespaces {
		if store == nil || !live.ValidOutputDestination(ns) || seen[ns] {
			return live.ErrInvalid
		}
		seen[ns] = true
		if _, e := p.OutputPolicyFingerprint([]string{ns}, "hls"); e != nil {
			return e
		}
	}
	c.HLSNamespaces = append([]string(nil), c.HLSNamespaces...)
	if c.ArchiveImporter != nil && c.ArchiveImporter.ArchiveImportContract() != live.ArchiveImportContract {
		return live.ErrInvalid
	}
	r.outputs = &c
	r.outputStore = store
	return nil
}
func (r *Repository) outputProvider() (live.OutputProvider, error) {
	if r == nil || r.outputs == nil {
		return nil, live.ErrOutputUnavailable
	}
	p, ok := r.provider.(live.OutputProvider)
	if !ok || p.OutputContract() != live.EgressContract {
		return nil, live.ErrOutputUnavailable
	}
	return p, nil
}
func (r *Repository) outputNamespace(ns string) bool {
	if r == nil || r.outputs == nil || r.outputStore == nil {
		return false
	}
	for _, v := range r.outputs.HLSNamespaces {
		if v == ns {
			return true
		}
	}
	return false
}
func (r *Repository) OutputsReady(ctx context.Context) error {
	if _, e := r.outputProvider(); e != nil {
		return e
	}
	if _, e := r.db.ExecContext(ctx, `SELECT id,phase,egress_id,authorization_epoch,policy_hash FROM owned_livestream_outputs LIMIT 0;SELECT id,state,media_id FROM owned_livestream_archive_imports LIMIT 0`); e != nil {
		return e
	}
	for _, ns := range r.outputs.HLSNamespaces {
		if e := r.outputStore.StreamingReady(ctx, ns); e != nil {
			return live.ErrOutputUnavailable
		}
	}
	return nil
}

type outputRecord struct {
	ID, Stream, Kind, Provider, Endpoint, Room, Actor, Session, Owner, Permission string
	Generation, Version, Epoch, OwnerVersion                                      int64
	Destinations                                                                  []string
	PolicyHash, RequestHash, KeyHash                                              []byte
	Playlist, Layout                                                              string
	SegmentDuration                                                               int
	Relay                                                                         bool
	Egress                                                                        *string
	Phase, Reason                                                                 string
	Created, Updated, Next                                                        time.Time
	Started, Finished                                                             *time.Time
}

const outputFields = `id::text,stream_id::text,generation,kind,provider,endpoint,room_name,actor_id::text,session_id::text,account_version,authorization_epoch,owner_id::text,owner_version,permission,destinations,policy_hash,request_hash,key_hash,playlist_name,segment_duration,layout,relay,egress_id,phase,reason,created_at,updated_at,started_at,finished_at,next_attempt_at`

func scanOutput(row scanner) (v outputRecord, e error) {
	var destinations []byte
	e = row.Scan(&v.ID, &v.Stream, &v.Generation, &v.Kind, &v.Provider, &v.Endpoint, &v.Room, &v.Actor, &v.Session, &v.Version, &v.Epoch, &v.Owner, &v.OwnerVersion, &v.Permission, &destinations, &v.PolicyHash, &v.RequestHash, &v.KeyHash, &v.Playlist, &v.SegmentDuration, &v.Layout, &v.Relay, &v.Egress, &v.Phase, &v.Reason, &v.Created, &v.Updated, &v.Started, &v.Finished, &v.Next)
	if errors.Is(e, sql.ErrNoRows) {
		return v, live.ErrNotFound
	}
	if e != nil {
		return v, e
	}
	e = json.Unmarshal(destinations, &v.Destinations)
	return
}
func loadOutput(ctx context.Context, tx *sql.Tx, id string) (outputRecord, error) {
	return scanOutput(tx.QueryRowContext(ctx, `SELECT `+outputFields+` FROM owned_livestream_outputs WHERE id=$1::uuid FOR UPDATE`, id))
}
func outputResponse(v outputRecord) live.OutputResponse {
	result := live.OutputResponse{OutputID: v.ID, State: v.Phase, Reason: v.Reason}
	if v.Egress != nil {
		result.EgressID = *v.Egress
	}
	if v.Kind == "hls" && (v.Phase == "active" || v.Phase == "done") {
		path := "/api/livestreams/" + v.Stream + "/hls/" + v.ID + "/" + v.Playlist
		result.PlaylistURL = &path
		result.PlaylistName = &v.Playlist
	}
	return result
}
func (r *Repository) outputPending(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	var installed bool
	if e := tx.QueryRowContext(ctx, `SELECT to_regclass('owned_livestream_outputs') IS NOT NULL`).Scan(&installed); e != nil {
		return false, e
	}
	if !installed {
		return false, nil
	}
	var pending bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_livestream_outputs WHERE stream_id=$1::uuid AND phase<>'done')`, id).Scan(&pending)
	return pending, e
}
func (r *Repository) outputProjection(ctx context.Context, tx *sql.Tx, v *live.Stream) error {
	if r.outputs == nil {
		return nil
	}
	rows, e := tx.QueryContext(ctx, `SELECT id::text,kind,egress_id,playlist_name,started_at FROM owned_livestream_outputs WHERE stream_id=$1::uuid AND generation=$2::bigint AND phase='active' ORDER BY kind`, v.ID, v.Generation)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var id, kind, playlist string
		var eg *string
		var at *time.Time
		if e = rows.Scan(&id, &kind, &eg, &playlist, &at); e != nil {
			return e
		}
		if kind == "hls" {
			path := "/api/livestreams/" + v.ID + "/hls/" + id + "/" + playlist
			v.HLSEgressID = eg
			v.HLSPlaylistPath = &path
			v.HLSStartedAt = at
		} else {
			v.EgressID = eg
		}
	}
	return rows.Err()
}

func (r *Repository) StartEgress(ctx context.Context, actor, sid string, q live.EgressRequest) (live.OutputResponse, error) {
	if len(q.RtmpURLs) != 0 || q.FilePath != nil {
		return live.OutputResponse{}, live.ErrInvalid
	}
	return r.startOutput(ctx, actor, sid, q.Key, live.OutputStart{Kind: "egress", Destinations: q.DestinationIDs, Layout: "grid"})
}
func (r *Repository) StartHLS(ctx context.Context, actor, sid string, q live.HLSRequest) (live.OutputResponse, error) {
	if q.HLSBaseURL != nil || q.SegmentCount != nil && *q.SegmentCount != 0 {
		return live.OutputResponse{}, live.ErrInvalid
	}
	v := live.OutputStart{Kind: "hls", Destinations: []string{q.DestinationID}, Playlist: "playlist.m3u8", SegmentDuration: 6, Layout: "grid"}
	if q.PlaylistName != nil {
		v.Playlist = *q.PlaylistName
	}
	if q.SegmentDuration != nil {
		v.SegmentDuration = *q.SegmentDuration
	}
	if q.Layout != nil {
		v.Layout = *q.Layout
	}
	if !r.outputNamespace(q.DestinationID) {
		return live.OutputResponse{}, live.ErrOutputUnavailable
	}
	if !live.ValidOutputAsset(v.Playlist) || !strings.HasSuffix(v.Playlist, ".m3u8") || v.Playlist == "recording.m3u8" || v.SegmentDuration < 2 || v.SegmentDuration > 30 {
		return live.OutputResponse{}, live.ErrInvalid
	}
	return r.startOutput(ctx, actor, sid, q.Key, v)
}

func (r *Repository) startOutput(ctx context.Context, actor, sid, key string, q live.OutputStart) (live.OutputResponse, error) {
	var zero live.OutputResponse
	provider, e := r.outputProvider()
	if e != nil {
		return zero, e
	}
	if !live.Text(key, 128) || len(key) > 128 || len(q.Destinations) < 1 || len(q.Destinations) > 8 || !live.ValidOutputLayout(q.Layout) {
		return zero, live.ErrInvalid
	}
	q.Destinations = append([]string(nil), q.Destinations...)
	sort.Strings(q.Destinations)
	fingerprint, e := provider.OutputPolicyFingerprint(q.Destinations, q.Kind)
	if e != nil {
		return zero, e
	}
	relay, e := provider.OutputUsesRelay(q.Destinations, q.Kind)
	if e != nil {
		return zero, e
	}
	permission := "livestreams." + q.Kind
	tx, p, stream, e := r.locked(ctx, actor, sid, "", permission, true, false)
	if e != nil {
		return zero, e
	}
	defer tx.Rollback()
	if !p.Account.Superuser || relay && stream.Visibility != live.Public {
		return zero, live.ErrForbidden
	}
	if relay {
		if ok, x := r.relayVisible(ctx, tx, stream); x != nil {
			return zero, x
		} else if !ok {
			return zero, live.ErrForbidden
		}
	}
	if stream.Status != live.Active || stream.OperationState != "active" {
		return zero, live.ErrConflict
	}
	run, e := loadRun(ctx, tx, sid, stream.Generation)
	if e != nil {
		return zero, e
	}
	if ok, e := r.runAlive(ctx, tx, run); e != nil || !ok {
		if e != nil {
			return zero, e
		}
		return zero, live.ErrForbidden
	}
	if e = r.providerMatches(run); e != nil {
		return zero, e
	}
	if q.Kind == "hls" {
		if e = r.outputStore.StreamingReady(ctx, q.Destinations[0]); e != nil {
			return zero, live.ErrOutputUnavailable
		}
	}
	digest := hash(q)
	var keyHash []byte
	if key != "" {
		keyHash = hash(key)
	}
	var existing outputRecord
	if keyHash != nil {
		existing, e = scanOutput(tx.QueryRowContext(ctx, `SELECT `+outputFields+` FROM owned_livestream_outputs WHERE stream_id=$1::uuid AND actor_id=$2::uuid AND key_hash=$3::bytea FOR UPDATE`, sid, actor, keyHash))
	} else {
		e = live.ErrNotFound
	}
	if errors.Is(e, live.ErrNotFound) {
		existing, e = scanOutput(tx.QueryRowContext(ctx, `SELECT `+outputFields+` FROM owned_livestream_outputs WHERE stream_id=$1::uuid AND kind=$2::text AND phase<>'done' FOR UPDATE`, sid, q.Kind))
	}
	if e == nil {
		if existing.Generation != stream.Generation || existing.Actor != actor || existing.Session != p.Session.ID || existing.Version != p.Account.Version || existing.Epoch != p.Session.AuthorizationEpoch || !bytes.Equal(existing.RequestHash, digest) || !bytes.Equal(existing.PolicyHash, fingerprint) || keyHash != nil && !bytes.Equal(existing.KeyHash, keyHash) {
			return zero, live.ErrConflict
		}
		if e = r.commit(ctx, tx, actor, permission); e != nil {
			return zero, e
		}
		if existing.Phase == "active" || existing.Phase == "done" {
			return outputResponse(existing), nil
		}
		return outputResponse(existing), live.ErrOutputUncertain
	}
	if !errors.Is(e, live.ErrNotFound) {
		return zero, e
	}
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_livestream_outputs WHERE stream_id=$1::uuid`, sid).Scan(&count); e != nil {
		return zero, e
	}
	if count >= r.outputs.MaxOutputsPerStream {
		return zero, live.ErrConflict
	}
	id, e := uid()
	if e != nil {
		return zero, e
	}
	at, e := now(ctx, tx)
	if e != nil {
		return zero, e
	}
	destinations, e := json.Marshal(q.Destinations)
	if e != nil {
		return zero, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_livestream_outputs(id,stream_id,generation,kind,provider,endpoint,room_name,actor_id,session_id,account_version,authorization_epoch,owner_id,owner_version,permission,destinations,policy_hash,request_hash,key_hash,playlist_name,segment_duration,layout,relay,phase,created_at,updated_at,next_attempt_at)VALUES($1::uuid,$2::uuid,$3::bigint,$4::text,$5::text,$6::text,$7::text,$8::uuid,$9::uuid,$10::bigint,$11::bigint,$12::uuid,$13::bigint,$14::text,$15::jsonb,$16::bytea,$17::bytea,$18::bytea,$19::text,$20::integer,$21::text,$22::boolean,'attempted',$23::timestamptz,$23::timestamptz,$23::timestamptz)`, id, sid, stream.Generation, q.Kind, run.Provider, run.Endpoint, run.Room, actor, p.Session.ID, p.Account.Version, p.Session.AuthorizationEpoch, run.Owner, run.OwnerVersion, permission, string(destinations), fingerprint, digest, keyHash, q.Playlist, q.SegmentDuration, q.Layout, relay, at)
	if e != nil {
		return zero, e
	}
	if e = audit(ctx, tx, sid, actor, permission+".requested"); e != nil {
		return zero, e
	}
	if e = r.commit(ctx, tx, actor, permission); e != nil {
		return zero, e
	}
	// Only this invocation can call StartOutput. A duplicate or a restarted
	// worker observes the durable attempt and may only reconcile/stop it.
	result, observed, e := r.activateOutput(ctx, actor, id, sid, permission)
	if e != nil && observed.ID != "" {
		if x := r.rememberOutput(ctx, id, observed); x != nil {
			e = errors.Join(e, x)
		}
	}
	return result, e
}
