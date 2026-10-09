package contract_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"solsynth.local/go-migration/ports/passport/badgediscovery"
)

const badgeSyntheticSVG = "<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 2 2\"><path d=\"M0 0h2v2H0z\"/></svg>\n"

func TestBadgesActualMVCOracle(t *testing.T) {
	var cases []struct {
		Name, Mode, Method, Path string
		TraceID                  *string `json:"trace_id"`
		Headers                  map[string]string
	}
	var results []struct {
		Name    string
		Status  int
		Headers map[string]string
		Body    string
	}
	read := func(path string, value any) {
		t.Helper()
		b, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(b, value); e != nil {
			t.Fatal(e)
		}
	}
	read("badges-oracle/cases.json", &cases)
	read("badges-oracle/expected.json", &results)
	if len(cases) != len(results) || len(cases) == 0 {
		t.Fatal("badge oracle case/result mismatch")
	}
	for i, test := range cases {
		expected := results[i]
		t.Run(test.Name, func(t *testing.T) {
			if test.Name != expected.Name {
				t.Fatal("badge oracle fixture ordering mismatch")
			}
			root := t.TempDir()
			ready, empty := filepath.Join(root, "ready"), filepath.Join(root, "empty")
			for _, dir := range []string{ready, empty} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(ready, "draw.svg"), []byte(badgeSyntheticSVG), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(ready, "empty.svg"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			paths := map[string]string{"none": "", "whitespace": "\u00a0", "missing": filepath.Join(root, "missing"), "empty": empty, "ready": ready, "file": filepath.Join(ready, "draw.svg")}
			iconsPath, ok := paths[test.Mode]
			if !ok {
				t.Fatal("unknown icon fixture mode")
			}
			handler := badgediscovery.NewHandler(badgediscovery.Config{IconsPath: iconsPath, TraceID: func(*http.Request) string {
				if test.TraceID != nil {
					return *test.TraceID
				}
				return "badge-reference-trace"
			}})
			request := httptest.NewRequest(test.Method, test.Path, nil)
			for key, value := range test.Headers {
				request.Header.Set(key, value)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != expected.Status {
				t.Fatalf("status %d want %d: %s", response.Code, expected.Status, response.Body.String())
			}
			gotHeaders := map[string]string{}
			for key, values := range response.Header() {
				gotHeaders[strings.ToLower(key)] = strings.Join(values, ",")
			}
			if !reflect.DeepEqual(gotHeaders, expected.Headers) {
				t.Fatalf("headers %+v want %+v", gotHeaders, expected.Headers)
			}
			if strings.HasPrefix(expected.Headers["content-type"], "application/json") {
				if !reflect.DeepEqual(exchangeJSON(t, response.Body.Bytes()), exchangeJSON(t, []byte(expected.Body))) {
					t.Fatalf("JSON response differs from actual MVC: %s", response.Body.String())
				}
			} else if response.Body.String() != expected.Body {
				t.Fatalf("raw response bytes %q want %q", response.Body.String(), expected.Body)
			}
		})
	}
}
