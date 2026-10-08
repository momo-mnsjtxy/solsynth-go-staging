// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"os"
	"solsynth.local/go-migration/ports/passport/board"
	"testing"
)

func TestExampleFixtureIsExplicitAndValid(t *testing.T) {
	file, err := os.Open("fixture.example.json")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	config, err := board.DecodeSyntheticConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = board.NewSyntheticFixture(config); err != nil {
		t.Fatal(err)
	}
	if len(config.Principals) != 3 || !config.Synthetic {
		t.Fatal("example fixture lost explicit principals")
	}
}
