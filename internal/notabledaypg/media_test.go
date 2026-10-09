// SPDX-License-Identifier: AGPL-3.0-only
package notabledaypg

import (
	"solsynth.local/go-migration/ports/passport/notabledays"
	"testing"
	"time"
)

func TestMediaCreationFingerprintCompatibilityAndNormalization(t *testing.T) {
	body := notabledays.EventBody{Title: "Synthetic", StartTime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), TimeZone: "UTC"}
	old, e := fingerprint(body)
	if e != nil {
		t.Fatal(e)
	}
	hash := func(icon, background *string) string {
		v, e := fingerprint(struct {
			notabledays.EventBody
			IconID       *string `json:"icon_id,omitempty"`
			BackgroundID *string `json:"background_id,omitempty"`
		}{body, icon, background})
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	if hash(nil, nil) != old {
		t.Fatal("existing media-free retries would conflict")
	}
	id := "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
	p, e := normalizeMediaPatch(notabledays.EventPatch{IconID: &id}, true)
	if e != nil || *p.IconID == id {
		t.Fatal(p, e)
	}
	if hash(p.IconID, nil) == old || hash(p.IconID, nil) == hash(nil, p.IconID) {
		t.Fatal("slot not bound to request")
	}
	for _, bad := range []string{"", "https://example.invalid/file", "../file"} {
		if _, e := normalizeMediaPatch(notabledays.EventPatch{IconID: &bad}, true); e != notabledays.ErrInvalid {
			t.Fatal(bad, e)
		}
	}
	if _, e := normalizeMediaPatch(notabledays.EventPatch{IconID: &id}, false); e != notabledays.ErrUnsupported {
		t.Fatal(e)
	}
	if _, e := normalizeMediaPatch(notabledays.EventPatch{IconPresent: true}, true); e != nil {
		t.Fatal("explicit null unsupported", e)
	}
}
