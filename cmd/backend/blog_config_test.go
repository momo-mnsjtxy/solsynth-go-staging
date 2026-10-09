// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"flag"
	"testing"
)

func TestBlogConfigurationRequiresExplicitVerificationOptIn(t *testing.T) {
	for _, tc := range []struct {
		args                   []string
		valid, enabled, verify bool
	}{
		{nil, true, false, false}, {[]string{"--enable-blogs"}, true, true, false},
		{[]string{"--blog-domain-verify"}, false, false, false},
		{[]string{"--enable-blogs", "--blog-domain-verify"}, false, false, false},
		{[]string{"--enable-blogs", "--blog-domain-origins=https://blog.example"}, false, false, false},
		{[]string{"--enable-blogs", "--blog-domain-verify", "--blog-domain-origins=https://blog.example"}, true, true, true},
		{[]string{"--enable-blogs", "--blog-domain-verify", "--blog-domain-origins=https://127.0.0.1"}, false, false, false},
	} {
		f := flag.NewFlagSet("blogs", flag.ContinueOnError)
		s := blogFlags(f)
		if e := f.Parse(tc.args); e != nil {
			t.Fatal(e)
		}
		c, e := s.config()
		if (e == nil) != tc.valid {
			t.Fatal(tc.args, e)
		}
		if e == nil && ((c != nil) != tc.enabled || c != nil && (c.Verifier != nil) != tc.verify) {
			t.Fatal(tc.args, c)
		}
	}
}
