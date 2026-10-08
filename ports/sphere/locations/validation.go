// SPDX-License-Identifier: AGPL-3.0-only
package locations

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"solsynth.local/go-migration/ports/sphere/taxonomy"
	"strconv"
	"strings"
	"unicode/utf8"
)

var pointRE = regexp.MustCompile(`(?i)^POINT\s*\(\s*([+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[Ee][+-]?\d+)?)\s+([+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[Ee][+-]?\d+)?)\s*\)$`)
var uidRE = regexp.MustCompile(`^[A-F0-9]{8,32}$`)

func ParsePoint(s string) (Point, error) {
	m := pointRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Point{}, ErrInvalid
	}
	lon, e := strconv.ParseFloat(m[1], 64)
	if e != nil {
		return Point{}, ErrInvalid
	}
	lat, e := strconv.ParseFloat(m[2], 64)
	if e != nil || math.IsNaN(lon) || math.IsInf(lon, 0) || math.IsNaN(lat) || math.IsInf(lat, 0) || lon < -180 || lon > 180 || lat < -90 || lat > 90 {
		return Point{}, ErrInvalid
	}
	return Point{lon, lat}, nil
}
func (p Point) WKT() string {
	return fmt.Sprintf("POINT(%s %s)", strconv.FormatFloat(p.Longitude, 'f', -1, 64), strconv.FormatFloat(p.Latitude, 'f', -1, 64))
}
func Distance(a, b Point) float64 {
	lat1, lat2 := a.Latitude*math.Pi/180, b.Latitude*math.Pi/180
	dlat := (b.Latitude - a.Latitude) * math.Pi / 180
	dlon := (b.Longitude - a.Longitude) * math.Pi / 180
	v := math.Sin(dlat/2)*math.Sin(dlat/2) + math.Cos(lat1)*math.Cos(lat2)*math.Sin(dlon/2)*math.Sin(dlon/2)
	return 6371008.8 * 2 * math.Asin(math.Sqrt(math.Min(1, math.Max(0, v))))
}
func Text(s *string, n int) bool {
	return s == nil || utf8.ValidString(*s) && !strings.ContainsRune(*s, 0) && taxonomy.UTF16Length(*s) <= n
}
func ValidateLocation(name, address, wkt *string, metadata map[string]json.RawMessage) error {
	if !Text(name, 256) || !Text(address, 1024) || !Text(wkt, 128) {
		return ErrInvalid
	}
	if wkt != nil && strings.TrimSpace(*wkt) != "" {
		if _, e := ParsePoint(*wkt); e != nil {
			return e
		}
	}
	if metadata != nil {
		b, e := json.Marshal(metadata)
		if e != nil || len(b) > 8192 {
			return ErrInvalid
		}
		for k := range metadata {
			if len(k) > 128 || strings.ContainsRune(k, 0) {
				return ErrInvalid
			}
		}
	}
	return nil
}
func ValidateMeet(v CreateMeet) error {
	if v.Visibility != nil && (*v.Visibility < 0 || *v.Visibility > 2) || !Text(v.Notes, 8192) || v.ExpiresInSeconds != nil && (*v.ExpiresInSeconds < 60 || *v.ExpiresInSeconds > 86400) {
		return ErrInvalid
	}
	if v.ImageID != nil && *v.ImageID != "" {
		return ErrUnsupported
	}
	return ValidateLocation(v.LocationName, v.LocationAddress, v.LocationWKT, v.Metadata)
}
func ValidatePin(v PinRequest) error {
	if v.Visibility != nil && (*v.Visibility < 0 || *v.Visibility > 2) {
		return ErrInvalid
	}
	return ValidateLocation(v.LocationName, v.LocationAddress, v.LocationWKT, v.Metadata)
}
func NormalizeUID(uid string) (string, error) {
	uid = strings.ToUpper(strings.TrimSpace(uid))
	if !uidRE.MatchString(uid) || len(uid)%2 != 0 {
		return "", ErrInvalid
	}
	return uid, nil
}
