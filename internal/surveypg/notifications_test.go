// SPDX-License-Identifier: AGPL-3.0-only
package surveypg

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNotificationTitleUnicodeBoundAndFallback(t *testing.T) {
	for _, value := range []string{"", " \n\u3000", strings.Repeat("\u3000", 350) + "Visible title", strings.Repeat("測", 1024), strings.Repeat("𓀀", 512)} {
		got := notificationTitle(&value)
		if !utf8.ValidString(got) || len(got) > 1024 || strings.TrimSpace(got) == "" {
			t.Fatal("valid survey title poisons bounded inbox", len(got))
		}
	}
	if got := notificationTitle(nil); got != "a survey" {
		t.Fatal(got)
	}
	value := strings.Repeat("\u3000", 350) + "Visible title"
	if got := notificationTitle(&value); got != "Visible title" {
		t.Fatal(got)
	}
}
