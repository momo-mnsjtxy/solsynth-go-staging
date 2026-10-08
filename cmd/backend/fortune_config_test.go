// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"flag"
	"io"
	"testing"
)

func TestFortuneConfigurationRequiresExplicitInputs(t *testing.T) {
	cases := []struct {
		args  []string
		valid bool
	}{
		{nil, true},
		{[]string{"--fortune-provider-url=https://provider.example/report"}, false},
		{[]string{"--enable-fortunes"}, false},
		{[]string{"--enable-fortunes", "--fortune-provider-url=http://provider.example/report", "--fortune-provider-revision=synthetic-v1"}, false},
		{[]string{"--enable-fortunes", "--fortune-provider-url=https://provider.example/report", "--fortune-provider-revision=synthetic-v1"}, true},
		{[]string{"--enable-fortunes", "--fortune-provider-url=https://provider.example/report", "--fortune-provider-revision=synthetic-v1", "--fortune-interval=0s"}, false},
	}
	for _, tc := range cases {
		f := flag.NewFlagSet("fixture", flag.ContinueOnError)
		f.SetOutput(io.Discard)
		s := fortuneFlags(f)
		if e := f.Parse(tc.args); e != nil {
			t.Fatal(e)
		}
		_, _, e := s.config()
		if (e == nil) != tc.valid {
			t.Fatalf("%v valid=%v error=%v", tc.args, tc.valid, e)
		}
	}
}
