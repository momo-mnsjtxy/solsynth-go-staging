// SPDX-License-Identifier: AGPL-3.0-only
// Source-informed from Solsynth/DysonNetwork, revision
// 4ee2d0cebb9da8284a92483fee6dce795bf6947c. Modified 2026-10-07.

// Package develop defines a bounded app-discovery compatibility candidate.
// Shared global policy and Production enum semantics remain unverified.
package develop

import (
	"context"
	"errors"
)

type Query struct {
	Take   int32
	Offset int32
	Search *string
}

type AppDiscovery struct {
	ID            string  `json:"id"`
	Slug          string  `json:"slug"`
	Title         string  `json:"title"`
	Description   *string `json:"description"`
	ProductsCount int32   `json:"products_count"`
	WidgetsCount  int32   `json:"widgets_count"`
}

type Page struct {
	Apps  []AppDiscovery
	Total int32
}

type Reader interface {
	Discover(context.Context, Query) (Page, error)
}

var ErrInvalidQuery = errors.New("invalid discovery query")

// ValidateQuery applies candidate bounds, not the excluded upstream pagination
// filter. The licensed controller establishes defaults but not these limits.
func ValidateQuery(q Query) error {
	if q.Take < 0 || q.Take > 100 || q.Offset < 0 {
		return ErrInvalidQuery
	}
	return nil
}
