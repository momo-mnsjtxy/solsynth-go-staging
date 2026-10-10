// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"flag"
	"io"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/application"
	"testing"
)

func TestAccountExtrasExplicitSettlementConfiguration(t *testing.T) {
	for _, tc := range []struct {
		args           []string
		valid, enabled bool
	}{
		{nil, true, false},
		{[]string{"--name-card-currency=synthetic"}, false, false},
		{[]string{"--enable-name-cards"}, false, false},
		{[]string{"--enable-name-cards", "--name-card-currency=synthetic", "--name-card-payee-wallet=10000000-0000-4000-8000-000000000001"}, true, true},
		{[]string{"--enable-name-cards", "--name-card-currency=synthetic", "--name-card-payee-wallet=10000000-0000-4000-8000-000000000001", "--name-card-price=0"}, false, false},
		{[]string{"--rewind-reporting-year=1800"}, false, false},
		{[]string{"--rewind-reporting-year=2026"}, true, false},
	} {
		fs := flag.NewFlagSet("synthetic", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		opts := accountExtraFlags(fs)
		if e := fs.Parse(tc.args); e != nil {
			t.Fatal(e)
		}
		var c application.Config
		e := opts.apply(&c)
		if (e == nil) != tc.valid {
			t.Fatal(tc.args, e)
		}
		if e == nil && (c.NameCards != nil) != tc.enabled {
			t.Fatal("unexpected paid enablement")
		}
	}
}

func TestRewindActualDictionaryAndCanonicalActivityConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dictionary.txt")
	if err := os.WriteFile(path, []byte("研究 100\n生命 100\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--rewind-stop-words=" + path},
		{"--rewind-max-tokens=1"},
		{"--rewind-dictionary=relative.txt"},
		{"--rewind-dictionary=" + path, "--rewind-max-tokens=0"},
	} {
		fs := flag.NewFlagSet("synthetic", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		options := accountExtraFlags(fs)
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		if err := options.apply(&application.Config{}); err == nil {
			t.Fatal("invalid dictionary config", args)
		}
	}
	fs := flag.NewFlagSet("synthetic", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	options := accountExtraFlags(fs)
	if err := fs.Parse([]string{"--enable-rewind-action-activity", "--rewind-dictionary=" + path, "--rewind-max-tokens=100"}); err != nil {
		t.Fatal(err)
	}
	var config application.Config
	if err := options.apply(&config); err != nil {
		t.Fatal(err)
	}
	if config.Rewind == nil || !config.Rewind.CanonicalActionActivity || config.Rewind.Tokenizer == nil {
		t.Fatal("actual adapters not composed")
	}
	words, err := config.Rewind.Tokenizer.TopWords(context.Background(), []string{"研究生命研究生命"}, 100)
	if err != nil || len(words) != 2 || words[0].Count != 2 || words[1].Count != 2 {
		t.Fatal(words, err)
	}
}
