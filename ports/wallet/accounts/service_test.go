package accounts

import (
	"context"
	"errors"
	"testing"
)

const accountA = "11000000-0000-0000-0000-000000000001"
const accountB = "11000000-0000-0000-0000-000000000002"
const walletA = "21000000-0000-0000-0000-000000000001"
const walletB = "21000000-0000-0000-0000-000000000002"
const realmID = "31000000-0000-0000-0000-000000000001"

func str(v string) *string { return &v }

type storeRecorder struct {
	Store
	wallets map[string]Wallet
	calls   int
	last    TransferCommand
	created Owner
}

func (r *storeRecorder) GetWallet(_ context.Context, id string) (Wallet, error) {
	r.calls++
	if w, ok := r.wallets[id]; ok {
		return w, nil
	}
	return Wallet{}, ErrNotFound
}
func (r *storeRecorder) DefaultWallet(ctx context.Context, account string) (Wallet, error) {
	for _, w := range r.wallets {
		if w.AccountID != nil && *w.AccountID == account {
			return w, nil
		}
	}
	return Wallet{}, ErrNotFound
}
func (r *storeRecorder) WalletByPublicID(ctx context.Context, id string) (Wallet, error) {
	for _, w := range r.wallets {
		if w.PublicID != nil && *w.PublicID == id {
			return w, nil
		}
	}
	return Wallet{}, ErrNotFound
}
func (r *storeRecorder) Transfer(_ context.Context, c TransferCommand) (Transaction, error) {
	r.calls++
	r.last = c
	return Transaction{ID: walletB, Amount: c.Amount, Status: Confirmed, PayerWalletID: c.PayerWalletID, PayeeWalletID: c.PayeeWalletID, Currency: c.Currency}, nil
}
func (r *storeRecorder) CreateWallet(_ context.Context, o Owner, n string) (Wallet, error) {
	r.calls++
	r.created = o
	return Wallet{ID: walletA, Name: n, Pockets: []Pocket{}}, nil
}
func (r *storeRecorder) ListWallets(_ context.Context, account string, realms []string) ([]Wallet, error) {
	r.calls++
	return []Wallet{}, nil
}

type accessFixture struct {
	permission, realm error
	realmCalls        int
	lastRole          RealmRole
}

func (a *accessFixture) Permission(context.Context, Principal, string) error { return a.permission }
func (a *accessFixture) Realm(_ context.Context, _ Principal, _ string, role RealmRole) error {
	a.realmCalls++
	a.lastRole = role
	return a.realm
}
func (a *accessFixture) RealmIDs(context.Context, Principal) ([]string, error) { return nil, a.realm }

type pinFixture struct {
	err      error
	calls    int
	received string
}

func (p *pinFixture) VerifyPayment(_ context.Context, _ Principal, pin string) error {
	p.calls++
	p.received = pin
	return p.err
}
func serviceFixture() (*Service, *storeRecorder, *accessFixture, *pinFixture, Principal) {
	r := &storeRecorder{wallets: map[string]Wallet{walletA: {ID: walletA, AccountID: str(accountA), Pockets: []Pocket{}}, walletB: {ID: walletB, AccountID: str(accountB), PublicID: str("DNW-ABCD-EFGH-JKLM"), Pockets: []Pocket{}}}}
	a := &accessFixture{}
	pin := &pinFixture{}
	return &Service{Store: r, Access: a, Payments: pin}, r, a, pin, Principal{accountA, "synthetic-session"}
}
func transferFixture(t *testing.T) TransferRequest {
	return TransferRequest{Amount: mustAmount(t, "12.3456"), Currency: "points", PayeeWalletID: str(walletB)}
}
func TestServiceAuthorityAndMandatoryPINGates(t *testing.T) {
	ctx := context.Background()
	s, r, a, pin, p := serviceFixture()
	request := transferFixture(t)
	s.Payments = nil
	if _, e := s.Transfer(ctx, p, request, "key"); e != ErrUnavailable || r.calls != 0 {
		t.Fatal(e, r.calls)
	}
	s.Payments = pin
	a.permission = ErrForbidden
	if _, e := s.Transfer(ctx, p, request, "key"); e != ErrForbidden || r.calls != 0 {
		t.Fatal(e, r.calls)
	}
	a.permission = nil
	pin.err = ErrPIN
	if _, e := s.Transfer(ctx, p, request, "key"); e != ErrPIN {
		t.Fatal(e)
	}
	if r.last.ActorID != "" {
		t.Fatal("mutation after PIN failure")
	}
	pin.err = nil
	request.PINCode = str("NO_PIN_PROVEDED")
	if _, e := s.Transfer(ctx, p, request, "key"); e != nil {
		t.Fatal(e)
	}
	if pin.received != "NO_PIN_PROVEDED" {
		t.Fatal("sentinel bypassed mandatory verifier")
	}
	if _, e := s.Get(ctx, Principal{}, walletA); e != ErrUnauthorized {
		t.Fatal(e)
	}
}
func TestServiceWalletOwnershipAndRealmRole(t *testing.T) {
	s, r, a, _, p := serviceFixture()
	ctx := context.Background()
	if _, e := s.Get(ctx, p, walletB); e != ErrForbidden {
		t.Fatal(e)
	}
	w := r.wallets[walletA]
	w.AccountID = nil
	w.RealmID = str(realmID)
	r.wallets[walletA] = w
	request := transferFixture(t)
	request.PayerWalletID = str(walletA)
	a.realm = ErrForbidden
	if _, e := s.Transfer(ctx, p, request, "key"); e != ErrForbidden {
		t.Fatal(e)
	}
	a.realm = nil
	if _, e := s.Transfer(ctx, p, request, "key"); e != nil || a.lastRole != Editor {
		t.Fatal(e, a.lastRole)
	}
	s.Access = nil
	if _, e := s.Get(ctx, p, walletA); e != ErrUnavailable {
		t.Fatal(e)
	}
	if _, e := s.List(ctx, p); e != ErrUnavailable {
		t.Fatal(e)
	}
}
func TestServiceResolutionPrecisionAndFingerprint(t *testing.T) {
	s, r, _, pin, p := serviceFixture()
	ctx := context.Background()
	q := transferFixture(t)
	q.PayeeAccountID = str(accountA)
	q.PayeePublicID = str("missing")
	if _, e := s.Transfer(ctx, p, q, "dup"); e != nil {
		t.Fatal(e)
	}
	first := r.last
	if first.Amount.String() != "12.345" || first.PayeeWalletID != walletB || first.PayerWalletID != walletA || first.IdempotencyKey != "dup" || len(first.Fingerprint) != 64 {
		t.Fatal(first)
	}
	q.PINCode = str("synthetic-new-proof")
	if _, e := s.Transfer(ctx, p, q, "dup"); e != nil {
		t.Fatal(e)
	}
	if r.last.Fingerprint != first.Fingerprint || pin.received != "synthetic-new-proof" {
		t.Fatal("PIN retained in fingerprint")
	}
	q.Amount = mustAmount(t, "12.346")
	_, _ = s.Transfer(ctx, p, q, "dup")
	if r.last.Fingerprint == first.Fingerprint {
		t.Fatal("amount missing from fingerprint")
	}
	q.PayeeWalletID = nil
	q.PayeePublicID = str("DNW-ABCD-EFGH-JKLM")
	if _, e := s.Transfer(ctx, p, q, ""); e != nil {
		t.Fatal(e)
	}
	q.PayeePublicID = nil
	if _, e := s.Transfer(ctx, p, q, ""); e != ErrInvalid {
		t.Fatal("self account accepted", e)
	}
}
func TestServiceValidationAndCreate(t *testing.T) {
	s, r, _, _, p := serviceFixture()
	ctx := context.Background()
	w, e := s.Create(ctx, p, CreateRequest{Name: " \t Travel  "})
	if e != nil || w.Name != "Travel" || r.created.AccountID != p.AccountID {
		t.Fatal(w, e)
	}
	w, e = s.Create(ctx, p, CreateRequest{RealmID: str(realmID)})
	if e != nil || w.Name != "Organization Wallet" || r.created.RealmID != realmID {
		t.Fatal(w, e)
	}
	for _, change := range []func(*TransferRequest){func(q *TransferRequest) { q.Amount = Amount{} }, func(q *TransferRequest) { q.Currency = " points " }, func(q *TransferRequest) { q.PayeeWalletID = str(walletA) }, func(q *TransferRequest) { q.PayeeWalletID = str("bad") }, func(q *TransferRequest) { q.TransferRequestID = str(walletA) }} {
		q := transferFixture(t)
		change(&q)
		_, e = s.Transfer(ctx, p, q, "")
		if e == nil {
			t.Fatal("invalid request passed", q)
		}
	}
	if !errors.Is(ErrUnavailable, ErrUnavailable) {
		t.Fatal("error identity")
	}
}
