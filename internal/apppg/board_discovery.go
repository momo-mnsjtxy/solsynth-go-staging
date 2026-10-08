// SPDX-License-Identifier: AGPL-3.0-only
package apppg

import (
	"context"
	"encoding/json"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/passport/board"
)

// One SQL snapshot applies current public publisher/app state and enabled widget
// filtering before pagination; credentials, grants and private payloads never enter DTOs.
func (r *Repository) DiscoverBoard(ctx context.Context, q apps.BoardDiscoveryQuery) ([]apps.BoardDiscoveryApp, error) {
	out := []apps.BoardDiscoveryApp{}
	if r == nil || r.db == nil {
		return out, apps.ErrUnavailable
	}
	if e := apps.ValidateBoardDiscovery(q); e != nil {
		return out, e
	}
	limit, offset := q.Take, q.Offset
	if q.Slug != "" {
		limit = 1
		offset = 0
	}
	rows, e := r.db.QueryContext(ctx, `SELECT a.id::text,a.slug,a.name,a.description,pub.name,(SELECT jsonb_agg(bounded.manifest ORDER BY bounded.key COLLATE "C") FROM (SELECT w.key,w.manifest FROM owned_app_widgets w WHERE w.app_id=a.id AND w.manifest->'is_enabled'='true'::jsonb ORDER BY w.key COLLATE "C" LIMIT 33) bounded)`+appJoins+` WHERE `+activeApp+` AND pub.shadowbanned_at IS NULL AND a.status=1 AND EXISTS(SELECT 1 FROM owned_authority_accounts owner WHERE owner.id=pub.account_id AND NOT owner.disabled) AND COALESCE(a.oauth_config->'allowed_scopes','[]'::jsonb) @> '["accounts.profile.board"]'::jsonb AND EXISTS(SELECT 1 FROM owned_app_widgets w WHERE w.app_id=a.id AND w.manifest->'is_enabled'='true'::jsonb) AND ($1::text='' OR lower(a.slug)=lower($1::text)) ORDER BY a.name COLLATE "C",a.slug COLLATE "C",a.id LIMIT $2::integer OFFSET $3::integer`, q.Slug, limit, offset)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var v apps.BoardDiscoveryApp
		var raw []byte
		if e = rows.Scan(&v.ID, &v.Slug, &v.Name, &v.Description, &v.PublisherName, &raw); e != nil {
			return nil, e
		}
		var manifests []board.Manifest
		if e = json.Unmarshal(raw, &manifests); e != nil {
			return nil, e
		}
		if len(manifests) > 32 {
			return nil, apps.ErrUnavailable
		}
		v.BoardWidgets = []apps.BoardDiscoveryWidget{}
		for _, w := range manifests {
			if !w.IsEnabled {
				continue
			}
			if w.FieldTypes == nil {
				w.FieldTypes = []board.FieldType{}
			}
			if w.RequiredFields == nil {
				w.RequiredFields = []string{}
			}
			v.BoardWidgets = append(v.BoardWidgets, apps.BoardDiscoveryWidget{Key: w.Key, Name: w.Name, Description: w.Description, Slug: v.PublisherName + "." + v.Slug + "." + w.Key, IsEnabled: true, RendererType: w.RendererType, FieldTypes: w.FieldTypes, RequiredFields: w.RequiredFields, MaxPayloadBytes: w.MaxPayloadBytes, AllowMultiple: w.AllowMultiple})
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
