// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

func fundFingerprint(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (s *Service) funds() (FundRepository, error) {
	v, ok := s.Store.(FundRepository)
	if !ok {
		return nil, ErrUnavailable
	}
	return v, nil
}
func (s *Service) fundPayment(ctx context.Context, p Principal, pin *string, purpose, binding string) (context.Context, error) {
	proof, ok := s.Payments.(PaymentAuthorizer)
	if !ok {
		return ctx, ErrUnavailable
	}
	raw := ""
	if pin != nil {
		raw = *pin
	}
	return proof.PreparePayment(ctx, p, raw, purpose, binding)
}
func (s *Service) CreateFund(ctx context.Context, p Principal, q CreateFundRequest, key string) (Fund, error) {
	var out Fund
	if e := s.ready(p); e != nil {
		return out, e
	}
	if e := s.permission(ctx, p, PermissionFunds); e != nil {
		return out, e
	}
	repo, e := s.funds()
	if e != nil {
		return out, e
	}
	// Deadline freshness is checked under the repository creation lock. Pure
	// normalization here keeps an idempotent replay valid after its deadline.
	q, e = NormalizeFund(q, time.Time{})
	if e != nil {
		return out, e
	}
	if len(key) > 128 || strings.ContainsAny(key, "\x00\r\n") {
		return out, ErrInvalid
	}
	requested := ""
	if q.PayerWalletID != nil {
		requested = *q.PayerWalletID
	}
	w, e := s.Get(ctx, p, requested)
	if e != nil {
		return out, e
	}
	// Authored fund ownership is individual. Realm campaigns require a separately
	// specified beneficiary/governance contract and are not inferred here.
	if w.AccountID == nil || *w.AccountID != p.AccountID {
		return out, ErrForbidden
	}
	pin := q.PINCode
	q.PINCode = nil
	q.PayerWalletID = &w.ID
	command := FundCommand{ActorID: p.AccountID, WalletID: w.ID, IdempotencyKey: key, Request: q}
	command.Fingerprint = command.Binding()
	ctx, e = s.fundPayment(ctx, p, pin, PaymentFundCreate, command.Fingerprint)
	if e != nil {
		return out, e
	}
	return repo.CreateFund(ctx, command)
}
func (s *Service) ContributeFund(ctx context.Context, p Principal, id string, q ContributeFundRequest) (Transaction, error) {
	var out Transaction
	if e := s.ready(p); e != nil {
		return out, e
	}
	if e := s.permission(ctx, p, PermissionFunds); e != nil {
		return out, e
	}
	if !ValidID(id) {
		return out, ErrInvalid
	}
	repo, e := s.funds()
	if e != nil {
		return out, e
	}
	fund, e := repo.Fund(ctx, p.AccountID, id)
	if e != nil {
		return out, e
	}
	if !fund.IsRaising {
		return out, ErrInvalid
	}
	amount := q.Amount
	if fund.ContributionType == ContributionFixed {
		amount = fund.ContributionAmount
	}
	if amount.Sign() <= 0 {
		return out, ErrAmount
	}
	requested := ""
	if q.PayerWalletID != nil {
		requested = *q.PayerWalletID
	}
	w, e := s.Get(ctx, p, requested)
	if e != nil {
		return out, e
	}
	if w.AccountID == nil || *w.AccountID != p.AccountID {
		return out, ErrForbidden
	}
	command := FundContributionCommand{ActorID: p.AccountID, FundID: id, WalletID: w.ID, Amount: amount}
	command.Fingerprint = command.Binding(fund)
	ctx, e = s.fundPayment(ctx, p, q.PINCode, PaymentFundContribute, command.Fingerprint)
	if e != nil {
		return out, e
	}
	return repo.ContributeFund(ctx, command)
}
func (s *Service) ReceiveFund(ctx context.Context, p Principal, id string) (Transaction, error) {
	if e := s.ready(p); e != nil {
		return Transaction{}, e
	}
	if e := s.permission(ctx, p, PermissionFunds); e != nil {
		return Transaction{}, e
	}
	repo, e := s.funds()
	if e != nil {
		return Transaction{}, e
	}
	return repo.ReceiveFund(ctx, p.AccountID, id)
}
func (s *Service) CancelFund(ctx context.Context, p Principal, id string) (Fund, error) {
	if e := s.ready(p); e != nil {
		return Fund{}, e
	}
	if e := s.permission(ctx, p, PermissionFunds); e != nil {
		return Fund{}, e
	}
	repo, e := s.funds()
	if e != nil {
		return Fund{}, e
	}
	return repo.CancelFund(ctx, p.AccountID, id)
}

func (c FundCommand) Binding() string {
	return fundFingerprint(struct {
		Actor, Wallet string
		Terms         CreateFundRequest
	}{c.ActorID, c.WalletID, c.Request})
}
func (c FundContributionCommand) Binding(f Fund) string {
	return fundFingerprint(struct {
		Actor, Fund, Wallet, Currency, Beneficiary string
		Amount                                     Amount
	}{c.ActorID, c.FundID, c.WalletID, f.Currency, f.BeneficiaryWalletID, c.Amount})
}
func (s *Service) Funds(ctx context.Context, p Principal, offset, take int, status *int) ([]Fund, int, error) {
	if e := s.ready(p); e != nil {
		return nil, 0, e
	}
	repo, e := s.funds()
	if e != nil {
		return nil, 0, e
	}
	return repo.Funds(ctx, p.AccountID, offset, take, status)
}
