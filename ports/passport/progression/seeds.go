// SPDX-License-Identifier: AGPL-3.0-only
package progression

import (
	"context"
	_ "embed"
	"encoding/json"
	"time"
)

//go:embed catalog.json
var seedCatalog []byte

type Seed struct {
	Kind       string     `json:"kind"`
	Definition Definition `json:"definition"`
}

func Seeds() ([]Seed, error) { var out []Seed; e := json.Unmarshal(seedCatalog, &out); return out, e }

type ExtendedRepository interface {
	BackdatedCheckIn(context.Context, string, time.Time) (CheckIn, error)
	SyncSeeds(context.Context, string) (int, error)
}
