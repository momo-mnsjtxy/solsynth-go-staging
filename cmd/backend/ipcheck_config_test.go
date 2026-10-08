// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestIPCheckExplicitDatasetAndProxyConfiguration(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		valid bool
	}{
		{nil, true},
		{[]string{"--ipcheck-forwarded-header=X-Forwarded-For"}, false},
		{[]string{"--ipcheck-trusted-proxies=10.0.0.0/8"}, false},
		{[]string{"--ipcheck-trusted-proxies=0.0.0.0/0", "--ipcheck-forwarded-header=X-Forwarded-For"}, false},
		{[]string{"--ipcheck-trusted-proxies=10.0.0.0/8", "--ipcheck-forwarded-header=X-Forwarded-For"}, true},
	} {
		fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		s := ipCheckFlags(fs)
		if e := fs.Parse(tc.args); e != nil {
			t.Fatal(e)
		}
		_, e := s.config()
		if (e == nil) != tc.valid {
			t.Fatalf("%v error=%v", tc.args, e)
		}
	}
	path := filepath.Join(t.TempDir(), "synthetic-geo.json")
	if e := os.WriteFile(path, []byte(`{"revision":"synthetic","provenance":"Independently authored synthetic record","entries":[{"prefix":"198.51.100.0/24","geo":{"city":"Synthetic","latitude":0,"longitude":0}}]}`), 0600); e != nil {
		t.Fatal(e)
	}
	fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
	s := ipCheckFlags(fs)
	if e := fs.Parse([]string{"--ipcheck-geo-file=" + path}); e != nil {
		t.Fatal(e)
	}
	c, e := s.config()
	if e != nil || c.Provider == nil {
		t.Fatal("explicit dataset not loaded", e)
	}
}
