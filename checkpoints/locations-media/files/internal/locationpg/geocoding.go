// SPDX-License-Identifier: AGPL-3.0-only
package locationpg

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/ports/federation"
	"solsynth.local/go-migration/ports/sphere/locations"
)

func (r *Repository) WithGeocoder(provider locations.Geocoder) *Repository {
	r.geocoder = provider
	return r
}
func (r *Repository) Geocode(ctx context.Context, actor string, v locations.GeocodeRequest) ([]locations.GeocodePlace, error) {
	if e := locations.ValidateGeocode(v); e != nil {
		return nil, e
	}
	if r.geocoder == nil {
		return nil, locations.ErrUnavailable
	}
	tx, _, e := r.begin(ctx, actor, "locations.geocode", true)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	var valid bool
	e = tx.QueryRowContext(ctx, `INSERT INTO owned_location_geocode_limits(account_id,last_at) VALUES($1,clock_timestamp()) ON CONFLICT(account_id) DO UPDATE SET last_at=clock_timestamp() WHERE owned_location_geocode_limits.last_at<=clock_timestamp()-interval '5 seconds' RETURNING true`, actor).Scan(&valid)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, locations.ErrRateLimit
	}
	if e != nil {
		return nil, e
	}
	if e = r.recheck(ctx, tx, actor, "locations.geocode"); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	out, e := r.geocoder.Geocode(ctx, v)
	if e != nil {
		return nil, locations.ErrUnavailable
	}
	if e = validatePlaces(out); e != nil {
		return nil, e
	}
	// The provider request is external and may outlive the original credential.
	tx, _, e = r.begin(ctx, actor, "locations.geocode", true)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if e = r.recheck(ctx, tx, actor, "locations.geocode"); e != nil {
		return nil, e
	}
	return out, tx.Commit()
}
func validatePlaces(out []locations.GeocodePlace) error {
	if len(out) > 10 {
		return locations.ErrUnavailable
	}
	for _, v := range out {
		if !locations.Text(&v.Name, 256) || !locations.Text(&v.Address, 1024) {
			return locations.ErrUnavailable
		}
		if _, e := locations.ParsePoint((locations.Point{Longitude: v.Longitude, Latitude: v.Latitude}).WKT()); e != nil {
			return locations.ErrUnavailable
		}
	}
	return nil
}

// HTTPSGeocoder implements an owned provider contract: GET the configured
// endpoint with encoded q/language, returning {"places":[name,address,longitude,
// latitude]}. It shares the pinned-public-DNS/redirect policy of remote media.
type HTTPSGeocoder struct {
	endpoint *url.URL
	fetcher  *mediapg.HTTPSRemoteFetcher
}

func NewHTTPSGeocoder(endpoint string) (*HTTPSGeocoder, error) {
	u, e := url.Parse(endpoint)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return nil, locations.ErrInvalid
	}
	f, e := mediapg.NewHTTPSRemoteFetcher([]string{u.Hostname()})
	if e != nil {
		return nil, locations.ErrInvalid
	}
	return &HTTPSGeocoder{u, f}, nil
}
func (g *HTTPSGeocoder) Geocode(ctx context.Context, v locations.GeocodeRequest) ([]locations.GeocodePlace, error) {
	if e := locations.ValidateGeocode(v); e != nil {
		return nil, e
	}
	u := *g.endpoint
	q := url.Values{"q": {v.Query}}
	if v.Language != "" {
		q.Set("language", v.Language)
	}
	u.RawQuery = q.Encode()
	body, e := g.fetcher.Fetch(ctx, u.String(), (64<<10)+1)
	if e != nil {
		return nil, locations.ErrUnavailable
	}
	defer body.Close()
	raw, e := io.ReadAll(io.LimitReader(body, (64<<10)+1))
	if e != nil || len(raw) > 64<<10 || federation.CheckJSON(raw) != nil {
		return nil, locations.ErrUnavailable
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var out struct {
		Places *[]struct {
			Name      string   `json:"name"`
			Address   string   `json:"address"`
			Longitude *float64 `json:"longitude"`
			Latitude  *float64 `json:"latitude"`
		} `json:"places"`
	}
	if d.Decode(&out) != nil || d.Decode(new(any)) != io.EOF || out.Places == nil || len(*out.Places) > 10 {
		return nil, locations.ErrUnavailable
	}
	places := []locations.GeocodePlace{}
	for _, v := range *out.Places {
		if v.Longitude == nil || v.Latitude == nil {
			return nil, locations.ErrUnavailable
		}
		places = append(places, locations.GeocodePlace{Name: v.Name, Address: v.Address, Longitude: *v.Longitude, Latitude: *v.Latitude})
	}
	if validatePlaces(places) != nil {
		return nil, locations.ErrUnavailable
	}
	return places, nil
}
