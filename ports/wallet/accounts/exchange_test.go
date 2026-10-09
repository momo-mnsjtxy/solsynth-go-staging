// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"encoding/json"
	"strings"
	"testing"
)

func exchangeConfigFixture() ExchangeConfig {
	return ExchangeConfig{Version: "synthetic-v1", TreasuryAccountID: accountB, TreasuryWalletID: walletB, Rates: []ExchangeRate{{"points", "3", "golds", "1"}}}
}
func TestExchangeExactRemainderBoundsAndImmutableConfig(t *testing.T) {
	cfg := exchangeConfigFixture()
	p, e := NewExchangePolicy(cfg)
	if e != nil {
		t.Fatal(e)
	}
	cfg.Rates[0].TargetAmount = "999"
	q, e := p.Quote(" Points ", mustAmount(t, "1.0019"))
	if e != nil || q.SourceAmount.String() != "1.001" || q.TargetAmount.String() != "0.333" || q.RemainderMills != "2/3" {
		t.Fatal(q, e)
	}
	clone := p.Config()
	clone.Rates[0].TargetAmount = "777"
	repeat, e := p.Quote("points", q.SourceAmount)
	if e != nil || repeat != q {
		t.Fatal(repeat, e)
	}
	for _, amount := range []string{"0", "-1", "0.001"} {
		if _, e = p.Quote("points", mustAmount(t, amount)); e == nil {
			t.Fatal(amount)
		}
	}
	if _, e = p.Quote("missing", mustAmount(t, "1")); e == nil {
		t.Fatal("missing rate accepted")
	}
	cfg = exchangeConfigFixture()
	cfg.Rates[0] = ExchangeRate{"points", "1", "golds", "1"}
	p, e = NewExchangePolicy(cfg)
	if e != nil {
		t.Fatal(e)
	}
	q, e = p.Quote("points", mustAmount(t, "9007199254740993.001"))
	if e != nil || q.TargetAmount.String() != "9007199254740993.001" || q.RemainderMills != "0" {
		t.Fatal(q, e)
	}
	cfg.Rates[0].TargetAmount = "2"
	p, e = NewExchangePolicy(cfg)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = p.Quote("points", mustAmount(t, "79228162514264337593543950335")); e == nil {
		t.Fatal("overflow accepted")
	}
	cfg = exchangeConfigFixture()
	first, _ := NewExchangePolicy(cfg)
	cfg.Rates[0].SourceAmount = "3.000"
	second, _ := NewExchangePolicy(cfg)
	if first.Digest() != second.Digest() {
		t.Fatal("equivalent decimals changed digest")
	}
	for _, change := range []func(*ExchangeConfig){func(c *ExchangeConfig) { c.Version = "" }, func(c *ExchangeConfig) { c.TreasuryWalletID = "" }, func(c *ExchangeConfig) { c.Rates = append(c.Rates, ExchangeRate{"POINTS", "1", "golds", "1"}) }, func(c *ExchangeConfig) { c.Rates[0].TargetCurrency = "POINTS" }, func(c *ExchangeConfig) { c.Rates[0].SourceAmount = "1e3" }, func(c *ExchangeConfig) { c.Rates[0].SourceAmount = "0" }, func(c *ExchangeConfig) { c.Rates[0].SourceAmount = "0." + strings.Repeat("0", 28) + "1" }} {
		cfg = exchangeConfigFixture()
		change(&cfg)
		if _, e = NewExchangePolicy(cfg); e == nil {
			t.Fatal(cfg)
		}
	}
	b, e := json.Marshal(q)
	if e != nil || strings.Contains(string(b), "9.007") {
		t.Fatal(string(b), e)
	}
}
func TestExchangePaymentPurposeAndReplayBinding(t *testing.T) {
	p, _ := NewExchangePolicy(exchangeConfigFixture())
	q, _ := p.Quote("points", mustAmount(t, "3"))
	c := ExchangeCommand{ActorID: accountA, WalletID: walletA, Owner: Owner{AccountID: accountA}, Quote: q, IdempotencyKey: "synthetic-replay"}
	first := ExchangeBinding(c)
	c.IdempotencyKey = "other"
	if ExchangeBinding(c) != first {
		t.Fatal("replay key included in payment binding")
	}
	c.Quote.PolicyVersion = "synthetic-v2"
	if ExchangeBinding(c) == first {
		t.Fatal("rate version not bound")
	}
	for _, key := range []string{"", " key", "a\nb", strings.Repeat("x", 129), "a b"} {
		if ValidExchangeKey(key) {
			t.Fatal(key)
		}
	}
}
