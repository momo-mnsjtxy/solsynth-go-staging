// SPDX-License-Identifier: AGPL-3.0-only
package tickets

import (
	"strings"
	"testing"
)

func TestTicketMediaIDsBoundedAndTextContract(t *testing.T) {
	id := "a7100000-0000-4000-8000-000000000001"
	if !ValidFileIDs(nil) || !ValidFileIDs([]string{id}) {
		t.Fatal("valid references rejected")
	}
	for _, ids := range [][]string{{""}, {"https://example.invalid/file"}, {id, id}, {id, strings.ToUpper(id)}, make([]string, 33)} {
		if ValidFileIDs(ids) {
			t.Fatal("invalid references accepted", ids)
		}
	}
	v := TicketCreate{Title: "Synthetic support", Content: "File explanation", FileIDs: []string{id}}
	if e := ValidateTicket(v); e != nil {
		t.Fatal(e)
	}
	v.Content = ""
	if e := ValidateTicket(v); e == nil {
		t.Fatal("attachment-only source ambiguity silently accepted")
	}
}
