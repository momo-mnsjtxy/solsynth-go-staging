// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

type visibilityOnly struct{}

func (visibilityOnly) CanReadInTransaction(context.Context, *sql.Tx, string, string) (bool, error) {
	return false, nil
}

type visibilityReadiness struct {
	visibilityOnly
	calls int
	err   error
}

func (p *visibilityReadiness) Ready(context.Context) error { p.calls++; return p.err }

func TestAnyReadVisibilityPropagatesNestedReadiness(t *testing.T) {
	first, second := &visibilityReadiness{}, &visibilityReadiness{}
	combined := AnyReadVisibility{nil, visibilityOnly{}, first, AnyReadVisibility{second}}
	ready, ok := any(combined).(interface{ Ready(context.Context) error })
	if !ok {
		t.Fatal("composed visibility hides required policy readiness")
	}
	if e := ready.Ready(context.Background()); e != nil || first.calls != 1 || second.calls != 1 {
		t.Fatal("composed readiness did not inspect every policy", e, first.calls, second.calls)
	}
	missingSchema := errors.New("synthetic missing association column")
	second.err = missingSchema
	if e := ready.Ready(context.Background()); !errors.Is(e, missingSchema) {
		t.Fatal("nested missing schema was hidden", e)
	}
	first.err = missingSchema
	before := second.calls
	if e := ready.Ready(context.Background()); !errors.Is(e, missingSchema) || second.calls != before {
		t.Fatal("first unavailable policy was not returned", e, second.calls)
	}
}
