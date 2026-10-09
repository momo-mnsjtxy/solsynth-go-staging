// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFundSharesConserveExactMills(t *testing.T) {
	for _, kind := range []int{FundEven, FundRandom} {
		for _, raw := range []string{"0.003", "0.005", "1.001", "99999999999999999999999.999"} {
			total := mustAmount(t, raw)
			remaining := total
			sum := Amount{}
			for slots := 3; slots > 0; slots-- {
				part, e := FundShare(remaining, slots, kind, nil)
				if e != nil || part.Sign() <= 0 {
					t.Fatal(raw, slots, part, e)
				}
				remaining, e = remaining.Sub(part)
				if e != nil {
					t.Fatal(e)
				}
				sum, e = sum.Add(part)
				if e != nil {
					t.Fatal(e)
				}
			}
			if remaining.Sign() != 0 || sum.Cmp(total) != 0 {
				t.Fatal(raw, sum, remaining)
			}
		}
	}
	for _, slots := range []int{0, 4, 513} {
		if _, e := FundShare(mustAmount(t, "0.003"), slots, FundEven, nil); e == nil {
			t.Fatal(slots)
		}
	}
	if _, e := FundShare(mustAmount(t, "1"), 2, FundRandom, strings.NewReader("")); !errors.Is(e, io.EOF) {
		t.Fatal(e)
	}
}
func TestNormalizeFundTermsAndBounds(t *testing.T) {
	now := time.Now().UTC()
	valid := CreateFundRequest{Currency: "synthetic", TotalAmount: mustAmount(t, "0.005"), AmountOfSplits: 3, IsOpen: true}
	q, e := NormalizeFund(valid, now)
	if e != nil || *q.ExpirationHours != 24 || !q.IsOpen {
		t.Fatal(q, e)
	}
	for _, change := range []func(*CreateFundRequest){func(q *CreateFundRequest) { q.AmountOfSplits = 513 }, func(q *CreateFundRequest) { q.TotalAmount = mustAmount(t, "0.002") }, func(q *CreateFundRequest) { q.Currency = " synthetic" }, func(q *CreateFundRequest) { q.RecipientAccountIDs = []string{accountA, accountA} }, func(q *CreateFundRequest) { v := 169; q.ExpirationHours = &v }, func(q *CreateFundRequest) {
		q.IsRaising = true
		q.TargetAmount = mustAmount(t, "1")
		q.ContributionType = ContributionFixed
		q.ContributionAmount = mustAmount(t, "2")
	}} {
		v := valid
		change(&v)
		if _, e = NormalizeFund(v, now); e == nil {
			t.Fatal(v)
		}
	}
	valid.IsRaising = true
	valid.TargetAmount = mustAmount(t, "1")
	valid.IsOpen = false
	valid.RecipientAccountIDs = []string{accountB, accountA}
	q, e = NormalizeFund(valid, now)
	if e != nil || q.RecipientAccountIDs[0] != accountA || valid.RecipientAccountIDs[0] != accountB || q.TotalAmount.Cmp(q.TargetAmount) != 0 {
		t.Fatal(q, e)
	}
}

type fundRecorder struct {
	*storeRecorder
	FundRepository
	fund        Fund
	created     FundCommand
	contributed FundContributionCommand
}

func (f *fundRecorder) CreateFund(_ context.Context, c FundCommand) (Fund, error) {
	f.created = c
	return f.fund, nil
}
func (f *fundRecorder) Fund(context.Context, string, string) (Fund, error) { return f.fund, nil }
func (f *fundRecorder) ContributeFund(_ context.Context, c FundContributionCommand) (Transaction, error) {
	f.contributed = c
	return Transaction{ID: walletB, Amount: c.Amount}, nil
}

type fundPaymentFixture struct {
	pinFixture
	required         bool
	purpose, binding string
}

func (p *fundPaymentFixture) PreparePayment(ctx context.Context, actor Principal, pin, purpose, binding string) (context.Context, error) {
	p.received = pin
	p.purpose = purpose
	p.binding = binding
	if p.required && pin == "" {
		return ctx, ErrPIN
	}
	return ctx, nil
}
func TestFundServiceUsesBoundProofAndOptionalPIN(t *testing.T) {
	s, base, access, legacy, p := serviceFixture()
	repo := &fundRecorder{storeRecorder: base, fund: Fund{ID: realmID, IsRaising: true, Currency: "synthetic", BeneficiaryWalletID: walletB}}
	s.Store = repo
	if _, e := s.ContributeFund(context.Background(), p, realmID, ContributeFundRequest{Amount: mustAmount(t, "1")}); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	if legacy.calls != 0 {
		t.Fatal("legacy verifier substituted")
	}
	payment := &fundPaymentFixture{required: true}
	s.Payments = payment
	if _, e := s.ContributeFund(context.Background(), p, realmID, ContributeFundRequest{Amount: mustAmount(t, "1")}); !errors.Is(e, ErrPIN) || repo.contributed.ActorID != "" {
		t.Fatal(e)
	}
	payment.required = false
	if _, e := s.ContributeFund(context.Background(), p, realmID, ContributeFundRequest{Amount: mustAmount(t, "1")}); e != nil {
		t.Fatal(e)
	}
	if repo.contributed.Fingerprint != payment.binding || payment.purpose != PaymentFundContribute || payment.received != "" {
		t.Fatal(payment, repo.contributed)
	}
	old := repo.contributed
	old.Amount = mustAmount(t, "2")
	if old.Binding(repo.fund) == payment.binding {
		t.Fatal("amount not bound")
	}
	access.permission = ErrForbidden
	if _, e := s.CreateFund(context.Background(), p, CreateFundRequest{}, ""); !errors.Is(e, ErrForbidden) {
		t.Fatal(e)
	}
}
func TestFundPublicHTTPAndUnsupportedClientRoutes(t *testing.T) {
	s, base, _, _, _ := serviceFixture()
	s.Store = &fundRecorder{storeRecorder: base, fund: Fund{ID: realmID, Recipients: []FundRecipient{}}}
	h := NewHandler(s, authFixture{err: ErrUnauthorized})
	out := httptest.NewRecorder()
	h.ServeHTTP(out, httptest.NewRequest("GET", "/api/wallets/funds/"+realmID, nil))
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	out = httptest.NewRecorder()
	h.ServeHTTP(out, httptest.NewRequest("GET", "/api/wallets/funds", nil))
	if out.Code != 401 {
		t.Fatal(out.Code)
	}
	_, _, _, _, p := serviceFixture()
	h = NewHandler(s, authFixture{principal: p})
	for _, path := range []string{"/api/wallets/funds/recipients", "/api/wallets/funds/" + realmID + "/order"} {
		out = httptest.NewRecorder()
		h.ServeHTTP(out, httptest.NewRequest("GET", path, nil))
		if out.Code != 404 {
			t.Fatal(path, out.Code)
		}
	}
}
