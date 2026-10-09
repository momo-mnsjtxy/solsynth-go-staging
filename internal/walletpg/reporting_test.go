// SPDX-License-Identifier: AGPL-3.0-only
package walletpg

import (
	"encoding/json"
	"testing"

	a "solsynth.local/go-migration/ports/wallet/accounts"
)

func reportAmount(t *testing.T, s string) a.Amount {
	t.Helper()
	v, e := a.ParseAmount(s)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestReportSummaryExactCurrenciesAndOverflow(t *testing.T) {
	summary, in, out, net, e := reportSummary(nil)
	if e != nil || len(summary) != 0 || in.String() != "0.000" || out.String() != "0.000" || net.String() != "0.000" {
		t.Fatal(summary, in, out, net, e)
	}
	groups := []reportGroup{{"Transfer", "points", reportAmount(t, "9007199254740993.001"), reportAmount(t, "0.002")}, {"Payment", "points", reportAmount(t, "0.001"), reportAmount(t, "2.000")}}
	_, in, out, net, e = reportSummary(groups)
	if e != nil || in.String() != "9007199254740993.002" || out.String() != "2.002" || net.String() != "9007199254740991.000" {
		t.Fatal(in, out, net, e)
	}
	groups = append(groups, reportGroup{Type: "Transfer", Currency: "golds", Income: reportAmount(t, "1.001")})
	summary, in, out, net, e = reportSummary(groups)
	if e != nil || in != nil || out != nil || net != nil || summary["Transfer"].Currencies["golds"].Income.String() != "1.001" {
		t.Fatal(summary, in, out, net, e)
	}
	encoded, e := json.Marshal(a.WalletOverview{Summary: summary, TotalIncome: in, TotalSpending: out, NetTotal: net})
	if e != nil {
		t.Fatal(e)
	}
	var wire map[string]json.RawMessage
	if e = json.Unmarshal(encoded, &wire); e != nil {
		t.Fatal(e)
	}
	for _, field := range []string{"total_income", "total_spending", "net_total"} {
		if string(wire[field]) != "null" {
			t.Fatal("mixed currencies must retain explicit null totals", string(encoded))
		}
	}
	var roundTrip a.WalletOverview
	if e = json.Unmarshal(encoded, &roundTrip); e != nil || roundTrip.Summary["Transfer"].Currencies["points"].Income.String() != "9007199254740993.001" {
		t.Fatal("exact per-currency mills changed in JSON", string(encoded), e)
	}
	_, _, _, _, e = reportSummary([]reportGroup{{"Transfer", "points", reportAmount(t, "79228162514264337593543950335"), a.Amount{}}, {"Payment", "points", reportAmount(t, "0.001"), a.Amount{}}})
	if e == nil {
		t.Fatal("overflow silently rounded")
	}
}
