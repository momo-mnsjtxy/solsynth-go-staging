// Package username ports Passport's username generation utility.
// It is not an account service, uniqueness reservation, or authentication layer.
// Derived from Solsynth/DysonNetwork under AGPL-3.0; see PROVENANCE.md.
package username

import (
	"context"
	"errors"
	"math/rand"
	"strconv"
	"strings"
	"time"
)

// Account is the name projection returned by the existing account search RPC.
type Account struct{ Name string }

// SearchFunc adapts DyAccountService.SearchAccount, including its existing
// search limits and matching behavior. Errors must be returned unchanged.
type SearchFunc func(context.Context, string) ([]Account, error)

// Service injects the account search, random source, and clock for testing.
// RandIntn must obey math/rand.Intn's [0,n) contract. Custom dependencies are
// responsible for their own concurrency safety.
type Service struct {
	SearchAccounts SearchFunc
	RandIntn       func(int) int
	Now            func() time.Time
}

// ErrSearchUnavailable reports a missing account-service adapter.
var ErrSearchUnavailable = errors.New("username: account search adapter is required")

// Sanitize removes everything outside ASCII letters, digits, underscore and
// hyphen, lowercases, prefixes non-letter starts with u, and truncates to 30.
// Empty input represents both null and empty strings from the C# boundary.
func Sanitize(value string) string {
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			b.WriteByte(c)
		}
	}
	name := b.String()
	if len(name) > 0 && (name[0] < 'a' || name[0] > 'z') {
		name = "u" + name
	}
	if len(name) > 30 {
		name = name[:30]
	}
	return name
}

// Exists searches first and then requires a case-insensitive exact name match.
// Valid generated names are ASCII. Arbitrary Unicode name comparison parity
// with .NET OrdinalIgnoreCase is outside this utility's verified contract.
func (s Service) Exists(ctx context.Context, name string) (bool, error) {
	if s.SearchAccounts == nil {
		return false, ErrSearchUnavailable
	}
	accounts, err := s.SearchAccounts(ctx, name)
	if err != nil {
		return false, err
	}
	for _, account := range accounts {
		if strings.EqualFold(account.Name, name) {
			return true, nil
		}
	}
	return false, nil
}

// GenerateUnique preserves the upstream sequence: base, ten random suffixes,
// then an unchecked Unix-millisecond suffix. It does not reserve the result.
func (s Service) GenerateUnique(ctx context.Context, base string) (string, error) {
	name := Sanitize(base)
	if name == "" {
		name = "user"
	}
	exists, err := s.Exists(ctx, name)
	if err != nil {
		return "", err
	}
	if !exists {
		return name, nil
	}
	intn := s.RandIntn
	if intn == nil {
		intn = rand.Intn
	}
	for i := 0; i < 10; i++ {
		candidate := name + strconv.Itoa(1000+intn(8999))
		exists, err := s.Exists(ctx, candidate)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
	}
	now := s.Now
	if now == nil {
		now = time.Now
	}
	return name + strconv.FormatInt(now().UnixMilli(), 10), nil
}

// GenerateFromEmail uses only the text preceding the first @. No email
// validation is introduced; this preserves the upstream utility's behavior.
func (s Service) GenerateFromEmail(ctx context.Context, email string) (string, error) {
	if email == "" {
		return s.GenerateUnique(ctx, "user")
	}
	local, _, _ := strings.Cut(email, "@")
	return s.GenerateUnique(ctx, local)
}
