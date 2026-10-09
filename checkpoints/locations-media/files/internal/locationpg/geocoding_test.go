// SPDX-License-Identifier: AGPL-3.0-only
package locationpg

import (
	"math"
	"solsynth.local/go-migration/ports/sphere/locations"
	"testing"
)

func TestGeocoderEndpointAndResponseBounds(t *testing.T) {
	for _, endpoint := range []string{"http://geo.example/api", "https://127.0.0.1/api", "https://geo.example:444/api", "https://user:secret@geo.example/api", "https://geo.example/api?token=secret", "https://geo.example/api#fragment"} {
		if _, e := NewHTTPSGeocoder(endpoint); e == nil {
			t.Fatal("unsafe endpoint", endpoint)
		}
	}
	if _, e := NewHTTPSGeocoder("https://geo.example/api"); e != nil {
		t.Fatal(e)
	}
	for _, p := range []locations.GeocodePlace{{Longitude: 181}, {Latitude: -91}, {Longitude: math.NaN()}, {Name: "\x00"}} {
		if validatePlaces([]locations.GeocodePlace{p}) == nil {
			t.Fatal("unsafe geocoding result", p)
		}
	}
}
