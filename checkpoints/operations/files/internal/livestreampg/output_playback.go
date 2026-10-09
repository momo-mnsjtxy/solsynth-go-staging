// SPDX-License-Identifier: AGPL-3.0-only
package livestreampg

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"strings"
	"time"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

func (r *Repository) GetOutput(ctx context.Context, actor, sid, kind string) (live.OutputResponse, error) {
	var zero live.OutputResponse
	if kind != "egress" && kind != "hls" {
		return zero, live.ErrInvalid
	}
	if _, e := r.outputProvider(); e != nil {
		return zero, e
	}
	permission := "livestreams." + kind
	tx, _, _, e := r.locked(ctx, actor, sid, "", permission, true, false)
	if e != nil {
		return zero, e
	}
	defer tx.Rollback()
	v, e := scanOutput(tx.QueryRowContext(ctx, `SELECT `+outputFields+` FROM owned_livestream_outputs WHERE stream_id=$1::uuid AND kind=$2::text ORDER BY created_at DESC,id DESC LIMIT 1`, sid, kind))
	if e != nil {
		return zero, e
	}
	return outputResponse(v), r.commit(ctx, tx, actor, permission)
}

func (r *Repository) artifact(ctx context.Context, v outputRecord, name string, limit int64) ([]byte, time.Time, error) {
	var at time.Time
	if len(v.Destinations) != 1 || !r.outputNamespace(v.Destinations[0]) {
		return nil, at, live.ErrOutputUnavailable
	}
	f, size, at, e := r.outputStore.OpenStreamingArtifact(ctx, v.Destinations[0], v.ID, name, limit)
	if errors.Is(e, media.ErrNotFound) {
		return nil, at, live.ErrNotFound
	}
	if e != nil {
		return nil, at, live.ErrOutputUnavailable
	}
	defer f.Close()
	if size < 1 || size > limit {
		return nil, at, live.ErrOutputUnavailable
	}
	data, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil || int64(len(data)) != size || int64(len(data)) > limit {
		return nil, at, live.ErrOutputUnavailable
	}
	if e = ctx.Err(); e != nil {
		return nil, at, e
	}
	return data, at, nil
}

func (r *Repository) OpenHLS(ctx context.Context, actor, sid, id, asset string) (live.Playback, error) {
	var out live.Playback
	if !authority.ValidID(actor) {
		return out, live.ErrUnauthorized
	}
	if !authority.ValidID(sid) || !authority.ValidID(id) || !live.ValidOutputAsset(asset) {
		return out, live.ErrInvalid
	}
	if _, e := r.outputProvider(); e != nil {
		return out, e
	}
	// The immutable output actor is observed only after authority preflight. This
	// read transaction closes before the complete sorted account lock plan opens.
	observation, e := r.read(ctx, actor)
	if e != nil {
		return out, e
	}
	var starter string
	e = observation.QueryRowContext(ctx, `SELECT actor_id::text FROM owned_livestream_outputs WHERE id=$1::uuid AND stream_id=$2::uuid AND kind='hls'`, id, sid).Scan(&starter)
	observation.Rollback()
	if errors.Is(e, sql.ErrNoRows) {
		return out, live.ErrNotFound
	}
	if e != nil {
		return out, e
	}
	tx, _, stream, e := r.locked(ctx, actor, sid, "", "livestreams.view", false, false, starter)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if _, e = r.visible(ctx, tx, actor, &stream); e != nil {
		return out, e
	}
	v, e := loadOutput(ctx, tx, id)
	if e != nil {
		return out, e
	}
	if v.Stream != sid || v.Actor != starter || v.Kind != "hls" || v.Phase != "active" && v.Phase != "done" {
		return out, live.ErrNotFound
	}
	if v.Phase == "active" {
		if ok, e := r.outputAlive(ctx, tx, v, stream); e != nil || !ok {
			if e != nil {
				return out, e
			}
			return out, live.ErrNotFound
		}
	} else if v.Finished == nil {
		return out, live.ErrNotFound
	}
	playlist, at, e := r.artifact(ctx, v, v.Playlist, r.outputs.MaxPlaylistBytes)
	if e != nil {
		return out, e
	}
	projected, allowed, e := live.ProjectHLS(playlist, v.Stream, v.ID, r.outputs.MaxSegments)
	if e != nil {
		return out, e
	}
	if asset == v.Playlist {
		out = live.Playback{Body: projected, MIME: "application/vnd.apple.mpegurl", ModifiedAt: at}
	} else {
		if !allowed[asset] || !strings.HasSuffix(asset, ".ts") {
			return out, live.ErrNotFound
		}
		data, at, e := r.artifact(ctx, v, asset, r.outputs.MaxSegmentBytes)
		if e != nil {
			return out, e
		}
		// This supported transport is ordinary 188-byte MPEG-TS packets. Partial
		// files, M2TS/fMP4 and arbitrary bytes are not served as a valid segment.
		if len(data) < 188 || len(data)%188 != 0 {
			return out, live.ErrPlaylist
		}
		for i := 0; i < len(data); i += 188 {
			if data[i] != 0x47 {
				return out, live.ErrPlaylist
			}
		}
		out = live.Playback{Body: data, MIME: "video/mp2t", ModifiedAt: at}
	}
	if e = r.commit(ctx, tx, actor, "livestreams.view"); e != nil {
		return live.Playback{}, e
	}
	return out, nil
}
