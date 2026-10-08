// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"flag"
	"solsynth.local/go-migration/internal/application"
	"testing"
)

func TestLivestreamAwardConfiguration(t *testing.T) {
	binding := []string{"--livestream-award-currency=synthetic", "--livestream-award-treasury-account=00000000-0000-4000-8000-000000000001", "--livestream-award-treasury-wallet=00000000-0000-4000-8000-000000000002"}
	for _, tt := range []struct {
		name        string
		args        []string
		streams, ok bool
	}{
		{"off", nil, false, true}, {"missing binding", []string{"--enable-livestream-awards"}, true, false},
		{"requires streams", append([]string{"--enable-livestream-awards"}, binding...), false, false},
		{"creation", append([]string{"--enable-livestream-awards"}, binding...), true, true},
		{"provider-free maintenance", append([]string{"--livestream-awards-maintenance-only"}, binding...), false, true},
		{"maintenance excludes streams", append([]string{"--livestream-awards-maintenance-only"}, binding...), true, false},
		{"mutual exclusion", append([]string{"--enable-livestream-awards", "--livestream-awards-maintenance-only"}, binding...), true, false},
		{"exact split", append([]string{"--enable-livestream-awards", "--livestream-award-max-amount=0.001"}, binding...), true, false},
		{"disabled binding", binding, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fs := flag.NewFlagSet(tt.name, flag.ContinueOnError)
			s := livestreamAwardFlags(fs)
			if e := fs.Parse(tt.args); e != nil {
				t.Fatal(e)
			}
			c := application.Config{}
			if tt.streams {
				c.Livestreams = &application.LivestreamConfig{}
			}
			e := s.apply(&c)
			if (e == nil) != tt.ok {
				t.Fatal(e)
			}
			if tt.ok && *s.maintenance && (c.LivestreamAwardMaintenance == nil || c.Livestreams != nil) {
				t.Fatal("maintenance not isolated")
			}
		})
	}
}
