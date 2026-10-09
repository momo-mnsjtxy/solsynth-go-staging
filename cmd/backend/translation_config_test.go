// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"flag"
	"io"
	"testing"
)

func TestTranslationConfigurationRequiresExplicitGrantAndSend(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		valid bool
	}{
		{nil, true},
		{[]string{"--translation-send"}, false},
		{[]string{"--enable-translation"}, false},
		{[]string{"--enable-translation", "--translation-subscriptions=synthetic-plan"}, true},
		{[]string{"--enable-translation", "--translation-subscriptions=synthetic-plan", "--translation-region=ap-test"}, false},
		{[]string{"--enable-translation", "--translation-subscriptions=synthetic-plan", "--translation-send"}, false},
	} {
		fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		s := translationFlags(fs)
		if e := fs.Parse(tc.args); e != nil {
			t.Fatal(e)
		}
		c, e := s.config()
		if (e == nil) != tc.valid {
			t.Fatalf("%v error=%v", tc.args, e)
		}
		if e == nil && c != nil && c.Provider != nil {
			t.Fatal("implicit outbound provider")
		}
	}
}
