// SPDX-License-Identifier: AGPL-3.0-only
package exchangecatalog

import (
	"strings"
	"testing"
)

// Independent .NET 10 configuration-provider checks reject comma-only empty
// containers. These are benign parser-regression fixtures, not valid trailing
// commas after values.
func TestTrailingCommaRequiresPrecedingValue(t *testing.T) {
	for _, input := range []string{`{,}`, `{"ignored":[,]}`, `{"ignored":{,}}`, `{"ignored":,}`, `{"ignored":[1,,]}`, `{"ignored":1,,}`, `{/* comment */,}`, `{"ignored":[/* comment */,]}`} {
		if _, err := LoadJSON(strings.NewReader(input)); err == nil {
			t.Errorf("accepted malformed JSON %s", input)
		}
	}
	for _, input := range []string{`{"ignored":1,}`, `{"ignored":[1,]}`, `{"ignored":{"nested":null,},}`, `{"ignored":[{},],}`, `{"ignored":1,/* comment */}`} {
		if _, err := LoadJSON(strings.NewReader(input)); err != nil {
			t.Errorf("rejected valid trailing comma %s: %v", input, err)
		}
	}
}
