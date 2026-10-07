package username

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSanitizeContract(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""}, {"Hello World!", "helloworld"}, {"123", "u123"},
		{"_HELLO-", "u_hello-"}, {"中文é🙂", ""}, {"AéB", "ab"},
		{strings.Repeat("A", 31), strings.Repeat("a", 30)},
		{"1" + strings.Repeat("A", 30), "u1" + strings.Repeat("a", 28)},
		{"A_B-C. D", "a_b-cd"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			if got := Sanitize(tc.in); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExistsExactCaseInsensitiveAndContext(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "sentinel")
	s := Service{SearchAccounts: func(got context.Context, query string) ([]Account, error) {
		if got != ctx || query != "alice" {
			t.Fatal("adapter arguments changed")
		}
		return []Account{{Name: "alice2"}, {Name: "ALICE"}}, nil
	}}
	if got, err := s.Exists(ctx, "alice"); err != nil || !got {
		t.Fatalf("%v %v", got, err)
	}
	s.SearchAccounts = func(context.Context, string) ([]Account, error) { return []Account{{Name: "alice2"}}, nil }
	if got, err := s.Exists(ctx, "alice"); err != nil || got {
		t.Fatalf("%v %v", got, err)
	}
}

func TestBaseAndEmailContract(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"", "user"}, {"@example.test", "user"}, {"A.B+tag@example.test", "abtag"},
		{"Name@extra@example.test", "name"}, {"NoAt", "noat"},
	} {
		s := Service{SearchAccounts: func(_ context.Context, query string) ([]Account, error) {
			if query != tc.want {
				t.Fatalf("queried %q, want %q", query, tc.want)
			}
			return nil, nil
		}, RandIntn: func(int) int { t.Fatal("random called for available base"); return 0 },
			Now: func() time.Time { t.Fatal("clock called for available base"); return time.Time{} }}
		got, err := s.GenerateFromEmail(context.Background(), tc.input)
		if err != nil || got != tc.want {
			t.Fatalf("%q: %q %v", tc.input, got, err)
		}
	}
}

func TestSuffixRangeAndFirstAvailable(t *testing.T) {
	var queries []string
	rands := []int{0, 8998}
	s := Service{SearchAccounts: func(_ context.Context, q string) ([]Account, error) {
		queries = append(queries, q)
		if q == "alice9998" {
			return nil, nil
		}
		return []Account{{Name: q}}, nil
	}, RandIntn: func(n int) int {
		if n != 8999 {
			t.Fatalf("random upper bound %d", n)
		}
		x := rands[0]
		rands = rands[1:]
		return x
	}}
	got, err := s.GenerateUnique(context.Background(), "Alice")
	if err != nil || got != "alice9998" {
		t.Fatalf("%q %v", got, err)
	}
	if !reflect.DeepEqual(queries, []string{"alice", "alice1000", "alice9998"}) {
		t.Fatal(queries)
	}
}

func TestTenAttemptsThenUncheckedMillisecondFallback(t *testing.T) {
	count, randomCalls, clockCalls := 0, 0, 0
	base := strings.Repeat("a", 30)
	s := Service{SearchAccounts: func(_ context.Context, q string) ([]Account, error) {
		count++
		return []Account{{Name: q}}, nil
	}, RandIntn: func(int) int { randomCalls++; return 0 },
		Now: func() time.Time { clockCalls++; return time.UnixMilli(1234567890123) }}
	got, err := s.GenerateUnique(context.Background(), base)
	if err != nil || got != base+"1234567890123" {
		t.Fatalf("%q %v", got, err)
	}
	if count != 11 || randomCalls != 10 || clockCalls != 1 {
		t.Fatalf("calls %d %d %d", count, randomCalls, clockCalls)
	}
}

func TestSuffixIsNotRetruncated(t *testing.T) {
	base := strings.Repeat("a", 30)
	s := Service{SearchAccounts: func(_ context.Context, q string) ([]Account, error) {
		if q == base {
			return []Account{{Name: q}}, nil
		}
		return nil, nil
	}, RandIntn: func(int) int { return 0 }}
	got, err := s.GenerateUnique(context.Background(), base)
	if err != nil || got != base+"1000" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestSearchErrorsPropagate(t *testing.T) {
	sentinel := errors.New("synthetic lookup failure")
	for _, failAt := range []int{1, 3} {
		calls := 0
		s := Service{SearchAccounts: func(_ context.Context, q string) ([]Account, error) {
			calls++
			if calls == failAt {
				return nil, sentinel
			}
			return []Account{{Name: q}}, nil
		}, RandIntn: func(int) int { return 0 }}
		got, err := s.GenerateUnique(context.Background(), "alice")
		if got != "" || err != sentinel || calls != failAt {
			t.Fatalf("%q %v calls=%d", got, err, calls)
		}
	}
	if _, err := (Service{}).Exists(context.Background(), "alice"); err != ErrSearchUnavailable {
		t.Fatal(err)
	}
}

func TestDefaultRandomAndClock(t *testing.T) {
	calls := 0
	s := Service{SearchAccounts: func(_ context.Context, query string) ([]Account, error) {
		calls++
		if calls > 1 {
			suffix, err := strconv.Atoi(strings.TrimPrefix(query, "alice"))
			if err != nil || suffix < 1000 || suffix >= 9999 {
				t.Fatalf("default random suffix out of bounds: %q", query)
			}
		}
		return []Account{{Name: query}}, nil
	}}
	before := time.Now().UnixMilli()
	got, err := s.GenerateUnique(context.Background(), "alice")
	after := time.Now().UnixMilli()
	ms, parseErr := strconv.ParseInt(strings.TrimPrefix(got, "alice"), 10, 64)
	if err != nil || parseErr != nil || ms < before || ms > after || calls != 11 {
		t.Fatalf("default clock result %q, %v, %v, calls=%d", got, err, parseErr, calls)
	}
}
