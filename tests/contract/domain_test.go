package contract_test

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/passport/username"
	"solsynth.local/go-migration/ports/sphere"
)

func TestUsernameSourceContract(t *testing.T) {
	for input, want := range map[string]string{
		"": "", "A B.C+Dé": "abcd", "_123": "u_123", "你好": "", strings.Repeat("X", 31): strings.Repeat("x", 30),
	} {
		if got := username.Sanitize(input); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", input, got, want)
		}
	}
	var queries []string
	svc := username.Service{
		SearchAccounts: func(_ context.Context, query string) ([]username.Account, error) {
			queries = append(queries, query)
			return []username.Account{{Name: strings.ToUpper(query)}}, nil
		},
		RandIntn: func(n int) int {
			if n != 8999 {
				t.Fatalf("suffix range = %d", n)
			}
			return 8998
		},
		Now: func() time.Time { return time.UnixMilli(123456789) },
	}
	base := strings.Repeat("a", 30)
	got, err := svc.GenerateUnique(context.Background(), base)
	if err != nil || got != base+"123456789" {
		t.Fatalf("fallback = %q, %v", got, err)
	}
	if len(queries) != 11 {
		t.Fatalf("lookup count = %d; fallback must remain unchecked", len(queries))
	}
	for _, query := range queries[1:] {
		if query != base+"9998" {
			t.Errorf("suffix candidate = %q", query)
		}
	}
	// Email local-part extraction is independent of lookup transport.
	svc.SearchAccounts = func(_ context.Context, query string) ([]username.Account, error) { return nil, nil }
	got, err = svc.GenerateFromEmail(context.Background(), "A.B+tag@example.invalid")
	if err != nil || got != "abtag" {
		t.Fatalf("email result = %q, %v", got, err)
	}
}

func TestRecoverySourceContract(t *testing.T) {
	sequences := []int64{9, 2, 2, 0, -1}
	ranges := []messager.SequenceRange{{StartSequence: 7, EndSequence: 5}, {StartSequence: 8, EndSequence: 10}, {StartSequence: -1, EndSequence: 3}}
	plan := messager.NewRecoveryPlan(sequences, ranges)
	if !plan.UsesSequences() || !reflect.DeepEqual(plan.Sequences(), []int64{2, 9}) {
		t.Fatalf("sequences: %v", plan.Sequences())
	}
	want := []messager.SequenceRange{{StartSequence: 5, EndSequence: 10}}
	if !reflect.DeepEqual(plan.Ranges(), want) {
		t.Fatalf("ranges: %v", plan.Ranges())
	}
	for i := int64(-2); i < 13; i++ {
		if plan.MatchesSequence(i) != (i == 2 || i >= 5 && i <= 10) {
			t.Errorf("membership %d", i)
		}
	}
	sequences[0] = 500
	ranges[0].StartSequence = 500
	copyOfRanges := plan.Ranges()
	copyOfRanges[0].StartSequence = 500
	if !plan.MatchesSequence(5) {
		t.Fatal("caller mutation changed plan")
	}
	if messager.NewRecoveryPlan(nil, nil).UsesSequences() {
		t.Fatal("empty inputs must leave timestamp fallback selected")
	}
	encoded, err := json.Marshal(want[0])
	if err != nil || string(encoded) != `{"start_sequence":5,"end_sequence":10}` {
		t.Fatalf("range JSON: %s, %v", encoded, err)
	}
}

func TestTimelineBaseRankSourceContract(t *testing.T) {
	now := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	p := sphere.Post{CreatedAt: now, Type: "Article", ReactionScore: 10, ThreadRepliesCount: 5, AwardedScore: 20, RealmBoostLevel: 2}
	// 14 reactions + 4 replies + 2 awards + 1.5 article + 1.2 realm + 5 prior.
	if got := sphere.CalculateBaseRank(p, now); math.Abs(got-27.7) > 1e-10 {
		t.Fatalf("rank = %v", got)
	}
	published := now.Add(-time.Hour)
	p.PublishedAt = &published
	if got, want := sphere.CalculateBaseRank(p, now), 27.7/math.Pow(2, 1.2); math.Abs(got-want) > 1e-10 {
		t.Fatalf("published-at rank = %v, want %v", got, want)
	}
	// Preserve the source formula's exceptional value for far-future timestamps.
	future := now.Add(2 * time.Hour)
	p.PublishedAt = &future
	if !math.IsNaN(sphere.CalculateBaseRank(p, now)) {
		t.Fatal("far-future fractional power must remain NaN")
	}
}
