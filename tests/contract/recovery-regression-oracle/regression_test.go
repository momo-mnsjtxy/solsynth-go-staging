// SPDX-License-Identifier: AGPL-3.0-only
package regression_test

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"solsynth.local/go-migration/ports/passport/badgediscovery"
	"solsynth.local/go-migration/ports/wallet/exchangecatalog"
)

func TestRecoveryRegressionsAgainstIndependentDotNet(t *testing.T) {
	var cases struct {
		Configs []struct{ Name, JSON string }
		Routes  []struct{ Name, Path string }
	}
	var expected struct {
		Configs []struct {
			Name     string
			Accepted bool
			Error    *string
		}
		Routes []struct {
			Name   string
			Status int
		}
	}
	read := func(name string, value any) {
		t.Helper()
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(b, value); err != nil {
			t.Fatal(err)
		}
	}
	read("cases.json", &cases)
	read("expected.json", &expected)
	if len(cases.Configs) != len(expected.Configs) || len(cases.Routes) != len(expected.Routes) || len(cases.Configs) == 0 || len(cases.Routes) == 0 {
		t.Fatal("case/result count mismatch")
	}
	for i, tc := range cases.Configs {
		want := expected.Configs[i]
		t.Run("configuration/"+tc.Name, func(t *testing.T) {
			if tc.Name != want.Name || want.Accepted != (want.Error == nil) {
				t.Fatal("invalid reference fixture")
			}
			_, err := exchangecatalog.LoadJSON(strings.NewReader(tc.JSON))
			if (err == nil) != want.Accepted {
				t.Fatalf("accepted=%t want %t, err=%v", err == nil, want.Accepted, err)
			}
		})
	}
	handler := badgediscovery.NewHandler(badgediscovery.Config{})
	for i, tc := range cases.Routes {
		want := expected.Routes[i]
		t.Run("route/"+tc.Name, func(t *testing.T) {
			if tc.Name != want.Name {
				t.Fatal("case ordering mismatch")
			}
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, httptest.NewRequest("GET", tc.Path, nil))
			if rr.Code != want.Status {
				t.Fatalf("status %d want %d", rr.Code, want.Status)
			}
			if badgediscovery.RouteMatches(tc.Path) != (want.Status == 200) {
				t.Fatal("host matcher differs from reference route")
			}
		})
	}
}
