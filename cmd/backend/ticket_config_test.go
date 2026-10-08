// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"flag"
	"testing"
)

func TestExamPolicyRequiresExplicitBoundedCanonicalMappings(t *testing.T) {
	for _, tc := range []struct {
		args []string
		ok   bool
	}{
		{nil, true},
		{[]string{"--exam-group-mappings=graduates=12345678-1234-4234-9234-123456789012"}, true},
		{[]string{"--exam-group-mappings=graduates=not-a-group"}, false},
		{[]string{"--exam-required-tests=entry"}, false},
		{[]string{"--exam-activation-policy", "--exam-required-tests=entry"}, true},
		{[]string{"--exam-activation-policy", "--exam-require-verified-contact=false"}, false},
		{[]string{"--exam-activation-policy", "--exam-required-tests=entry,ENTRY"}, false},
	} {
		f := flag.NewFlagSet("synthetic", flag.ContinueOnError)
		o := ticketFlags(f)
		if e := f.Parse(tc.args); e != nil {
			t.Fatal(e)
		}
		_, e := o.config()
		if (e == nil) != tc.ok {
			t.Fatalf("%v: %v", tc.args, e)
		}
	}
}
