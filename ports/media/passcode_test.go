// SPDX-License-Identifier: AGPL-3.0-only
package media

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSharePasscodeControlledHeader(t *testing.T) {
	for _, path := range []string{"/api/files/" + syntheticID, "/api/files/" + syntheticID + "/info"} {
		server, _ := handler()
		request := httptest.NewRequest(http.MethodGet, path+"?passcode=sensitive-share-secret", nil)
		request.Header.Set("Authorization", "Bearer synthetic")
		output := httptest.NewRecorder()
		server.ServeHTTP(output, request)
		if output.Code < 400 {
			t.Fatal("URL secret accepted", output.Code)
		}
	}
	for _, values := range [][]string{{"correct"}, {"a", "b"}, {strings.Repeat("x", 257)}, {"bad\x00value"}} {
		request := httptest.NewRequest("GET", "/api/files/"+syntheticID, nil)
		for _, v := range values {
			request.Header.Add(BundlePasscodeHeader, v)
		}
		got, e := requestBundlePasscode(request)
		if len(values) == 1 && values[0] == "correct" {
			if e != nil || got != "correct" {
				t.Fatal(got, e)
			}
		} else if e == nil {
			t.Fatal("ambiguous header accepted", values)
		}
	}
}
