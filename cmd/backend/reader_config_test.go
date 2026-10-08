// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"flag"
	"io"
	"testing"
)

func TestReaderConfigurationRequiresOutboundOptIn(t *testing.T) {
	for _, tc := range []struct {
		args         []string
		valid, fetch bool
	}{
		{nil, true, false},
		{[]string{"--reader-fetch"}, false, false},
		{[]string{"--enable-reader"}, false, false},
		{[]string{"--enable-reader", "--reader-origins=https://preview.example"}, true, false},
		{[]string{"--enable-reader", "--reader-origins=https://preview.example", "--reader-fetch"}, true, true},
		{[]string{"--enable-reader", "--reader-origins=http://preview.example", "--reader-fetch"}, false, false},
	} {
		fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		s := readerFlags(fs)
		if e := fs.Parse(tc.args); e != nil {
			t.Fatal(e)
		}
		c, e := s.config()
		if (e == nil) != tc.valid {
			t.Fatalf("%v error=%v", tc.args, e)
		}
		if e == nil && c != nil && (c.Fetcher != nil) != tc.fetch {
			t.Fatal("implicit outbound adapter")
		}
	}
}
