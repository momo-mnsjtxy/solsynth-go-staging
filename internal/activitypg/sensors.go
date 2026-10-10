// SPDX-License-Identifier: AGPL-3.0-only
package activitypg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/activities"
)

type ProviderObserver interface {
	Observe(context.Context, string) (activities.Content, bool, error)
}

func (r *Repository) WithSteamProvider(p ProviderObserver) *Repository {
	return r.WithProvider("steam", p)
}
func (r *Repository) WithProvider(name string, p ProviderObserver) *Repository {
	if r.providers == nil {
		r.providers = map[string]ProviderObserver{}
	}
	if p != nil {
		r.providers[name] = p
	}
	return r
}
func sensorLease(visibility *int, minutes int) error {
	if visibility == nil || *visibility < 0 || *visibility > 2 || minutes < 1 || minutes > 60 {
		return activities.ErrInvalid
	}
	return nil
}
func (r *Repository) ObserveSensor(ctx context.Context, actor string, v activities.SensorRequest) (activities.Activity, error) {
	if e := sensorLease(v.Visibility, v.LeaseMinutes); e != nil {
		return activities.Activity{}, e
	}
	switch v.Sensor {
	case "music", "gaming", "coding", "browser":
	default:
		return activities.Activity{}, activities.ErrInvalid
	}
	provider := "owned.sensor." + v.Sensor
	manual := "sensor:" + v.Sensor
	v.Content.Provider = &provider
	return r.Set(ctx, actor, activities.Request{Content: v.Content, ManualID: &manual, LeaseMinutes: v.LeaseMinutes, Visibility: *v.Visibility, VisibilitySpecified: true}, false, "")
}
func (r *Repository) ObserveProvider(ctx context.Context, actor string, v activities.ProviderRequest) (activities.Activity, error) {
	if r == nil || r.providers[v.Provider] == nil {
		return activities.Activity{}, activities.ErrUnavailable
	}
	if v.ConsentMinutes < 0 || v.ConsentMinutes > 1440 {
		return activities.Activity{}, activities.ErrInvalid
	}
	if !authority.ValidID(v.ConnectionID) {
		return activities.Activity{}, activities.ErrInvalid
	}
	if e := sensorLease(v.Visibility, v.LeaseMinutes); e != nil {
		return activities.Activity{}, e
	}
	tx, _, _, e := r.begin(ctx, actor, actor, activities.ManagePermission)
	if e != nil {
		return activities.Activity{}, e
	}
	defer tx.Rollback()
	var id string
	e = tx.QueryRowContext(ctx, `SELECT provided_identifier FROM owned_account_connections WHERE id=$1::uuid AND account_id=$2::uuid AND provider=$3::text AND revoked_at IS NULL AND verified_at<=clock_timestamp()`, v.ConnectionID, actor, v.Provider).Scan(&id)
	if e == sql.ErrNoRows {
		return activities.Activity{}, activities.ErrNotFound
	}
	if e != nil {
		return activities.Activity{}, e
	}
	tx.Rollback()
	content, active, e := r.providers[v.Provider].Observe(ctx, id)
	if e != nil {
		return activities.Activity{}, e
	}
	manual := "provider:" + v.Provider + ":" + v.ConnectionID
	// Verify the same canonical link again after provider I/O; original HTTP
	// proof remains in ctx and Set revalidates it after all canonical locks.
	tx, _, _, e = r.begin(ctx, actor, actor, activities.ManagePermission)
	if e != nil {
		return activities.Activity{}, e
	}
	var current string
	e = tx.QueryRowContext(ctx, `SELECT provided_identifier FROM owned_account_connections WHERE id=$1::uuid AND account_id=$2::uuid AND provider=$3::text AND revoked_at IS NULL`, v.ConnectionID, actor, v.Provider).Scan(&current)
	tx.Rollback()
	if e != nil || current != id {
		return activities.Activity{}, activities.ErrNotFound
	}
	if !active {
		provider, kind, title := v.Provider, "presence", "Idle"
		content = activities.Content{Provider: &provider, Type: &kind, Title: &title}
	}
	out, e := r.Set(ctx, actor, activities.Request{Content: content, ManualID: &manual, LeaseMinutes: v.LeaseMinutes, Visibility: *v.Visibility, VisibilitySpecified: true, TrustedConnectionID: v.ConnectionID, ProviderIdentifier: id, TrustedProvider: v.Provider}, false, "")
	if e != nil {
		return out, e
	}
	if v.ConsentMinutes > 0 {
		e = r.enrollProvider(ctx, actor, out.ID, v)
	}
	return out, e
}
