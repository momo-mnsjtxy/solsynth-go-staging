// SPDX-License-Identifier: AGPL-3.0-only
package locations

import (
	"context"
	"strings"
)

type GeocodeRequest struct {
	Query    string `json:"query"`
	Language string `json:"language,omitempty"`
}
type GeocodePlace struct {
	Name      string  `json:"name"`
	Address   string  `json:"address"`
	Longitude float64 `json:"longitude"`
	Latitude  float64 `json:"latitude"`
}
type Geocoder interface {
	Geocode(context.Context, GeocodeRequest) ([]GeocodePlace, error)
}
type GeocodingRepository interface {
	Geocode(context.Context, string, GeocodeRequest) ([]GeocodePlace, error)
}

func ValidateGeocode(v GeocodeRequest) error {
	if strings.TrimSpace(v.Query) == "" || !Text(&v.Query, 256) || len(v.Language) > 35 {
		return ErrInvalid
	}
	for _, c := range v.Language {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return ErrInvalid
		}
	}
	return nil
}
