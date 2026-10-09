package walletpg

import (
	"bytes"
	"context"
	"crypto/rand"
	"solsynth.local/go-migration/ports/wallet/accounts"
	"testing"
)

func TestPINMACIndependentSaltPepperAndValidation(t *testing.T) {
	salt, pepper := make([]byte, 32), make([]byte, 32)
	if _, e := rand.Read(salt); e != nil {
		t.Fatal(e)
	}
	if _, e := rand.Read(pepper); e != nil {
		t.Fatal(e)
	}
	a, e := DerivePINMAC("482971", salt, pepper)
	if e != nil || len(a) != 32 {
		t.Fatal(e)
	}
	b, e := DerivePINMAC("482971", salt, pepper)
	if e != nil || !bytes.Equal(a, b) {
		t.Fatal(e)
	}
	salt[0] ^= 1
	c, e := DerivePINMAC("482971", salt, pepper)
	if e != nil || bytes.Equal(a, c) {
		t.Fatal(e)
	}
	for _, pin := range []string{"", "NO_PIN_PROVEDED", "12345", "1234567890123", "12345x"} {
		if _, e = DerivePINMAC(pin, salt, pepper); e == nil {
			t.Fatal(pin)
		}
	}
	if _, e = DerivePINMAC("482971", salt, pepper[:16]); e == nil {
		t.Fatal("short pepper")
	}
}
func TestPINAndRepositoryMissingDependenciesFailClosed(t *testing.T) {
	p := accounts.Principal{AccountID: "a7000000-0000-0000-0000-000000000001", SessionID: "test"}
	verifier := NewPINVerifier(nil, nil)
	if e := verifier.VerifyPayment(context.Background(), p, ""); e != accounts.ErrUnavailable {
		t.Fatal(e)
	}
	if _, e := verifier.Status(context.Background(), p); e != accounts.ErrUnavailable {
		t.Fatal(e)
	}
	r := New(nil)
	if _, e := r.CreateWallet(context.Background(), accounts.Owner{AccountID: p.AccountID}, "test"); e != accounts.ErrUnavailable {
		t.Fatal(e)
	}
}
