// SPDX-License-Identifier: AGPL-3.0-only
package locations

import (
	"math"
	"testing"
)

func TestPointParsingAndDatelineDistance(t *testing.T) {
	for _, s := range []string{"POINT(181 0)", "POINT(0 91)", "POINT(NaN 0)", "POINT(1e999 0)", "POINT(1 2 3)", "LINESTRING(0 0,1 1)", "SRID=4326;POINT(0 0)"} {
		if _, e := ParsePoint(s); e == nil {
			t.Fatal("accepted", s)
		}
	}
	p, e := ParsePoint(" point ( 1.5 -2.25 ) ")
	if e != nil || p.WKT() != "POINT(1.5 -2.25)" {
		t.Fatal(p, e)
	}
	d := Distance(Point{179.999, 0}, Point{-179.999, 0})
	if math.Abs(d-222.39) > 1 {
		t.Fatal("dateline distance", d)
	}
}
func TestPrivateDefaultsAndPlainTagBoundary(t *testing.T) {
	if Public != 0 || Private != 1 || Unlisted != 2 || PinOffline != 1 || Completed != 1 || Expired != 2 {
		t.Fatal("authored enum drift")
	}
	if _, e := NormalizeUID("04aabbcc0011"); e != nil {
		t.Fatal(e)
	}
	for _, v := range []string{"solian://phpass?picc_data=opaque", "AAB", "test-not-a-uid"} {
		if _, e := NormalizeUID(v); e == nil {
			t.Fatal("unvalidated NFC reference", v)
		}
	}
	image := "anything"
	if e := ValidateMeet(CreateMeet{ImageID: &image}); e != ErrUnsupported {
		t.Fatal("unimplemented media accepted", e)
	}
}
